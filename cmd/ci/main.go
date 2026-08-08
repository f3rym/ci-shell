// Command ci — точка входа CLI-утилиты ci-shell.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
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

	// d.EnsureImage остаётся здесь, после подготовки кода: это уже дорогая
	// операция с собственным прогрессом, а не дешёвая проверка Preflight.
	if err := d.EnsureImage(ctx, img.Ref); err != nil {
		return err
	}

	envMap := e.Map()
	workDir := envMap["CI_PROJECT_DIR"]
	if workDir == "" {
		// Правило GitLab по умолчанию: /builds/<путь проекта> — шелл всё
		// равно открывается там, где работала джоба.
		workDir = "/builds/" + job.ProjectPath
	}

	if len(e.Missing) > 0 {
		keys := make([]string, 0, len(e.Missing))
		for _, m := range e.Missing {
			keys = append(keys, m.Key)
		}
		fmt.Fprintf(os.Stderr, "предупреждение: без значений маскированных переменных (%s, файл %s) может воспроизвестись не тот сбой\n", strings.Join(keys, ", "), e.SecretsPath)
	}

	c, skipped, err := d.Start(ctx, runner.Spec{
		Image:     img.Ref,
		SourceDir: code.Dir,
		WorkDir:   workDir,
		Env:       envMap,
		JobID:     job.ID,
	})
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
	uid, gid := os.Getuid(), os.Getgid()
	if uid >= 0 && gid >= 0 {
		defer func() {
			chownCtx := context.Background()
			chownCmd := fmt.Sprintf("chown -R %d:%d %s", uid, gid, workDir)
			// Ошибка и ненулевой код не фатальны и не печатаются как ошибка
			// — в образе может не быть chown; на этот случай в
			// Checkout.Remove уже есть готовая команда добивки. Вывод
			// chown пользователю не нужен.
			_, _ = c.Exec(chownCtx, chownCmd, io.Discard, io.Discard)
		}()
	}

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
	if len(steps) == 0 {
		fmt.Fprintln(os.Stderr, "предупреждение: в конфиге джобы нет шагов — возможно, всё делает entrypoint образа")
	} else {
		outcome, err := runner.RunSteps(ctx, c, steps, os.Stdout, os.Stderr, pr)
		if err != nil {
			return err
		}
		if outcome.Failed != nil {
			fmt.Fprintf(os.Stderr, "шаг %d/%d (%s) упал с кодом %d: %s\nшелл открывается сразу после последнего успешного шага\n",
				outcome.FailedIndex, outcome.Total, outcome.Failed.Section, outcome.ExitCode, outcome.Failed.Command)
		} else {
			fmt.Fprintf(os.Stderr, "все %d шагов прошли — сбой не воспроизвёлся. Вероятные причины: недостающие маскированные значения (см. предупреждение выше), артефакты предыдущих стадий и кэш ранера, которые v0.1.0 не восстанавливает\n", outcome.Total)
		}
	}

	// Ключи файловых переменных: значения в контейнер уходят содержимым, а
	// не путём к файлу (WR-05, материализация — Фаза 6) — честная строка
	// баннера, а не молчаливое ограничение.
	var fileVars []string
	for _, v := range e.Vars {
		if v.Kind == env.KindFile {
			fileVars = append(fileVars, v.Key)
		}
	}

	banner := render.BannerInput{
		ImageRef:       img.Ref,
		ImageAssumed:   img.Source == runner.ImageSourceDefault,
		Missing:        e.Missing,
		SecretsPath:    e.SecretsPath,
		FileVars:       fileVars,
		CodeSource:     fmt.Sprintf("%s — %s", code.Source, code.Dir),
		GitDirExternal: !code.SelfContained,
	}
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
	pr.Stage("убираю контейнер и временный чекаут…")

	return nil
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

func fail(err error) {
	fmt.Fprintf(os.Stderr, "ошибка: %s\n", explain(err))
	os.Exit(1)
}
