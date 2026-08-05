// Command ci — точка входа CLI-утилиты ci-shell.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/f3rym/ci-shell/internal/joburl"
	"github.com/f3rym/ci-shell/internal/provider"
	"github.com/f3rym/ci-shell/internal/provider/gitlab"
	"github.com/f3rym/ci-shell/internal/render"
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
	default:
		return err.Error()
	}
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

	if err := render.Job(os.Stdout, job, jobCfg); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "ошибка: %s\n", explain(err))
	os.Exit(1)
}
