// Command ci — точка входа CLI-утилиты ci-shell.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/f3rym/ci-shell/internal/cache"
	"github.com/f3rym/ci-shell/internal/config"
	"github.com/f3rym/ci-shell/internal/env"
	"github.com/f3rym/ci-shell/internal/joburl"
	"github.com/f3rym/ci-shell/internal/provider"
	"github.com/f3rym/ci-shell/internal/provider/gitlab"
	"github.com/f3rym/ci-shell/internal/render"
	"github.com/f3rym/ci-shell/internal/repo"
	"github.com/f3rym/ci-shell/internal/runner"
	"github.com/f3rym/ci-shell/internal/token"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "shell":
		runShell(os.Args[2:])
	case "secrets":
		runSecrets(os.Args[2:])
	case "apply":
		runApply(os.Args[2:])
	default:
		printUsage()
		os.Exit(2)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "использование: ci shell [--image <образ>] [--here] <ссылка на джобу GitLab | номер джобы>")
	fmt.Fprintln(os.Stderr, "номер джобы работает из каталога репозитория проекта — хост и проект берутся из git remote origin")
	fmt.Fprintln(os.Stderr, "--here кладёт код джобы в текущий каталог и оставляет его там после выхода из шелла")
	fmt.Fprintln(os.Stderr, "флаги ставятся до позиционного аргумента (разбор аргументов stdlib другого порядка не понимает)")
	fmt.Fprintln(os.Stderr, "ci secrets — открыть файл секретов в редакторе ($VISUAL/$EDITOR, иначе vi); файл создаётся сам при первом запуске с недостающими значениями")
	fmt.Fprintln(os.Stderr, "ci apply [--job <id>] [--force] — перенести сохранённые правки воспроизведённой джобы в текущий репозиторий трёхсторонним мерджем, не коммитя")
	fmt.Fprintln(os.Stderr, "правки ищутся только среди патчей того проекта, чей remote origin у текущего репозитория; --force снимает требование иметь коммит джобы в этом репозитории")
}

// defaultConfigPath — путь к конфиг-файлу токенов, зафиксированный в
// 01-SKELETON.md. Показывается пользователю как есть, без вычисления
// фактического пути: token.configPath не экспортирован, а формула пути
// не зависит от конкретного запуска.
const defaultConfigPath = "${XDG_CONFIG_HOME:-~/.config}/ci-shell/config.yml"

// explain превращает ошибку в текст с конкретным следующим шагом. Каждая
// предсказуемая поломка — нет токена, небезопасные права, кривая ссылка,
// отклонённый токен, отсутствующая джоба, нераскрываемый конфиг,
// нереализованный метод — называет действие, а не просто констатирует
// поломку (idea §7).
func explain(err error) string {
	switch {
	// Отмена контекста — это Ctrl-C/SIGTERM от самого пользователя:
	// честное «прервано», без доменной ошибки и без советов.
	case errors.Is(err, context.Canceled):
		return "прервано пользователем"
	case errors.Is(err, token.ErrNoToken):
		return fmt.Sprintf(
			"%s\nзадайте токен переменной CI_SHELL_TOKEN или GITLAB_TOKEN, либо положите его в конфиг-файл %s:\n"+
				"  hosts:\n    <хост>:\n      token: <значение токена>\n"+
				"токену достаточно области read_api",
			err, defaultConfigPath,
		)
	case errors.Is(err, token.ErrInsecurePermissions):
		return fmt.Sprintf("%s\nпочините правами доступа: chmod 600 <путь к файлу выше>", err)
	case errors.Is(err, env.ErrInsecureSecrets):
		return fmt.Sprintf("%s\nпочините правами доступа: chmod 600 <путь к файлу выше>", err)
	case errors.Is(err, joburl.ErrNotAJobURL):
		return fmt.Sprintf("%s\nожидается ссылка на джобу вида https://gitlab.com/<группа>/<проект>/-/jobs/<id>", err)
	case errors.Is(err, joburl.ErrInsecureScheme):
		return fmt.Sprintf("%s\nподдерживается только https", err)
	case errors.Is(err, provider.ErrUnauthorized):
		return fmt.Sprintf("%s\nпроверьте область действия токена (нужна как минимум read_api) и срок его годности", err)
	case errors.Is(err, provider.ErrJobNotFound):
		return fmt.Sprintf("%s\nлибо джобы с таким id нет, либо у токена нет доступа к этому проекту", err)
	case errors.Is(err, provider.ErrConfigNotFound):
		return fmt.Sprintf("%s\n.gitlab-ci.yml отсутствует на коммите этой джобы — метаданные джобы это не отменяет", err)
	case errors.Is(err, provider.ErrUnresolvedRefs):
		return fmt.Sprintf("%s\nраскрытие include/extends/!reference — требование NEXT-02, вне v0.1.0; метаданные джобы напечатаны без образа и шагов", err)
	case errors.Is(err, provider.ErrNotSupported):
		return err.Error()
	case errors.Is(err, repo.ErrGitUnavailable):
		return fmt.Sprintf("%s\nпоставьте git — утилита ходит в него за кодом коммита джобы", err)
	case errors.Is(err, repo.ErrNotAGitRepo):
		return fmt.Sprintf("%s\nзапустите ci shell из рабочей копии того же проекта, что и упавшая джоба, либо передайте полную ссылку на джобу", err)
	case errors.Is(err, repo.ErrNoOrigin):
		return fmt.Sprintf("%s\nнастройте remote origin (git remote add origin <ссылка>), либо передайте полную ссылку на джобу вместо номера", err)
	case errors.Is(err, repo.ErrRemoteUnparsable):
		return fmt.Sprintf("%s\nпередайте полную ссылку на джобу вместо номера — эту форму remote origin утилита не распознаёт", err)
	case errors.Is(err, cache.ErrUnavailable):
		return fmt.Sprintf("%s\nзадайте XDG_CACHE_HOME или HOME — утилите нужен постоянный каталог для файла окружения и worktree", err)
	case errors.Is(err, config.ErrUnavailable):
		return fmt.Sprintf("%s\nзадайте XDG_CONFIG_HOME или HOME — утилите нужен каталог для файла настроек места чекаута", err)
	case errors.Is(err, repo.ErrCheckoutExists):
		return fmt.Sprintf("%s\nуберите каталог руками или запустите без флага --here", err)
	case errors.Is(err, repo.ErrCommitNotFound):
		return fmt.Sprintf("%s\nу джобы нет коммита — воспроизводить нечего", err)
	case errors.Is(err, repo.ErrUnsafeProject):
		return fmt.Sprintf("%s\nпроверьте ссылку и путь проекта", err)
	case errors.Is(err, repo.ErrMirrorFailed):
		return fmt.Sprintf("%s\nпроверьте права и место на диске в каталоге кэша", err)
	case errors.Is(err, repo.ErrFetchFailed):
		return fmt.Sprintf("%s\nпроверьте область действия токена и доступность проекта — читать репозиторий тем же токеном, что и API", err)
	case errors.Is(err, repo.ErrWorktreeFailed):
		return fmt.Sprintf("%s\nснимите зависший worktree: git worktree prune", err)
	case errors.Is(err, repo.ErrNoPatch):
		return fmt.Sprintf("%s\nсначала запустите ci shell и почините шаг в контейнере", err)
	case errors.Is(err, repo.ErrPatchUnsafe):
		return fmt.Sprintf("%s\nпатч не применён — проверьте, тот ли это репозиторий", err)
	case errors.Is(err, repo.ErrApplyFailed):
		return fmt.Sprintf("%s\nразрешите конфликт и повторите", err)
	case errors.Is(err, runner.ErrDockerUnavailable):
		return fmt.Sprintf("%s\nпоставьте Docker — это единственная внешняя зависимость утилиты", err)
	case errors.Is(err, runner.ErrDaemonUnavailable):
		return fmt.Sprintf("%s\nзапустите Docker и повторите", err)
	case errors.Is(err, runner.ErrNoImage):
		return fmt.Sprintf("%s\n%s: похоже на shell-executor или образ по умолчанию в конфиге ранера; если выше было предупреждение о неразобранном конфиге пайплайна, дело в нём", err, unreproducibleNote(err))
	case errors.Is(err, runner.ErrImageUnavailable):
		return fmt.Sprintf("%s\nпроверьте docker login для приватного реестра и доступность самого реестра", err)
	case errors.Is(err, runner.ErrNoShell):
		return fmt.Sprintf("%s\n%s: в образе нет ни bash, ни sh", err, unreproducibleNote(err))
	case errors.Is(err, runner.ErrContainerFailed):
		return fmt.Sprintf("%s\nконтейнер не поднялся, подробности см. выше", err)
	default:
		return err.Error()
	}
}

