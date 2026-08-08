package repo

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"

	"github.com/f3rym/ci-shell/internal/cache"
	"github.com/f3rym/ci-shell/internal/render"
)

// Sentinel-ошибки зеркала — в стиле остального пакета: диагностический
// контекст несёт текст, обёрнутый через fmt.Errorf("...: %w", ErrX).
var (
	// ErrUnsafeProject — путь проекта не годится для имени каталога.
	ErrUnsafeProject = errors.New("путь проекта не годится для имени каталога")
	// ErrMirrorFailed — не удалось подготовить зеркало репозитория.
	ErrMirrorFailed = errors.New("не удалось подготовить зеркало репозитория")
	// ErrFetchFailed — не удалось скачать коммит джобы.
	ErrFetchFailed = errors.New("не удалось скачать коммит джобы")
)

// sanitizeSegment приводит один сегмент пути к безопасному имени каталога:
// разрешены латинские буквы, цифры, точка, дефис и подчёркивание — любой
// другой символ (включая двоеточие порта в имени хоста, иначе путь невалиден
// на Windows) заменяется подчёркиванием. Пустой сегмент и сегмент, состоящий
// только из точек (например ".." — классический выход за пределы каталога),
// возвращают ErrUnsafeProject: именно на них строится обход кэша.
func sanitizeSegment(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("repo: пустой сегмент пути проекта: %w", ErrUnsafeProject)
	}
	if strings.Trim(s, ".") == "" {
		return "", fmt.Errorf("repo: сегмент пути %q состоит только из точек: %w", s, ErrUnsafeProject)
	}

	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String(), nil
}

// mirrorDir возвращает путь зеркала: cache.Dir("repos", <безопасный хост>,
// <безопасные сегменты пути проекта>) с суффиксом ".git" у последнего
// сегмента. Форма фиксирована требованием REPO-01 и idea-0.2.0 §4. Пустой
// хост или пустой путь проекта — ErrUnsafeProject.
func mirrorDir(host, projectPath string) (string, error) {
	if host == "" || projectPath == "" {
		return "", fmt.Errorf("repo: пустой хост или путь проекта: %w", ErrUnsafeProject)
	}

	safeHost, err := sanitizeSegment(host)
	if err != nil {
		return "", err
	}

	segments := strings.Split(projectPath, "/")
	safeSegments := make([]string, 0, len(segments))
	for _, seg := range segments {
		safeSeg, err := sanitizeSegment(seg)
		if err != nil {
			return "", err
		}
		safeSegments = append(safeSegments, safeSeg)
	}
	// Один аргумент вместо переменного числа: filepath.Join внутри cache.Dir
	// нормализует "/" внутри сегмента точно так же, как отдельные элементы —
	// суффикс ".git" дописывается к последнему сегменту дописыванием в конец
	// уже собранной строки.
	safeProjectPath := strings.Join(safeSegments, "/") + ".git"

	return cache.Dir("repos", safeHost, safeProjectPath)
}

// mirrorURL возвращает адрес источника: https-схема, хост и путь проекта с
// суффиксом ".git". Адрес не несёт учётных данных ни в каком виде — весь
// секрет живёт только в окружении дочернего процесса (см. auth.go).
func mirrorURL(host, projectPath string) string {
	u := url.URL{
		Scheme: "https",
		Host:   host,
		Path:   "/" + projectPath + ".git",
	}
	return u.String()
}

// hasCommit сообщает, есть ли в каталоге dir объект коммита sha. Ошибка
// команды означает «коммита нет», а не поломку зеркала — это решение о
// ветке для Materialize и fetchCommit, а не диагноз.
func hasCommit(ctx context.Context, dir, sha string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "-e", sha+"^{commit}")
	_, err := runCmd(cmd)
	return err == nil
}

