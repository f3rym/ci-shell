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

	// Конфиг пайплайна не критичен для метаданных джобы: если его нет или он
	// использует include/extends/!reference, печатаем то, что уже есть, и
	// предупреждаем в stderr, а не прерываем работу.
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
		fmt.Fprintf(os.Stderr, "предупреждение: %s\n", err)
	default:
		fail(err)
	}

	if err := render.Job(os.Stdout, job, jobCfg); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "ошибка: %s\n", err)
	os.Exit(1)
}