// unreproducibleNote отличает джобу, которая не воспроизводится этой
// утилитой в принципе (runner.Unreproducible), от временно неготовой машины
// пользователя: в первом случае не советуем повторить попытку, а честно
// говорим, что дело не в машине пользователя (idea §7).
func unreproducibleNote(err error) string {
	if runner.Unreproducible(err) {
		return "джоба не воспроизводится этой утилитой, повторная попытка не поможет"
	}
	return "проверьте состояние машины и повторите"
}

// checkoutBase — единственная точка, где решается место чекаута кода джобы
// (REPO-04). Пакет repo получает готовый каталог и признак сохранения, а
// саму политику выбора не знает.
//
// При here (--here) — текущий рабочий каталог, признак сохранения истинный,
// файл настроек не читается и не пишется: флаг разовый по определению
// (idea-0.2.0 §4), и сохранять его значение означало бы превратить
// исключение в новую политику.
//
// Иначе читаются настройки. Непустой сохранённый каталог используется как
// есть: ведущий "~" раскрывается через HOME, путь приводится к абсолютному,
// каталог создаётся os.MkdirAll с правами 0o700.
//
// Сохранённого каталога нет и запуск интерактивный (token.Interactive()): в
// поток ошибок печатается вопрос, называющий предлагаемый каталог по
// умолчанию — пустой ответ его принимает, непустой задаёт свой путь. Ответ
// сохраняется вызовом config.Save; неудача сохранения не прерывает запуск.
//
// Сохранённого каталога нет и запуск неинтерактивный: вопрос не задаётся —
// берётся каталог по умолчанию, а в поток ошибок уходит одна строка,
// называющая этот каталог и ключ checkout_dir файла настроек. Тот же
// предохранитель от зависшего процесса, что у вопроса о токене (план
// 04-01): в пайпе и в CI спрашивать некого.
func checkoutBase(here bool) (base string, persist bool, err error) {
	defaultDir, err := cache.Dir("checkouts")
	if err != nil {
		return "", false, err
	}

	if here {
		wd, err := os.Getwd()
		if err != nil {
			return "", false, fmt.Errorf("не удалось определить текущий каталог: %w", err)
		}
		return wd, true, nil
	}

	settings, settingsPath, err := config.Load()
	if err != nil {
		return "", false, err
	}
	if settings.CheckoutDir != "" {
		dir := settings.CheckoutDir
		if home := os.Getenv("HOME"); home != "" && strings.HasPrefix(dir, "~") {
			dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", false, fmt.Errorf("не удалось привести каталог чекаутов %s к абсолютному пути: %w", dir, err)
		}
		if err := os.MkdirAll(abs, 0o700); err != nil {
			return "", false, fmt.Errorf("не удалось создать каталог чекаутов %s: %w", abs, err)
		}
		return abs, false, nil
	}

	if token.Interactive() {
		fmt.Fprintf(os.Stderr, "где хранить код воспроизводимых джоб? Enter — %s: ", defaultDir)
		line, readErr := bufio.NewReader(os.Stdin).ReadString('\n')
		answer := strings.TrimSpace(line)
		if readErr != nil && answer == "" {
			answer = ""
		}
		chosen := defaultDir
		if answer != "" {
			abs, absErr := filepath.Abs(answer)
			if absErr != nil {
				return "", false, fmt.Errorf("не удалось привести каталог %s к абсолютному пути: %w", answer, absErr)
			}
			chosen = abs
		}
		if err := os.MkdirAll(chosen, 0o700); err != nil {
			return "", false, fmt.Errorf("не удалось создать каталог чекаутов %s: %w", chosen, err)
		}
		if savedPath, saveErr := config.Save(config.Settings{CheckoutDir: chosen}); saveErr != nil {
			fmt.Fprintf(os.Stderr, "предупреждение: не удалось сохранить место чекаута: %s; используется только в этом прогоне\n", saveErr)
		} else {
			fmt.Fprintf(os.Stderr, "место чекаута сохранено в %s\n", savedPath)
		}
		return chosen, false, nil
	}

	fmt.Fprintf(os.Stderr, "код будет чекаутиться в %s; поменять — ключ checkout_dir в %s\n", defaultDir, settingsPath)
	return defaultDir, false, nil
}

