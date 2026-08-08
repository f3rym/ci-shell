package repo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/f3rym/ci-shell/internal/render"
)

// ErrCheckoutExists — при --here (Persist) путь чекаута уже существует на
// диске. Молча писать поверх чужих файлов утилита не имеет права —
// единственный отказ, который вводит план 05-02.
var ErrCheckoutExists = errors.New("каталог для кода уже существует")

// Source описывает, откуда взят код чекаута — печатается пользователю как
// есть, тот же приём, что у env.Source и runner.ImageSource.
type Source string

const (
	// SourceLocal — код взят worktree из рабочей копии пользователя, без
	// единого сетевого вызова.
	SourceLocal Source = "локальный worktree рабочей копии"
	// SourceMirror — код взят мелким fetch в зеркало в кэше.
	SourceMirror Source = "мелкий fetch в зеркало кэша"
)

// Request — вход Materialize.
type Request struct {
	Host        string
	ProjectPath string
	SHA         string
	Ref         string
	Tag         bool
	// Base — каталог, внутри которого создаётся чекаут.
	Base string
	// Persist — истинно для --here (план 05-02): чекаут остаётся на диске
	// после Remove, путь в Base становится детерминированным вместо
	// случайного. Политику выбора Base и значения Persist знает только
	// cmd/ci — пакет repo получает готовое решение.
	Persist bool
	// Token — секрет; используется ровно в одном месте: при сборке
	// окружения сетевого fetch.
	Token string
}

// Checkout — выход Materialize: каталог, готовый к монтированию в контейнер.
type Checkout struct {
	Dir    string
	SHA    string
	Source Source
	// SelfContained — истинно, когда служебный каталог git чекаута лежит
	// внутри Dir (зеркальная ветка); ложно для worktree — его служебный
	// каталог git это файл-указатель на репозиторий пользователя вне
	// смонтированного каталога.
	SelfContained bool
	// Persist — скопировано из Request.Persist: при истинном значении
	// Remove ничего не удаляет (--here).
	Persist bool

	wt     *Worktree
	parent string
}

// checkoutPath — единственное место, где решается физическое расположение
// чекаута.
//
// Без Persist — временный родительский каталог со случайным именем и
// говорящим префиксом job- внутри base (два одновременных запуска не должны
// драться за один путь), сам код кладётся в его подкаталог src. Возвращаются
// оба пути: parent нужен уборке, dir — монтированию.
//
// С Persist путь детерминированный: подкаталог base с именем вида
// ci-shell-<последний сегмент пути проекта>-<короткий sha>, построенным из
// уже безопасного сегмента (тот же приём sanitizeSegment, что в mirror.go),
// чтобы путь проекта не притащил в имя каталога ничего лишнего; родительский
// каталог для уборки не заводится вовсе (возвращается пустым) — Remove с
// Persist ничего не удаляет. Если такой путь уже существует, возвращается
// ErrCheckoutExists независимо от того, пуст каталог или нет: так
// пользователь не потеряет ни своих файлов, ни результатов предыдущего
// запуска, а git не упрётся в непустой каталог при подготовке worktree.
func checkoutPath(base, projectPath, sha string, persist bool) (dir, parent string, err error) {
	if persist {
		segments := strings.Split(projectPath, "/")
		safeProject, err := sanitizeSegment(segments[len(segments)-1])
		if err != nil {
			return "", "", err
		}
		dir = filepath.Join(base, fmt.Sprintf("ci-shell-%s-%s", safeProject, shortSHA(sha)))
		if _, statErr := os.Stat(dir); statErr == nil {
			return "", "", fmt.Errorf("repo: каталог %s уже существует: %w", dir, ErrCheckoutExists)
		} else if !os.IsNotExist(statErr) {
			return "", "", fmt.Errorf("repo: не удалось проверить каталог %s: %w", dir, statErr)
		}
		return dir, "", nil
	}

	parent, err = os.MkdirTemp(base, "job-*")
	if err != nil {
		return "", "", fmt.Errorf("repo: не удалось создать временный каталог: %w", err)
	}
	return filepath.Join(parent, "src"), parent, nil
}

// Materialize всегда возвращает каталог с кодом джобы на нужном коммите.
// Локальная ветка проверяется первой и не делает ни одного сетевого вызова
// (REPO-02): рабочая копия того же проекта с уже подтянутым коммитом даёт
// worktree. Любое несовпадение на локальной ветке — не аномалия, а штатный
// переход на сетевую (REPO-01): пользователь вне репозитория, в чужом
// репозитории или без нужного коммита получает код, а не отказ.
func Materialize(ctx context.Context, req Request, p render.Progress) (*Checkout, error) {
	if req.SHA == "" {
		return nil, fmt.Errorf("repo: у джобы нет коммита: %w", ErrCommitNotFound)
	}

	if root, err := Root(ctx); err == nil {
		if _, ok := RemoteMatches(ctx, root, req.Host, req.ProjectPath); ok && hasCommit(ctx, root, req.SHA) {
			dir, parent, err := checkoutPath(req.Base, req.ProjectPath, req.SHA, req.Persist)
			if err != nil {
				return nil, err
			}
			wt, err := Prepare(ctx, root, req.SHA, dir, p)
			if err != nil {
				os.RemoveAll(parent)
				return nil, err
			}
			return &Checkout{
				Dir:           wt.Dir,
				SHA:           wt.SHA,
				Source:        SourceLocal,
				SelfContained: false,
				Persist:       req.Persist,
				wt:            wt,
				parent:        parent,
			}, nil
		}
	}

	// Сетевая ветка: мелкий fetch коммита в bare-зеркало в кэше, затем
	// самостоятельный чекаут из зеркала.
	mirror, err := mirrorDir(req.Host, req.ProjectPath)
	if err != nil {
		return nil, err
	}
	if err := ensureMirror(ctx, mirror); err != nil {
		return nil, err
	}

	env := AuthEnv(os.Environ(), req.Host, req.ProjectPath, req.Token)
	if err := fetchCommit(ctx, mirror, req.Host, req.ProjectPath, req.SHA, req.Ref, req.Tag, env, p); err != nil {
		return nil, err
	}

	dir, parent, err := checkoutPath(req.Base, req.ProjectPath, req.SHA, req.Persist)
	if err != nil {
		return nil, err
	}
	originURL := mirrorURL(req.Host, req.ProjectPath)
	if err := materializeFromMirror(ctx, mirror, dir, req.SHA, originURL, p); err != nil {
		os.RemoveAll(parent)
		return nil, err
	}

	return &Checkout{
		Dir:           dir,
		SHA:           req.SHA,
		Source:        SourceMirror,
		SelfContained: true,
		Persist:       req.Persist,
		parent:        parent,
	}, nil
}

