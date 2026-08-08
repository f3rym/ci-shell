package repo

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"

	"github.com/f3rym/ci-shell/internal/joburl"
)

// Sentinel-ошибки OriginRef — в стиле остального пакета: диагностический
// контекст несёт текст, обёрнутый через fmt.Errorf("...: %w", ErrX).
var (
	// ErrNoOrigin — remote origin не настроен (нет вывода, пустой вывод или
	// git отказал целиком).
	ErrNoOrigin = errors.New("remote origin не настроен")
	// ErrRemoteUnparsable — remote origin есть, но ни одна из известных форм
	// записи (https, scp-подобная, ssh://) не подошла.
	ErrRemoteUnparsable = errors.New("из remote origin не вывести хост и проект")
)

// OriginRef запускает `git -C root remote get-url origin` и разбирает
// результат в хост и путь проекта. Функция ничего не печатает: вывод —
// забота cmd/ci, как и во всём пакете.
func OriginRef(ctx context.Context, root string) (host, projectPath string, err error) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "remote", "get-url", "origin")
	out, cmdErr := runCmd(cmd)
	if cmdErr != nil {
		return "", "", fmt.Errorf("repo: %s: %w", cmdErr, ErrNoOrigin)
	}
	remote := strings.TrimSpace(out)
	if remote == "" {
		return "", "", fmt.Errorf("repo: remote origin пуст: %w", ErrNoOrigin)
	}

	host, projectPath, ok := parseRemote(remote)
	if !ok {
		return "", "", fmt.Errorf("repo: из remote origin %q не вывести хост и проект: %w", remote, ErrRemoteUnparsable)
	}
	return host, projectPath, nil
}

// parseRemote разбирает строку remote origin в одной из трёх форм:
//   - https://<host>/<группа>/<проект>.git (и http:// на всякий случай)
//   - scp-подобная git@<host>:<группа>/<проект>.git
//   - ssh://git@<host>:<порт>/<группа>/<проект>.git
//
// Из хоста отбрасываются учётные данные до "@" и номер порта; из пути —
// ведущие и хвостовые "/" и суффикс ".git".
func parseRemote(remote string) (host, projectPath string, ok bool) {
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil || u.Hostname() == "" {
			return "", "", false
		}
		host = u.Hostname()
		projectPath = u.Path
	} else if at := strings.Index(remote, "@"); at >= 0 {
		// Форма git@<host>:<путь> — учётные данные ("git") уже отсечены
		// индексом at, дальше ищем разделитель хоста и пути.
		rest := remote[at+1:]
		sep := strings.Index(rest, ":")
		if sep < 0 {
			return "", "", false
		}
		host = rest[:sep]
		projectPath = rest[sep+1:]
	} else {
		return "", "", false
	}

	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}

	projectPath = strings.Trim(projectPath, "/")
	projectPath = strings.TrimSuffix(projectPath, ".git")
	projectPath = strings.Trim(projectPath, "/")

	// Путь проекта проверяется здесь, на границе, а не у каждого
	// потребителя: отсюда он уходит и в url.URL.Path адреса зеркала (а
	// url.URL.String сегменты ".." не убирает — "a/../../x" улетел бы в
	// запрос как есть), и в имя ссылки refs/ci-shell зеркала, и в origin
	// внутри контейнера. Форма проверки — общая с разбором ссылки на джобу,
	// чтобы номер джобы и полная ссылка давали одинаково валидный путь.
	if host == "" || !joburl.ValidProjectPath(projectPath) {
		return "", "", false
	}
	return host, projectPath, true
}