// reproduce готовит код упавшей джобы на нужном коммите (repo.Materialize —
// worktree рабочей копии или чекаут из зеркала в кэше) и поднимает
// контейнер. Функция владеет очисткой через defer и никогда не завершает
// процесс — ни через fail, ни прямым выходом: иначе отложенная очистка не
// выполнится, и на машине останутся чекаут и контейнер. Любая поломка
// возвращается наружу как ошибка; печать и завершение делает runShell уже
// после возврата из reproduce.
func reproduce(ctx context.Context, ref joburl.Ref, job provider.Job, jobCfg provider.JobConfig, img runner.ImageChoice, e env.Environment, tok token.Token, base string, persist bool, pr render.Progress) error {
	// Ctrl-C и SIGTERM во время загрузки образа или прогона шагов отменяют
	// контекст: дочерний процесс docker получает сигнал, вызов возвращает
	// ошибку, reproduce возвращается наружу — и отложенная очистка ниже
	// снимает контейнер, а затем worktree.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Создание клиента docker и дешёвая проверка воспроизводимости идут в
	// самом начале — до подготовки кода и до тяги образа: пользователь с
	// невоспроизводимой джобой или без запущенного Docker получает ответ за
	// секунду и без единого артефакта на диске (idea §7).
	d, err := runner.New(pr)
	if err != nil {
		return err
	}
	pr.Stage("проверяю Docker и воспроизводимость джобы…")
	if err := runner.Preflight(ctx, d, img.Ref); err != nil {
		return err
	}

	code, err := repo.Materialize(ctx, repo.Request{
		Host:        ref.Host,
		ProjectPath: ref.ProjectPath,
		SHA:         job.CommitSHA,
		Ref:         job.Ref,
		Tag:         job.Tag,
		Base:        base,
		Persist:     persist,
		Token:       tok.Secret,
	}, pr)
	if err != nil {
		return err
	}
	if code.Persist {
		msg := fmt.Sprintf("код джобы останется в %s\n", code.Dir)
		if !code.SelfContained {
			msg += fmt.Sprintf("на локальной ветке остаётся зарегистрированный worktree — снять: git worktree remove --force %s\n", code.Dir)
		}
		fmt.Fprint(os.Stderr, msg)
	}
	// Фоновый контекст: отмена основного контекста не должна помешать
	// уборке временного чекаута. Неудача снятия печатается предупреждением
	// — путь остатка и готовая команда добивки входят в текст самой ошибки
	// (см. Checkout.Remove) — пользователь не должен обнаруживать остаток
	// через неделю. При Persist Checkout.Remove ничего не делает — код
	// остаётся на диске, как и просил пользователь флагом --here.
	defer func() {
		if err := code.Remove(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "предупреждение: %s\n", err)
		}
	}()

	// Рабочий каталог берётся у доменной модели (правило GitLab по
	// умолчанию живёт в env.Environment.WorkDir, не здесь) — от него зависит
	// ещё и точка монтирования файловых переменных ниже.
	workDir := e.WorkDir()

	// Точка монтирования файловых переменных — рабочий каталог с суффиксом
	// ".tmp": так делает сам ранер GitLab, и это соседний каталог, а не
	// подкаталог кода, поэтому в чекаут (git-репозиторий, из которого
	// Фаза 7 будет забирать правки пользователя) ничего не попадает.
	fv, err := runner.MaterializeFileVars(e.FileContents(), workDir+".tmp")
	if err != nil {
		return err
	}
	// Регистрируется немедленно и ОБЯЗАТЕЛЬНО РАНЬШЕ отложенного снятия
	// контейнера ниже: отложенные вызовы разворачиваются в обратном
	// порядке, поэтому зарегистрированный раньше выполнится позже — файлы
	// исчезнут, когда контейнер уже снят и читать их некому.
	defer func() {
		if err := fv.Remove(); err != nil {
			fmt.Fprintf(os.Stderr, "предупреждение: %s\n", err)
		}
	}()
	if len(fv.Skipped) > 0 {
		fmt.Fprintf(os.Stderr, "предупреждение: файловые переменные не материализованы, имя не годится для файла: %s\n", strings.Join(fv.Skipped, ", "))
	}

	// d.EnsureImage остаётся здесь, после подготовки кода: это уже дорогая
	// операция с собственным прогрессом, а не дешёвая проверка Preflight.
	if err := d.EnsureImage(ctx, img.Ref); err != nil {
		return err
	}

	envMap := e.Map(fv.Paths)

	if len(e.Missing) > 0 {
		keys := make([]string, 0, len(e.Missing))
		for _, m := range e.Missing {
			keys = append(keys, m.Key)
		}
		fmt.Fprintf(os.Stderr, "предупреждение: без значений маскированных переменных (%s, файл %s) может воспроизвестись не тот сбой\n", strings.Join(keys, ", "), e.SecretsPath)
	}

	// Собирается в переменную, а не литералом на месте вызова: ту же
	// спецификацию переиспользует чистый прогон (runner.CleanRun) ниже —
	// второе построение по памяти рано или поздно разъехалось бы с первым,
	// и «чистый прогон» проверял бы уже не ту джобу.
	spec := runner.Spec{
		Image:         img.Ref,
		SourceDir:     code.Dir,
		WorkDir:       workDir,
		Env:           envMap,
		JobID:         job.ID,
		FileVarsDir:   fv.Dir,
		FileVarsMount: fv.Mount,
	}
	c, skipped, err := d.Start(ctx, spec)
	if err != nil {
		return err
	}
	// Регистрируется после defer снятия worktree, чтобы порядок
	// разворачивания снял сначала контейнер, а потом каталог, который в
	// него примонтирован. Неудача снятия печатается предупреждением с
	// готовой командой добивки.
	defer func() {
		if err := c.Remove(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "предупреждение: %s\nдобейте вручную: docker rm -f %s\n", err, c.ID)
		}
	}()
	// Регистрируется ПОСЛЕ defer снятия контейнера, поэтому по LIFO
	// выполняется РАНЬШЕ него — пока контейнер ещё жив и команду ещё есть
	// где выполнить (idea-0.2.0 §8). Контейнер работает под root, каталог
	// смонтирован из постоянного кэша — без возврата владельца файлы root
	// накапливались бы в кэше пользователя навсегда.
	//
	// Возврат владельца идёт через Container.Chown, а не через оболочку
	// внутри контейнера: workDir — это значение CI_PROJECT_DIR, пришедшее из
	// переменных проекта/группы по API либо из файла секретов, и подстановка
	// его в строку для sh -c дала бы исполнение произвольной команды под root
	// в каталоге, смонтированном с хоста. Путь уходит отдельным элементом
	// argv, как в MkdirAll и Chmod.
	//
	// Отложенным вызовом дело не ограничивается: ветка чистого прогона ниже
	// снимает контейнер явно, и к моменту разворачивания defer выполнять
	// команду было бы уже негде. Поэтому возврат владельца вынесен в
	// идемпотентную функцию: ветка чистого прогона зовёт её сама перед
	// снятием контейнера, а отложенный вызов остаётся страховкой всех
	// остальных путей выхода и после явного вызова не делает ничего.
	uid, gid := os.Getuid(), os.Getgid()
	owner := ""
	if uid >= 0 && gid >= 0 {
		owner = fmt.Sprintf("%d:%d", uid, gid)
	}
	ownerRestored := false
	restoreOwner := func() {
		if owner == "" || ownerRestored {
			return
		}
		ownerRestored = true
		// Ошибка не фатальна — в образе может не быть chown, и на этот
		// случай в Checkout.Remove уже есть готовая команда добивки. Но и
		// не молча: иначе пользователь узнаёт о root-файлах в своём кэше
		// только когда об них спотыкается уборка.
		if err := c.Chown(context.Background(), owner, workDir); err != nil {
			fmt.Fprintf(os.Stderr, "предупреждение: %s\nфайлы в каталоге кода могли остаться под root\n", err)
		}
	}
	defer restoreOwner()

	if len(skipped) > 0 {
		fmt.Fprintf(os.Stderr, "предупреждение: переменные не попали в контейнер, имя или значение несовместимы с файлом окружения: %s\n", strings.Join(skipped, ", "))
	}

	// Проба оболочки идёт до прогона шагов: найденная оболочка (bash
	// предпочтительно, как в docker-executor GitLab) используется и для
	// шагов, и для интерактивного шелла — башизмы в шагах не падают там,
	// где CI их исполнял. Образ без оболочки отсеивается до исполнения
	// чего-либо внутри контейнера.
	if err := c.DetectShell(ctx); err != nil {
		return err
	}

	steps := runner.Steps(jobCfg)
	// Нулевое значение (Failed == nil, FailedIndex == 0) остаётся, когда
	// шагов нет вовсе — тем же кодом ниже кладёт в контейнер retry,
	// сообщающий, что перезапускать нечего.
	var outcome runner.Outcome
	if len(steps) == 0 {
		fmt.Fprintln(os.Stderr, "предупреждение: в конфиге джобы нет шагов — возможно, всё делает entrypoint образа")
	} else {
		o, err := runner.RunSteps(ctx, c, steps, os.Stdout, os.Stderr, pr)
		if err != nil {
			return err
		}
		outcome = o
		if outcome.Failed != nil {
			fmt.Fprintf(os.Stderr, "шаг %d/%d (%s) упал с кодом %d: %s\nшелл открывается сразу после последнего успешного шага\n"+
				"  retry           — перезапустить этот шаг против того же грязного состояния, что оставили предыдущие шаги\n"+
				"  retry --rest    — прогнать оставшиеся шаги, тоже против грязного состояния\n"+
				"  единственная честная проверка «в CI будет зелено» — прогон начисто в свежем контейнере, который утилита предложит после выхода из шелла\n"+
				"  файлы правятся в своей IDE на хосте: каталог смонтирован, редактор внутри образа не нужен\n",
				outcome.FailedIndex, outcome.Total, outcome.Failed.Section, outcome.ExitCode, outcome.Failed.Command)
		} else {
			fmt.Fprintf(os.Stderr, "все %d шагов прошли — сбой не воспроизвёлся. Вероятные причины: недостающие маскированные значения (см. предупреждение выше), артефакты предыдущих стадий и кэш ранера, которые v0.1.0 не восстанавливает\n", outcome.Total)
		}
	}

	// Кладётся в контейнер до выдачи шелла: хост слепнет внутри
	// интерактивного exec, поэтому знание об упавшем шаге должно быть уже
	// внутри. Неудача не прерывает работу (принцип v0.2.0: доводить до
	// шелла) — только предупреждает, что шаг придётся перезапускать руками.
	if err := runner.InstallRetry(ctx, c, steps, outcome.FailedIndex); err != nil {
		fmt.Fprintf(os.Stderr, "предупреждение: %s — команда retry в контейнере недоступна, упавший шаг придётся перезапускать руками\n", err)
	}

	// Ключи файловых переменных, материализованных файлами (задача 1,
	// internal/runner/filevars.go): отсортированный список из карты путей —
	// источник правды теперь то, что реально легло на диск, а не признак
	// Kind в собранном окружении. Прежний обход e.Vars по типу переменной
	// для баннера больше не нужен.
	fileVars := make([]string, 0, len(fv.Paths))
	for k := range fv.Paths {
		fileVars = append(fileVars, k)
	}
	sort.Strings(fileVars)

	banner := render.BannerInput{
		ImageRef:        img.Ref,
		ImageAssumed:    img.Source == runner.ImageSourceDefault,
		Missing:         e.Missing,
		SecretsPath:     e.SecretsPath,
		FileVars:        fileVars,
		FileVarsSkipped: fv.Skipped,
		CodeSource:      fmt.Sprintf("%s — %s", code.Source, code.Dir),
		GitDirExternal:  !code.SelfContained,
	}
	// Присваивается отдельно, а не полем литерала: runner.Spec несёт
	// одноимённое поле FileVarsDir с другим смыслом (каталог на хосте) —
	// здесь это каталог ВНУТРИ контейнера, и явный шаг исключает путаницу.
	banner.FileVarsDir = fv.Mount
	if img.Source == runner.ImageSourceFlag && img.Configured != "" {
		banner.ImageOverridden = img.Configured
	}
	// Баннер — последнее, что видит пользователь ДО того, как терминал
	// уйдёт в контейнер: печатается строго перед c.Shell, иначе уедет за
	// спину пользователю, уже работающему внутри.
	if err := render.Banner(os.Stderr, banner); err != nil {
		return err
	}

	pr.Stage("вы внутри контейнера, рабочий каталог %s; для выхода — exit или Ctrl-D", workDir)
	if err := c.Shell(ctx); err != nil {
		return err
	}

	// Патч снимается сразу после возврата из шелла и строго до вопроса о
	// чистом прогоне ниже: чистый прогон исполняет шаги в том же
	// смонтированном каталоге и может насоздавать там файлов сборки — патч
	// обязан отражать то, что чинил человек, а не то, что оставил после
	// себя прогон. Материализованные файловые переменные в патч попасть не
	// могут: они лежат кэшем-соседом workDir+".tmp", а не внутри code.Dir
	// (контракт плана 06-02) — Capture строит разницу только из состояния
	// git-чекаута.
	//
	// Фоновый контекст, как у всей уборки ниже, и по более веской причине:
	// SIGTERM или SIGINT, дошедший до группы процессов после возврата из
	// шелла, отменил бы основной контекст, каждый вызов git внутри Capture
	// упал бы мгновенно, патч не был бы записан — а отложенный
	// code.Remove(context.Background()) всё равно снёс бы чекаут вместе с
	// правками пользователя. Правки — это ровно то, ради чего он тут сидел.
	patch, _, captureErr := repo.Capture(context.Background(), code.Dir, ref.Host, ref.ProjectPath, job.ID, job.CommitSHA)
	switch {
	case errors.Is(captureErr, repo.ErrNoChanges):
		fmt.Fprintln(os.Stderr, "правок в чекауте нет — переносить нечего")
	case captureErr != nil:
		// Не отдать патч обиднее, чем не дойти до шелла, но обрывать на
		// этом уже полученную ценность нельзя.
		fmt.Fprintf(os.Stderr, "предупреждение: %s\n", explain(captureErr))
	default:
		fmt.Fprintf(os.Stderr, "правки сохранены (файл доступен только вам): %s\n", patch.Path)
		fmt.Fprintf(os.Stderr, "перенести их в свой репозиторий — ci apply --job %d\n", job.ID)
	}

	// Петля фикса замыкается здесь: чинить было что, только если шаг упал.
	// Третий, честный уровень доверия — прогон начисто в свежем
	// контейнере — предлагается вопросом с хоста только в интерактивном
	// запуске; форма изнутри контейнера (retry --clean) требует канала
	// контейнер→хост и в этой фазе не появляется.
	if outcome.Failed != nil {
		if token.Interactive() {
			question := fmt.Sprintf(
				"шаг %d/%d (%s) чинился — прогнать все шаги начисто в свежем контейнере на исправленном коде?",
				outcome.FailedIndex, outcome.Total, outcome.Failed.Section,
			)
			if confirm(question) {
				// Сначала снимается первый контейнер — двух живых
				// контейнеров одной джобы быть не должно, и второй не
				// должен драться за смонтированный каталог с первым.
				// Идемпотентность Remove делает отложенное снятие ниже
				// безвредным.
				//
				// Владелец файлов возвращается ДО снятия: после него
				// команду выполнять уже негде, а отложенный вызов
				// разворачивается только в самом конце функции. Второй
				// контейнер вернёт владельца за собой сам — CleanRun
				// получает owner тем же значением.
				restoreOwner()
				// Фоновый контекст, как у всей остальной уборки: с уже
				// отменённым ctx exec.CommandContext убил бы docker rm, и
				// снятие отказывало бы гарантированно.
				//
				// Отказ снятия — не предупреждение, а причина не запускать
				// чистый прогон вовсе: инвариант «двух живых контейнеров
				// одной джобы быть не должно» либо соблюдается, либо второй
				// контейнер поднимается на тот же смонтированный каталог, где
				// ещё работает первый, и «единственная честная проверка»
				// проверяет уже не то. Отложенное снятие ниже остаётся
				// зарегистрированным (removed выставляется только при успехе)
				// и попробует ещё раз.
				if err := c.Remove(context.Background()); err != nil {
					fmt.Fprintf(os.Stderr, "предупреждение: %s\nчистый прогон пропущен: старый контейнер ещё жив\n", err)
				} else {
					cleanOutcome, err := runner.CleanRun(ctx, d, spec, steps, owner, os.Stdout, os.Stderr, pr)
					switch {
					case err != nil:
						// Сломался сам инструмент (docker, прерывание) — не
						// превращает весь запуск в неудачу: шелл пользователь
						// уже получил, ценность уже доставлена.
						fmt.Fprintf(os.Stderr, "предупреждение: чистый прогон не завершился: %s\n", explain(err))
					case cleanOutcome.Failed == nil:
						fmt.Fprintln(os.Stderr, "✓ джоба воспроизводится зелёной, можно пушить")
					default:
						fmt.Fprintf(os.Stderr, "чистый прогон: шаг %d/%d (%s) всё ещё падает с кодом %d — фикс ещё не готов\n",
							cleanOutcome.FailedIndex, cleanOutcome.Total, cleanOutcome.Failed.Section, cleanOutcome.ExitCode)
					}
				}
			}
		} else {
			// Тот же предохранитель от подвисшего процесса, что у вопроса
			// о токене (план 04-01): в пайпе и в CI спрашивать некого.
			fmt.Fprintln(os.Stderr, "чистый прогон не предлагается вне терминала")
		}
	}

	pr.Stage("убираю контейнер и временный чекаут…")

	return nil
}

