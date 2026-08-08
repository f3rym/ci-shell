// Package repo приводит рабочую копию к коммиту джобы отдельным worktree,
// не трогая текущую ветку и незакоммиченные правки пользователя.
package repo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/f3rym/ci-shell/internal/cache"
	"github.com/f3rym/ci-shell/internal/render"
)

// Sentinel-ошибки пакета — каждая несёт диагностический контекст в тексте
// через fmt.Errorf("...: %w", ErrX), по образцу Фазы 1.
var (
	// ErrGitUnavailable — git не найден в PATH.
	ErrGitUnavailable = errors.New("git не найден в PATH")
	// ErrNotAGitRepo — команда запущена не из рабочей копии git.
	ErrNotAGitRepo = errors.New("текущий каталог не является рабочей копией git")
	// ErrCommitNotFound — коммита джобы нет локально.
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
// тот же проект projectPath. Сравнение — без учёта регистра, путь проекта
// ищется в строке remote после отсечения суффикса ".git". Если remote не
// настроен или команда отказала — молчим про то, чего не знаем, вместо
// ложной тревоги: возвращаем пустую строку и true.
func RemoteMatches(ctx context.Context, root, projectPath string) (string, bool) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "remote", "get-url", "origin")
	out, err := runCmd(cmd)
	if err != nil {
		return "", true
	}
	remote := strings.TrimSpace(out)
	if remote == "" {
		return "", true
	}

	trimmed := strings.TrimSuffix(remote, ".git")
	return remote, strings.Contains(strings.ToLower(trimmed), strings.ToLower(projectPath))
}

// Worktree — временный worktree, приведённый к коммиту джобы.
type Worktree struct {
	Dir string
	SHA string

	root   string
	parent string
}

// Prepare приводит рабочую копию root к коммиту sha в отдельном временном
// worktree и печатает этап через p. Ни на одной ветке неудачи временный
// каталог не остаётся на диске.
func Prepare(ctx context.Context, root, sha string, p render.Progress) (*Worktree, error) {
	if sha == "" {
		return nil, fmt.Errorf("repo: у джобы нет коммита: %w", ErrCommitNotFound)
	}

	// Проверка наличия коммита идёт до создания каталогов: пользователь
	// должен узнать о нехватке коммита сразу, а не после мусора на диске.
	catFileCmd := exec.CommandContext(ctx, "git", "-C", root, "cat-file", "-e", sha+"^{commit}")
	if _, err := runCmd(catFileCmd); err != nil {
		return nil, fmt.Errorf("repo: коммит %s не найден локально: %w", sha, ErrCommitNotFound)
	}

	p.Stage("готовлю код на коммите %s…", shortSHA(sha))

	// Постоянный кэш пользователя вместо системного временного каталога:
	// snap-сборка Docker его не видит, и монтирование каталога с таким
	// путём в контейнер отказывает (idea-0.2.0 §3). Случайное имя внутри
	// сохраняется — каталог общий между запусками, и два одновременных
	// ci shell не должны драться за один путь.
	cacheDir, err := cache.Dir("worktrees")
	if err != nil {
		return nil, err
	}
	parent, err := os.MkdirTemp(cacheDir, "job-*")
	if err != nil {
		return nil, fmt.Errorf("repo: не удалось создать временный каталог: %w", err)
	}
	// git worktree add отказывается писать в существующий каталог — сам
	// каталог src заранее не создаётся, только его приватный родитель.
	dir := filepath.Join(parent, "src")

	addCmd := exec.CommandContext(ctx, "git", "-C", root, "worktree", "add", "--detach", dir, sha)
	if _, err := runCmd(addCmd); err != nil {
		os.RemoveAll(parent)
		return nil, fmt.Errorf("repo: git worktree add отказал: %s: %w", firstLine(err.Error()), ErrWorktreeFailed)
	}

	p.Stage("код на коммите %s: %s", shortSHA(sha), dir)

	return &Worktree{Dir: dir, SHA: sha, root: root, parent: parent}, nil
}

// Remove снимает worktree и безусловно удаляет временный каталог — даже
// если git отказал, утилита не оставляет следов на машине пользователя.
// Ошибка os.RemoveAll не глотается наравне с ошибкой git: шаги джобы
// работают в контейнере под root и могут оставить в примонтированном
// каталоге файлы, которые непривилегированному пользователю не удалить
// (EACCES на Linux). Текст ошибки содержит фактический путь и команду
// добивки: git worktree prune снял бы только метаданные git, сами файлы
// им не удалить.
func (w *Worktree) Remove(ctx context.Context) error {
	removeCmd := exec.CommandContext(ctx, "git", "-C", w.root, "worktree", "remove", "--force", w.Dir)
	_, gitErr := runCmd(removeCmd)
	rmErr := os.RemoveAll(w.parent)
	if gitErr == nil && rmErr == nil {
		return nil
	}

	var cause string
	switch {
	case gitErr != nil && rmErr != nil:
		cause = fmt.Sprintf("git worktree remove: %s; удаление каталога: %s", gitErr, rmErr)
	case gitErr != nil:
		cause = fmt.Sprintf("git worktree remove: %s", gitErr)
	default:
		cause = fmt.Sprintf("удаление каталога: %s", rmErr)
	}
	return fmt.Errorf("repo: не удалось убрать временный worktree %s: %s\n"+
		"добейте вручную: sudo rm -rf %s && git worktree prune — файлы, записанные контейнером под root, обычному пользователю не удалить",
		w.Dir, cause, w.parent)
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
