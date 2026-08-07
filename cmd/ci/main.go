// Command ci — точка входа CLI-утилиты ci-shell.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/f3rym/ci-shell/internal/env"
	"github.com/f3rym/ci-shell/internal/joburl"
	"github.com/f3rym/ci-shell/internal/provider"
	"github.com/f3rym/ci-shell/internal/provider/gitlab"
	"github.com/f3rym/ci-shell/internal/render"
	"github.com/f3rym/ci-shell/internal/repo"
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
	fmt.Fprintln(os.Stderr, "использование: ci shell <ссылка на джобу GitLab>")
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
		return fmt.Sprintf("%s\nзапустите ci shell из рабочей копии того же проекта, что и упавшая джоба", err)
	case errors.Is(err, repo.ErrCommitNotFound):
		return fmt.Sprintf("%s\nподтяните коммит: git fetch origin <sha> — иначе воспроизводить нечего", err)
	case errors.Is(err, repo.ErrWorktreeFailed):
		return fmt.Sprintf("%s\nснимите зависший worktree: git worktree prune", err)
	default:
		return err.Error()
	}
}

// reproduce приводит рабочую копию к коммиту упавшей джобы во временном
// worktree. Функция владеет очисткой через defer и никогда не завершает
// процесс — ни через fail, ни прямым выходом: иначе отложенная очистка не
// выполнится, и на машине останутся worktree и (в следующей задаче)
// контейнер. Любая поломка возвращается наружу как ошибка; печать и
// завершение делает runShell уже после возврата из reproduce.
func reproduce(ctx context.Context, ref joburl.Ref, job provider.Job, jobCfg provider.JobConfig, e env.Environment, pr render.Progress) error {
	root, err := repo.Root(ctx)
	if err != nil {
		return err
	}

	if remote, ok := repo.RemoteMatches(ctx, root, ref.ProjectPath); !ok {
		fmt.Fprintf(os.Stderr, "предупреждение: remote origin (%s) не похож на проект джобы (%s) — рабочая копия может быть не той\n", remote, ref.ProjectPath)
	}

	wt, err := repo.Prepare(ctx, root, job.CommitSHA, pr)
	if err != nil {
		return err
	}
	// Фоновый контекст: отмена основного контекста не должна помешать
	// уборке временного worktree.
	defer wt.Remove(context.Background())

	return nil
}

// runShell реализует единственную подкоманду: разбор ссылки → резолв
// токена → аутентифицированный запрос к GitLab API за интерфейсом
// provider.Provider → печать метаданных джобы.
func runShell(args []string) {
	fs := flag.NewFlagSet("shell", flag.ExitOnError)
	fs.Parse(args)

	if fs.NArg() < 1 {
		printUsage()
		os.Exit(2)
	}
	raw := fs.Arg(0)

	ref, err := joburl.Parse(raw)
	if err != nil {
		fail(err)
	}

	tok, err := token.Resolve(ref.Host)
	if err != nil {
		fail(err)
	}

	// Конкретный клиент создаётся один раз и присваивается переменной
	// интерфейсного типа — весь дальнейший доступ к GitLab идёт через
	// provider.Provider (GLAB-04).
	var p provider.Provider = gitlab.New(ref.Host, tok)

	ctx := context.Background()
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
			} else {
				fmt.Fprintf(os.Stderr, "предупреждение: джоба %q не найдена в конфиге пайплайна\n", job.Name)
			}
		case errors.Is(err, provider.ErrConfigNotFound), errors.Is(err, provider.ErrUnresolvedRefs):
			fmt.Fprintf(os.Stderr, "предупреждение: %s\n", explain(err))
		default:
			fmt.Fprintf(os.Stderr, "предупреждение: конфиг пайплайна не разобран: %s\n", explain(err))
		}
	}

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

	// Прогресс дальнейших этапов воспроизведения идёт в stderr, чтобы
	// stdout оставался чистым выводом метаданных и окружения.
	pr := render.Progress{W: os.Stderr}
	if err := reproduce(ctx, ref, job, jobCfg, e, pr); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "ошибка: %s\n", explain(err))
	os.Exit(1)
}