// materializeFromMirror наполняет уже вычисленный checkoutPath путь dir
// самостоятельным репозиторием: у него собственный служебный каталог git
// внутри dir, поэтому запросы истории внутри контейнера отвечают, а не
// падают — ровно тот дефект архива через API, из-за которого он не годится
// (idea-0.2.0 §4). Чекаут собирается не ещё одним worktree из зеркала: у
// worktree служебный каталог git лежит вне смонтированного каталога, и
// внутри контейнера запросы истории отвечали бы отказом. Источник —
// локальный каталог зеркала, поэтому ни сети, ни секрета на этом шаге нет
// вовсе, и окружение процесса не подменяется.
func materializeFromMirror(ctx context.Context, mirror, dir, sha, originURL string, p render.Progress) error {
	p.Stage("готовлю код на коммите %s…", shortSHA(sha))

	initCmd := exec.CommandContext(ctx, "git", "init", "--quiet", dir)
	if _, err := runCmd(initCmd); err != nil {
		return fmt.Errorf("repo: не удалось подготовить чекаут %s: %s: %w", dir, firstLine(err.Error()), ErrFetchFailed)
	}

	fetchCmd := exec.CommandContext(ctx, "git", "-C", dir, "fetch", "--no-tags", "--depth=1", mirror, sha)
	if _, err := runCmd(fetchCmd); err != nil {
		return fmt.Errorf("repo: не удалось скачать коммит %s из зеркала: %s: %w", shortSHA(sha), firstLine(err.Error()), ErrFetchFailed)
	}

	checkoutCmd := exec.CommandContext(ctx, "git", "-C", dir, "checkout", "--detach", sha)
	if _, err := runCmd(checkoutCmd); err != nil {
		return fmt.Errorf("repo: не удалось переключиться на коммит %s: %s: %w", shortSHA(sha), firstLine(err.Error()), ErrFetchFailed)
	}

	// Remote выставляется на чистый https-адрес проекта уже после чекаута:
	// скрипты джобы, читающие origin, видят то же, что в настоящем прогоне,
	// а токена там нет вовсе (idea §4, «после fetch — выставить remote на
	// чистый URL»).
	remoteCmd := exec.CommandContext(ctx, "git", "-C", dir, "remote", "add", "origin", originURL)
	if _, err := runCmd(remoteCmd); err != nil {
		return fmt.Errorf("repo: не удалось выставить origin: %s: %w", firstLine(err.Error()), ErrFetchFailed)
	}

	p.Stage("код на коммите %s: %s", shortSHA(sha), dir)

	return nil
}

// Remove убирает чекаут: на локальной ветке снимает worktree средствами
// git, затем безусловно удаляет родительский каталог; на зеркальной —
// только удаляет родительский каталог. Само зеркало не трогается никогда —
// это кэш, ради которого второй запуск не тянет ничего заново. Ошибки git и
// удаления не глотаются наравне: текст содержит фактический путь и готовую
// команду добивки — файлы, записанные контейнером под root, обычному
// пользователю не удалить.
//
// При Persist (--here) Remove немедленно возвращает отсутствие ошибки,
// ничего не снимая и ничего не удаляя: пользователь просил оставить код, а
// снятие worktree убрало бы ровно тот каталог, ради которого флаг и
// задавался. Следствие: на локальной ветке в репозитории пользователя
// остаётся зарегистрированный worktree — сказать об этом обязан cmd/ci
// сразу после успешного Materialize.
func (c *Checkout) Remove(ctx context.Context) error {
	if c.Persist {
		return nil
	}

	var gitErr error
	if c.wt != nil {
		gitErr = c.wt.Remove(ctx)
	}
	rmErr := os.RemoveAll(c.parent)

	if gitErr == nil && rmErr == nil {
		return nil
	}

	var cause string
	switch {
	case gitErr != nil && rmErr != nil:
		cause = fmt.Sprintf("%s; удаление каталога: %s", gitErr, rmErr)
	case gitErr != nil:
		cause = gitErr.Error()
	default:
		cause = fmt.Sprintf("удаление каталога: %s", rmErr)
	}

	hint := fmt.Sprintf("sudo rm -rf %s", c.parent)
	if c.wt != nil {
		hint += " && git worktree prune"
	}
	return fmt.Errorf("repo: не удалось убрать чекаут %s: %s\n"+
		"добейте вручную: %s — файлы, записанные контейнером под root, обычному пользователю не удалить",
		c.Dir, cause, hint)
}