// confirm печатает question в поток ошибок с пометкой, что пустой ответ
// значит согласие, читает одну строку стандартного ввода, обрезает её по
// краям пробельными символами и признаёт согласием пустой ответ и ответы,
// начинающиеся с «д» или латинской «y»; всё прочее — отказ. Ошибка чтения —
// тоже отказ: молча писать или запускать что-то на неполученном ответе
// нельзя. Единственный подтверждающий диалог этого плана.
func confirm(question string) bool {
	fmt.Fprintf(os.Stderr, "%s (пустой ответ — да): ", question)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer == "" {
		return true
	}
	return strings.HasPrefix(answer, "д") || strings.HasPrefix(answer, "y")
}

// runShell реализует единственную подкоманду: разбор ссылки → резолв
// токена → аутентифицированный запрос к GitLab API за интерфейсом
// provider.Provider → печать метаданных джобы.
func runShell(args []string) {
	fs := flag.NewFlagSet("shell", flag.ExitOnError)
	imageFlag := fs.String("image", "", "образ контейнера вместо образа из конфига джобы")
	hereFlag := fs.Bool("here", false, "положить код джобы в текущий каталог и оставить его там после выхода")
	fs.Parse(args)

	if fs.NArg() < 1 {
		printUsage()
		os.Exit(2)
	}
	raw := fs.Arg(0)

	// Контекст создаётся здесь, а не только перед первым запросом к API:
	// разбор номера джобы уже нуждается в нём для вызовов git.
	ctx := context.Background()

	var ref joburl.Ref
	if jobID, isNumber := joburl.JobNumber(raw); isNumber {
		root, err := repo.Root(ctx)
		if err != nil {
			fail(err)
		}
		host, projectPath, err := repo.OriginRef(ctx, root)
		if err != nil {
			fail(err)
		}
		// Хост в этой ветке приходит не от пользователя, а из клонированного
		// репозитория — пользователь обязан увидеть, куда полетит его
		// токен, прежде чем токен туда полетит (T-04-03). Печатается ДО
		// резолва токена.
		fmt.Fprintf(os.Stderr, "хост и проект определены из git remote origin: %s, %s\n", host, projectPath)
		ref = joburl.FromParts(host, projectPath, jobID)
	} else {
		r, err := joburl.Parse(raw)
		if err != nil {
			fail(err)
		}
		ref = r
	}

	tok, err := token.Resolve(ref.Host)
	if err != nil {
		// Нет токена — это не ошибка с инструкцией, а вопрос, на который
		// может ответить только человек за терминалом. В неинтерактивном
		// запуске (пайп, CI) поведение остаётся прежним: explain(err) ниже
		// печатает исходную подсказку про переменные окружения и конфиг.
		if errors.Is(err, token.ErrNoToken) && token.Interactive() {
			tok, err = token.Prompt(ref.Host)
		}
		if err != nil {
			fail(err)
		}
	}

	// Конкретный клиент создаётся один раз и присваивается переменной
	// интерфейсного типа — весь дальнейший доступ к GitLab идёт через
	// provider.Provider (GLAB-04).
	var p provider.Provider = gitlab.New(ref.Host, tok)

	job, err := p.JobByID(ctx, ref.ProjectPath, ref.JobID)
	if err != nil {
		fail(err)
	}

	fmt.Printf("распознано: хост %s, проект %s, job id %d\n", ref.Host, ref.ProjectPath, ref.JobID)
	fmt.Printf("токен: %s\n", tok.String())

	// Не фатально: утилита восстанавливает окружение упавших джоб, но
	// метаданные любой джобы всё равно полезны — предупреждаем и работаем
	// дальше с кодом выхода 0 (GLAB-01).
	if job.Status != "failed" {
		fmt.Fprintf(os.Stderr, "предупреждение: джоба в статусе %s, а восстанавливать окружение имеет смысл для упавших\n", job.Status)
	}

	// Конфиг пайплайна не критичен для метаданных джобы: если его нет, он
	// использует include/extends/!reference или просто не разобрался,
	// печатаем то, что уже есть, и предупреждаем в stderr, а не прерываем
	// работу. Job metadata is already fetched at this point, so no
	// config-side error justifies exit 1 (WR-04).
	jobCfg := provider.JobConfig{}
	if job.CommitSHA == "" {
		// The API can omit the commit (trigger/bridge scenarios); requesting
		// the config with ref="" would only produce an opaque 400 (WR-05).
		fmt.Fprintln(os.Stderr, "предупреждение: у джобы нет коммита — конфиг пайплайна не запрашивается")
	} else {
		cfg, err := p.MergedConfig(ctx, ref.ProjectPath, job.CommitSHA)
		switch {
		case err == nil:
			if jc, ok := cfg.JobByName(job.Name); ok {
				jobCfg = jc
			} else if jerr := cfg.JobError(job.Name); jerr != nil {
				fmt.Fprintf(os.Stderr, "предупреждение: %s\n", explain(jerr))
			} else {
				fmt.Fprintf(os.Stderr, "предупреждение: джоба %q не найдена в конфиге пайплайна\n", job.Name)
			}
		case errors.Is(err, provider.ErrConfigNotFound), errors.Is(err, provider.ErrUnresolvedRefs):
			fmt.Fprintf(os.Stderr, "предупреждение: %s\n", explain(err))
		default:
			fmt.Fprintf(os.Stderr, "предупреждение: конфиг пайплайна не разобран: %s\n", explain(err))
		}
	}

	// Единственное место в cmd/ci, где образ из разобранного конфига вообще
	// читается; дальше по коду ходит только результат выбора (img.Ref) —
	// джоба без image: в конфиге больше не отказ, а разумный дефолт (CLI-07).
	img := runner.ResolveImage(jobCfg.Image, *imageFlag)

	if err := render.Job(os.Stdout, job, jobCfg); err != nil {
		fail(err)
	}

	// Переменные проекта и групп не критичны: предопределённые CI_*
	// полезны и без них. Отказ (нет доступа, устаревший токен) печатается
	// предупреждением в stderr, и сборка окружения продолжается с пустым
	// набором API-переменных (GLAB-02).
	var varSet provider.VariableSet
	if vs, err := p.Variables(ctx, job); err != nil {
		fmt.Fprintf(os.Stderr, "предупреждение: переменные проекта и групп не получены: %s\n", explain(err))
	} else {
		varSet = vs
	}

	// Файл секретов пользователя закрывает маскированные переменные,
	// которые GitLab не отдаёт по API никогда (ENV-02). Ошибка чтения не
	// фатальна: небезопасные права (env.ErrInsecureSecrets) означают, что
	// содержимое вообще не было прочитано, поэтому риск закрыт — печатаем
	// предупреждение с командой chmod и продолжаем с пустым набором
	// секретов, чтобы окружение всё равно собралось.
	secrets, err := env.LoadSecrets(ref.Host, ref.ProjectPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "предупреждение: файл секретов не прочитан: %s\n", explain(err))
	}

	e := env.Assemble(env.Input{
		Job:         job,
		Host:        ref.Host,
		JobConfig:   jobCfg,
		APIVars:     varSet.Variables,
		Notices:     varSet.Notes,
		Secrets:     secrets.Values,
		SecretsPath: secrets.Path,
	})

	// Первый запуск с недостающими значениями сам создаёт файл секретов,
	// уже заполненный именами недостающих ключей (ENV-06) — человеку
	// остаётся вписать значения, а не сочинять формат. Ошибка не фатальна:
	// missingReport (render.Env ниже) всё равно печатает готовый к вставке
	// фрагмент, сборка окружения и воспроизведение продолжаются.
	if len(e.Missing) > 0 {
		if path, created, err := env.EnsureSecretsFile(ref.Host, ref.ProjectPath, e.Missing); err != nil {
			fmt.Fprintf(os.Stderr, "предупреждение: файл секретов не создан: %s\n", explain(err))
		} else if created {
			fmt.Fprintf(os.Stderr, "файл секретов создан: %s; в нём уже перечислены недостающие переменные, вписать значения — ci secrets\n", path)
		}
	}

	if err := render.Env(os.Stdout, e); err != nil {
		fail(err)
	}

	// Место чекаута решается один раз здесь, до всех дорогих операций
	// (загрузки образа, fetch кода): неудачу выбора места пользователь
	// должен получить до них, а не посреди прогона.
	base, persist, err := checkoutBase(*hereFlag)
	if err != nil {
		fail(err)
	}

	// Прогресс дальнейших этапов воспроизведения идёт в stderr, чтобы
	// stdout оставался чистым выводом метаданных и окружения.
	pr := render.Progress{W: os.Stderr}
	if err := reproduce(ctx, ref, job, jobCfg, img, e, tok, base, persist, pr); err != nil {
		fail(err)
	}
}

