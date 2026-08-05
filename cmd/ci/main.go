// Command ci — точка входа CLI-утилиты ci-shell.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/f3rym/ci-shell/internal/joburl"
	"github.com/f3rym/ci-shell/internal/provider"
	"github.com/f3rym/ci-shell/internal/provider/gitlab"
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
	printJob(job)
}

func printJob(job provider.Job) {
	fmt.Printf("джоба %s #%d\n", job.Name, job.ID)
	fmt.Printf("  статус:  %s\n", job.Status)
	fmt.Printf("  причина: %s\n", orDash(job.FailureReason))
	fmt.Printf("  стадия:  %s\n", job.Stage)
	fmt.Printf("  ref:     %s\n", job.Ref)
	fmt.Printf("  коммит:  %s (%s)\n", shortSHA(job.CommitSHA), job.CommitTitle)
	fmt.Printf("  ранер:   %s\n", orDash(job.RunnerDesc))
	fmt.Printf("  ссылка:  %s\n", job.WebURL)
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "ошибка: %s\n", err)
	os.Exit(1)
}
