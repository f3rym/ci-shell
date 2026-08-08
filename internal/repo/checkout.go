package repo

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/f3rym/ci-shell/internal/render"
)

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

	wt     *Worktree
	parent string
}

// checkoutPath — единственное место, где решается физическое расположение
// чекаута: временный родительский каталог со случайным именем и говорящим
// префиксом job- внутри base (два одновременных запуска не должны драться за
// один путь), сам код кладётся в его подкаталог src. Возвращаются оба пути:
// parent нужен уборке, dir — монтированию. В плане 05-02 именно эта функция
// получает второе поведение под флаг --here, другие места правиться не
// должны.
func checkoutPath(base string) (dir, parent string, err error) {
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
		if _, ok := RemoteMatches(ctx, root, req.ProjectPath); ok && hasCommit(ctx, root, req.SHA) {
			dir, parent, err := checkoutPath(req.Base)
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

	env := AuthEnv(os.Environ(), req.Token)
	if err := fetchCommit(ctx, mirror, req.Host, req.ProjectPath, req.SHA, req.Ref, req.Tag, env, p); err != nil {
		return nil, err
	}

	dir, parent, err := checkoutPath(req.Base)
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
func (c *Checkout) Remove(ctx context.Context) error {
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