// runSecrets реализует подкоманду ci secrets: создаёт файл секретов, если
// его ещё нет (env.EnsureSecretsFile без проекта — здесь ещё неизвестно, о
// каком проекте речь, поэтому в файл ложится только закомментированный
// пример формата), и открывает его в редакторе пользователя.
//
// Свой FlagSet без флагов — форма разбора аргументов совпадает с подкомандой
// shell, и лишний аргумент не проходит молча.
//
// Запуск редактора защищён проверкой терминала (token.Interactive()), как
// вопрос о токене (план 04-01): без неё процесс подвис бы в пайпе или в CI.
// Редактор берётся из VISUAL/EDITOR (иначе vi), бинарь ищется в PATH через
// exec.LookPath, команда собирается срезом аргументов без оболочки — в
// аргументы уходит только путь файла, содержимое секретов в аргументах
// процесса не появляется никогда. После выхода редактора права
// принудительно возвращаются к 0600: многие редакторы сохраняют файл через
// временный с последующим переименованием, и права нового файла определяет
// маска процесса.
func runSecrets(args []string) {
	fs := flag.NewFlagSet("secrets", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() > 0 {
		printUsage()
		os.Exit(2)
	}

	path, created, err := env.EnsureSecretsFile("", "", nil)
	if err != nil {
		fail(err)
	}
	if created {
		fmt.Fprintf(os.Stderr, "файл секретов создан: %s\n", path)
	}

	if !token.Interactive() {
		fmt.Fprintf(os.Stderr, "нет терминала — откройте файл сами: %s\n", path)
		return
	}

	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}

	// VISUAL или EDITOR из одних пробельных символов (легко приезжает из
	// подключённого профиля шелла) непусты, поэтому фолбэк на vi выше их не
	// перехватывает, а strings.Fields возвращает по ним пустой срез — без
	// этой проверки fields[0] роняет процесс паникой с трассировкой стека
	// вместо понятного сообщения.
	fields := strings.Fields(editor)
	if len(fields) == 0 {
		fields = []string{"vi"}
	}
	bin, err := exec.LookPath(fields[0])
	if err != nil {
		fail(fmt.Errorf("редактор %q не найден в PATH: %w", fields[0], err))
	}
	cmdArgs := append(append([]string{}, fields[1:]...), path)

	cmd := exec.Command(bin, cmdArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "предупреждение: редактор завершился с ошибкой: %s\n", err)
	}

	if err := os.Chmod(path, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "предупреждение: не удалось вернуть права 0600 файлу секретов %s: %s\n", path, err)
	} else {
		fmt.Fprintf(os.Stderr, "права файла секретов возвращены к 0600: %s\n", path)
	}
}