// ensureMirror гарантирует, что в dir есть служебный каталог git: если он
// уже там (проверяется запросом каталога git у самого git), не делает
// ничего; иначе создаёт bare-репозиторий. Remote в зеркале не заводится
// вовсе — адрес источника всегда передаётся вызовом fetch явным аргументом,
// поэтому в конфигурации зеркала нечего чистить после загрузки.
func ensureMirror(ctx context.Context, dir string) error {
	checkCmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--git-dir")
	if _, err := runCmd(checkCmd); err == nil {
		return nil
	}

	initCmd := exec.CommandContext(ctx, "git", "init", "--bare", "--quiet", dir)
	if _, err := runCmd(initCmd); err != nil {
		return fmt.Errorf("repo: не удалось создать зеркало %s: %s: %w", dir, firstLine(err.Error()), ErrMirrorFailed)
	}
	return nil
}

// branchRefspec строит refspec запасного пути fetchCommit: ветка или тег
// целиком в собственное пространство имён зеркала refs/ci-shell/, когда
// сервер не отдаёт произвольный sha напрямую.
func branchRefspec(ref string, tag bool) string {
	if tag {
		return "refs/tags/" + ref + ":refs/ci-shell/tags/" + ref
	}
	return "refs/heads/" + ref + ":refs/ci-shell/heads/" + ref
}

// fetchCommit достаёт коммит sha в bare-зеркало dir. Секрет сюда не
// передаётся: функция получает уже собранное окружение непрозрачным срезом
// env, поэтому в этом файле значение токена не появляется ни в одной
// переменной.
//
// Если коммит уже есть в зеркале — возврат без единого сетевого вызова: это
// и есть кэш из idea §4, второй запуск по тому же проекту не тянет ничего
// заново. Иначе — fetch на сам sha; если сервер такой fetch не поддерживает
// и ref непуст, вторая попытка тянет ветку/тег целиком в
// refs/ci-shell/. Сразу после успеха объект закрепляется собственной
// ссылкой refs/ci-shell/commits/<sha> — без неё скачанный объект остаётся
// недостижимым и уедет при первой уборке мусора.
func fetchCommit(ctx context.Context, dir, host, projectPath, sha, ref string, tag bool, env []string, p render.Progress) error {
	if hasCommit(ctx, dir, sha) {
		return nil
	}

	p.Stage("тяну коммит %s с %s…", shortSHA(sha), host)

	source := mirrorURL(host, projectPath)

	var lastErr error
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "fetch", "--no-tags", "--depth=1", source, sha)
	cmd.Env = env
	if _, err := runCmd(cmd); err != nil {
		lastErr = err

		// Запасной путь: сервер не отдаёт произвольный sha по адресу —
		// тянем ветку/тег целиком в своё пространство имён и надеемся, что
		// нужный коммит окажется в её истории.
		if ref != "" {
			refspec := branchRefspec(ref, tag)
			fallback := exec.CommandContext(ctx, "git", "-C", dir, "fetch", "--no-tags", "--depth=50", source, refspec)
			fallback.Env = env
			if _, ferr := runCmd(fallback); ferr != nil {
				lastErr = ferr
			}
		}
	}

	if !hasCommit(ctx, dir, sha) {
		if lastErr == nil {
			lastErr = errors.New("коммит не появился в зеркале после fetch")
		}
		return fmt.Errorf("repo: не удалось скачать коммит %s с %s: %s: %w", shortSHA(sha), host, firstLine(lastErr.Error()), ErrFetchFailed)
	}

	refCmd := exec.CommandContext(ctx, "git", "-C", dir, "update-ref", "refs/ci-shell/commits/"+sha, sha)
	if _, err := runCmd(refCmd); err != nil {
		return fmt.Errorf("repo: не удалось закрепить коммит %s ссылкой: %s: %w", shortSHA(sha), firstLine(err.Error()), ErrFetchFailed)
	}

	p.Stage("коммит %s скачан в зеркало", shortSHA(sha))

	return nil
}
