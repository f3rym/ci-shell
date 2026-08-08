// Package repo приводит рабочую копию к коммиту джобы отдельным worktree,
// не трогая текущую ветку и незакоммиченные правки пользователя.
package repo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/f3rym/ci-shell/internal/render"
)

// Sentinel-ошибки пакета — каждая несёт диагностический контекст в тексте
// через fmt.Errorf("...: %w", ErrX), по образцу Фазы 1.
var (
	// ErrGitUnavailable — git не найден в PATH.
	ErrGitUnavailable = errors.New("git не найден в PATH")
	// ErrNotAGitRepo — команда запущена не из рабочей копии git.
	ErrNotAGitRepo = errors.New("текущий каталог не является рабочей копией git")
	// ErrCommitNotFound — у джобы нет коммита, воспроизводить нечего: либо
	// sha в метаданных пуст, либо коммит не нашёлся ни локально, ни в
	// зеркале. С Фазы 5 это штатная ветка Materialize, а не отказ
	// пользователю с советом сходить в git руками.
	ErrCommitNotFound = errors.New("коммит джобы не найден локально")
	// ErrWorktreeFailed — сам git worktree add отказал.
	ErrWorktreeFailed = errors.New("не удалось создать worktree")
)

// Root ищет git в PATH и возвращает абсолютный путь корня рабочей копии,
// из которой запущена утилита.
func Root(ctx context.Context) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("repo: git не найден в PATH: %w", ErrGitUnavailable)
	}

	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	out, err := runCmd(cmd)
	if err != nil {
		return "", fmt.Errorf("repo: текущий каталог не является рабочей копией git: %w", ErrNotAGitRepo)
	}
	return strings.TrimSpace(out), nil
}

// RemoteMatches проверяет, указывает ли remote origin рабочей копии root на
// тот же проект: хост wantHost и путь проекта projectPath сравниваются
// целиком и без учёта регистра, а не поиском подстроки.
//
// Подстрока здесь была прямой ошибкой выбора репозитория: "group/project"
// содержится и в "https://gitlab.com/other-group/project-fork", и в
// "https://evil.example/group/project" — то есть совпадал форк, совпадал
// чужой хост, а Materialize берёт этот ответ единственным основанием
// считать локальную рабочую копию тем же проектом и смонтировать в
// контейнер её содержимое, выдав его пользователю за код джобы. Разбор
// remote берётся у parseRemote — той же формулы, по которой утилита
// выводит хост и проект из origin для номера джобы.
//
// Несовпадение — не поломка, а штатный переход на сетевую ветку
// Materialize, поэтому неразобранный, пустой или недоступный origin даёт
// ложь: «не знаем, что это тот же проект» здесь честнее, чем «поверим на
// слово».
func RemoteMatches(ctx context.Context, root, wantHost, projectPath string) (string, bool) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "remote", "get-url", "origin")
	out, err := runCmd(cmd)
	if err != nil {
		return "", false
	}
	remote := strings.TrimSpace(out)
	if remote == "" {
		return "", false
	}

	host, path, ok := parseRemote(remote)
	if !ok {
		return remote, false
	}
	return remote, strings.EqualFold(host, wantHost) && strings.EqualFold(path, projectPath)
}

// Worktree — временный worktree, приведённый к коммиту джобы.
type Worktree struct {
	Dir string
	SHA string

	root string
}

// Prepare приводит рабочую копию root к коммиту sha в уже вычисленный путь
// dir и печатает этап через p. С Фазы 5 каталог чекаута выбирает не этот
// пакет — путь приходит извне (см. repo.Checkout), а в плане 05-02 он
// станет настраиваемым пользователем; git worktree add отказывается писать
// в существующий каталог, поэтому dir на входе не должен существовать.
func Prepare(ctx context.Context, root, sha, dir string, p render.Progress) (*Worktree, error) {
	if sha == "" {
		return nil, fmt.Errorf("repo: у джобы нет коммита: %w", ErrCommitNotFound)
	}

	// Проверка наличия коммита остаётся защитой инварианта: путь
	// пользователя до неё больше не доходит напрямую — ветку между
	// локальным worktree и сетевым зеркалом выбирает Materialize.
	if !hasCommit(ctx, root, sha) {
		return nil, fmt.Errorf("repo: коммит %s не найден локально: %w", sha, ErrCommitNotFound)
	}

	p.Stage("готовлю код на коммите %s…", shortSHA(sha))

	addCmd := exec.CommandContext(ctx, "git", "-C", root, "worktree", "add", "--detach", dir, sha)
	if _, err := runCmd(addCmd); err != nil {
		return nil, fmt.Errorf("repo: git worktree add отказал: %s: %w", firstLine(err.Error()), ErrWorktreeFailed)
	}

	p.Stage("код на коммите %s: %s", shortSHA(sha), dir)

	return &Worktree{Dir: dir, SHA: sha, root: root}, nil
}

// Remove снимает worktree средствами git. Удаление файлов с диска — забота
// Checkout.Remove: при флаге --here (план 05-02) каталог удалять нельзя
// вовсе, и решение об этом принимает владелец чекаута, а не worktree.
func (w *Worktree) Remove(ctx context.Context) error {
	removeCmd := exec.CommandContext(ctx, "git", "-C", w.root, "worktree", "remove", "--force", w.Dir)
	if _, err := runCmd(removeCmd); err != nil {
		return fmt.Errorf("repo: не удалось снять worktree %s: %s (добейте вручную: git worktree prune): %w",
			w.Dir, firstLine(err.Error()), ErrWorktreeFailed)
	}
	return nil
}

// runCmd запускает уже собранную команду cmd (аргументы — срезом, без
// оболочки, без конкатенации строк) и возвращает stdout. При ненулевом коде
// возврата — ошибку с первой строкой stderr.
func runCmd(cmd *exec.Cmd) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := firstLine(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), errors.New(msg)
	}
	return stdout.String(), nil
}

// shortSHA обрезает sha до восьми символов для печати в прогрессе.
func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// firstLine возвращает первую непустую строку s.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