// runApply реализует подкоманду ci apply: находит сохранённые правки,
// печатает джобу и список изменённых файлов с числом строк, спрашивает
// ровно один раз и накладывает патч трёхсторонним мерджем в рабочее
// дерево — ничего не коммитя. Функция владеет только чтением и одним
// наложением, отложенной уборки у неё нет, поэтому завершать процесс ей
// можно — в отличие от reproduce.
func runApply(args []string) {
	fs := flag.NewFlagSet("apply", flag.ExitOnError)
	jobFlag := fs.Int64("job", 0, "идентификатор джобы, чьи правки переносить (по умолчанию — последняя сохранённая)")
	forceFlag := fs.Bool("force", false, "накладывать, даже если коммита джобы нет в этом репозитории")
	fs.Parse(args)
	if fs.NArg() > 0 {
		printUsage()
		os.Exit(2)
	}

	ctx := context.Background()

	root, err := repo.Root(ctx)
	if err != nil {
		fail(err)
	}

	// Патч выбирается только среди патчей ТОГО проекта, в репозитории
	// которого стоит пользователь: каталог патчей общий на машину, и без
	// хоста с проектом «последний сохранённый патч» означал бы последний
	// сохранённый на машине вообще, а --job <id> — совпадение по номеру,
	// уникальному только внутри одного инстанса GitLab. Ошибка разбора
	// origin здесь фатальна намеренно: не зная проекта, выбирать не из чего.
	host, projectPath, err := repo.OriginRef(ctx, root)
	if err != nil {
		fail(err)
	}

	var patch repo.Patch
	if *jobFlag != 0 {
		patch, err = repo.PatchFor(host, projectPath, *jobFlag)
	} else {
		patch, err = repo.LatestPatch(host, projectPath)
	}
	if err != nil {
		fail(err)
	}

	// Отчёт снимается до любого письма и до подтверждающего вопроса:
	// PatchStat только читает, CheckPaths должна отвергнуть опасный патч
	// раньше, чем он дойдёт даже до предложения пользователю.
	stats, err := repo.PatchStat(ctx, root, patch.Path)
	if err != nil {
		fail(err)
	}
	if err := repo.CheckPaths(stats); err != nil {
		fail(err)
	}

	var added, deleted int
	for _, s := range stats {
		if s.Added >= 0 {
			added += s.Added
		}
		if s.Deleted >= 0 {
			deleted += s.Deleted
		}
	}
	fmt.Fprintf(os.Stderr, "джоба #%d (%s): %d файлов изменено (+%d -%d)\n\n", patch.JobID, patch.SHA, len(stats), added, deleted)
	for _, s := range stats {
		addedStr, deletedStr := "-", "-"
		if s.Added >= 0 {
			addedStr = fmt.Sprintf("+%d", s.Added)
		}
		if s.Deleted >= 0 {
			deletedStr = fmt.Sprintf("-%d", s.Deleted)
		}
		// Переименование печатается обеими сторонами: пользователь должен
		// видеть ровно те пути, которые проверены CheckPaths и будут
		// записаны, а не компактную форму git с фигурными скобками.
		name := s.Path
		if s.From != "" {
			name = s.From + " → " + s.Path
		}
		fmt.Fprintf(os.Stderr, "  %s   %s %s\n", name, addedStr, deletedStr)
	}
	fmt.Fprintln(os.Stderr)

	// Отсутствие коммита джобы в этом репозитории — отказ, а не
	// предупреждение: базы для трёхстороннего мерджа нет, и это единственный
	// оставшийся признак того, что патч приехал не отсюда (клон другого
	// проекта с тем же origin, ещё не подтянутая история). Продавить решение
	// можно флагом --force — но осознанно.
	if ahead, ok := repo.AheadCount(ctx, root, patch.SHA); ok {
		if ahead > 0 {
			fmt.Fprintf(os.Stderr, "твой HEAD ушёл на %d коммитов вперёд коммита джобы — накладываю трёхсторонним мерджем\n", ahead)
		}
	} else if *forceFlag {
		fmt.Fprintln(os.Stderr, "предупреждение: коммита джобы нет в этом репозитории, но задан --force — базы для мерджа может не найтись; при отказе патч останется файлом")
	} else {
		fmt.Fprintf(os.Stderr, "коммита джобы %s нет в этом репозитории — базы для трёхстороннего мерджа нет\n"+
			"подтяните историю (git fetch) или, если уверены, повторите с --force; патч остался файлом: %s\n", patch.SHA, patch.Path)
		os.Exit(1)
	}

	// Тот же предохранитель от подвисшего процесса, что у вопроса о токене
	// и у вопроса о чистом прогоне: в чужой код без ответа человека не
	// пишем вовсе.
	if !token.Interactive() {
		fmt.Fprintf(os.Stderr, "нет терминала — наложите вручную: git -C %s apply --3way %s\n", root, patch.Path)
		os.Exit(2)
	}

	if !confirm(fmt.Sprintf("наложить правки трёхсторонним мерджем в %s?", root)) {
		fmt.Fprintf(os.Stderr, "не применено, патч остался файлом: %s\n", patch.Path)
		return
	}

	if err := repo.Apply(ctx, root, patch.Path); err != nil {
		fail(err)
	}

	fmt.Fprintln(os.Stderr, "✓ применено в рабочее дерево, не закоммичено — смотри git diff")
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "ошибка: %s\n", explain(err))
	os.Exit(1)
}
