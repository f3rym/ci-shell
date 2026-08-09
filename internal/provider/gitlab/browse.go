package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/f3rym/ci-shell/internal/joburl"
	"github.com/f3rym/ci-shell/internal/provider"
)

// userResponse — часть ответа GET /user, которая переносится в provider.User.
type userResponse struct {
	Username string `json:"username"`
	Name     string `json:"name"`
}

// groupResponse — элемент ответа GET /groups и GET /groups/:id/subgroups.
type groupResponse struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	FullPath string `json:"full_path"`
	ParentID int64  `json:"parent_id"`
}

// projectResponse — элемент ответа GET /groups/:id/projects и
// GET /users/:id/projects (форма simple=true).
type projectResponse struct {
	ID                int64      `json:"id"`
	Name              string     `json:"name"`
	Path              string     `json:"path"`
	PathWithNamespace string     `json:"path_with_namespace"`
	DefaultBranch     string     `json:"default_branch"`
	LastActivityAt    *time.Time `json:"last_activity_at"`
	Namespace         struct {
		FullPath string `json:"full_path"`
	} `json:"namespace"`
}

// pipelineResponse — элемент ответа GET /projects/:id/pipelines.
type pipelineResponse struct {
	ID        int64      `json:"id"`
	IID       int64      `json:"iid"`
	Ref       string     `json:"ref"`
	SHA       string     `json:"sha"`
	Status    string     `json:"status"`
	Source    string     `json:"source"`
	WebURL    string     `json:"web_url"`
	CreatedAt *time.Time `json:"created_at"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// toGroup переносит ответ API в доменный provider.Group. Каждая строка,
// идущая в доменный тип, проходит через provider.SafeText — правило
// применяется здесь один раз для всех трёх переносов (toGroup, toProject,
// toPipeline), потому что дальше эти строки уходят прямо в терминал
// (T-10-03).
func toGroup(r groupResponse) provider.Group {
	return provider.Group{
		ID:       r.ID,
		Name:     provider.SafeText(r.Name),
		Path:     provider.SafeText(r.Path),
		FullPath: provider.SafeText(r.FullPath),
		ParentID: r.ParentID,
	}
}

// toProject переносит ответ API в доменный provider.Project.
func toProject(r projectResponse) provider.Project {
	p := provider.Project{
		ID:            r.ID,
		Name:          provider.SafeText(r.Name),
		Path:          provider.SafeText(r.Path),
		FullPath:      provider.SafeText(r.PathWithNamespace),
		Namespace:     provider.SafeText(r.Namespace.FullPath),
		DefaultBranch: provider.SafeText(r.DefaultBranch),
	}
	if r.LastActivityAt != nil {
		p.LastActivity = *r.LastActivityAt
	}
	return p
}

// toPipeline переносит ответ API в доменный provider.Pipeline. Status тем
// же словом, что отдаёт API: статус-слово в этом проекте не переводится
// (09-UI-SPEC.md, «Символы состояния»).
func toPipeline(r pipelineResponse) provider.Pipeline {
	p := provider.Pipeline{
		ID:     r.ID,
		IID:    r.IID,
		Ref:    provider.SafeText(r.Ref),
		SHA:    r.SHA,
		Status: provider.SafeText(r.Status),
		Source: provider.SafeText(r.Source),
		WebURL: r.WebURL,
	}
	if r.CreatedAt != nil {
		p.CreatedAt = *r.CreatedAt
	}
	if r.UpdatedAt != nil {
		p.UpdatedAt = *r.UpdatedAt
	}
	return p
}

// CurrentUser возвращает того, чьим токеном обход ходит в API. Отказ
// доступа отображается в уже существующую ошибку отклонённого токена
// (provider.ErrUnauthorized, обёрнутая в request) — второго перевода
// ошибок в текст в этом файле не заводится.
func (c *Client) CurrentUser(ctx context.Context) (provider.User, error) {
	body, err := c.do(ctx, http.MethodGet, "/user")
	if err != nil {
		return provider.User{}, err
	}

	var resp userResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return provider.User{}, fmt.Errorf("gitlab: разбор ответа текущего пользователя: %w", err)
	}

	return provider.User{
		Username: provider.SafeText(resp.Username),
		Name:     provider.SafeText(resp.Name),
	}, nil
}

// Groups возвращает страницу групп: пустой parent — группы верхнего уровня,
// видимые токену (top_level_only=true); непустой — подгруппы группы с этим
// полным путём. Путь группы перед подстановкой проверяется
// joburl.ValidProjectPath и экранируется тем же способом, что уже применён
// к пути проекта (T-10-10).
func (c *Client) Groups(ctx context.Context, parent string, req provider.PageRequest) (provider.Page[provider.Group], error) {
	var (
		body   []byte
		cursor provider.Cursor
		err    error
	)

	if parent == "" {
		path, perr := pagePath("/groups?top_level_only=true", req)
		if perr != nil {
			return provider.Page[provider.Group]{}, perr
		}
		body, cursor, err = c.doPaged(ctx, path)
	} else {
		if !joburl.ValidProjectPath(parent) {
			return provider.Page[provider.Group]{}, fmt.Errorf("gitlab: путь группы %q недопустим: %w", parent, provider.ErrGroupNotFound)
		}
		path, perr := pagePath(fmt.Sprintf("/groups/%s/subgroups", url.PathEscape(parent)), req)
		if perr != nil {
			return provider.Page[provider.Group]{}, perr
		}
		body, cursor, err = c.doPaged(ctx, path)
	}

	if err != nil {
		if errors.Is(err, errStatusNotFound) {
			return provider.Page[provider.Group]{}, fmt.Errorf("gitlab: группа %q на хосте %s не найдена: %w", parent, c.host, provider.ErrGroupNotFound)
		}
		return provider.Page[provider.Group]{}, err
	}

	var raw []groupResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return provider.Page[provider.Group]{}, fmt.Errorf("gitlab: разбор списка групп: %w", err)
	}

	items := make([]provider.Group, 0, len(raw))
	for _, r := range raw {
		items = append(items, toGroup(r))
	}
	return provider.Page[provider.Group]{Items: items, Next: cursor}, nil
}

// Projects возвращает страницу проектов пространства имён ns. Адрес зависит
// от вида пространства имён: для группы — проекты группы без включения
// подгрупп (подгруппы в дереве отдельные узлы, включать их сюда значило бы
// показать один проект дважды), для человека — проекты этого пользователя.
// Путь пространства имён проверяется и экранируется так же, как путь
// группы. Запрашивается упрощённая форма проекта (simple=true) — полный
// проект несёт десятки полей, которые экрану не нужны, и это лишний трафик
// на каждой из сотен страниц.
func (c *Client) Projects(ctx context.Context, ns provider.Namespace, req provider.PageRequest) (provider.Page[provider.Project], error) {
	if !joburl.ValidProjectPath(ns.Path) {
		return provider.Page[provider.Project]{}, fmt.Errorf("gitlab: путь пространства имён %q недопустим: %w", ns.Path, provider.ErrProjectNotFound)
	}

	var (
		body   []byte
		cursor provider.Cursor
		err    error
	)

	switch ns.Kind {
	case provider.NamespaceGroup:
		base := fmt.Sprintf("/groups/%s/projects?include_subgroups=false&simple=true", url.PathEscape(ns.Path))
		path, perr := pagePath(base, req)
		if perr != nil {
			return provider.Page[provider.Project]{}, perr
		}
		body, cursor, err = c.doPaged(ctx, path)
	default:
		// provider.NamespaceUser — проекты личного пространства человека.
		base := fmt.Sprintf("/users/%s/projects?simple=true", url.PathEscape(ns.Path))
		path, perr := pagePath(base, req)
		if perr != nil {
			return provider.Page[provider.Project]{}, perr
		}
		body, cursor, err = c.doPaged(ctx, path)
	}

	if err != nil {
		if errors.Is(err, errStatusNotFound) {
			return provider.Page[provider.Project]{}, fmt.Errorf("gitlab: пространство имён %s %s на хосте %s не найдено: %w", ns.Kind, ns.Path, c.host, provider.ErrProjectNotFound)
		}
		return provider.Page[provider.Project]{}, err
	}

	var raw []projectResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return provider.Page[provider.Project]{}, fmt.Errorf("gitlab: разбор списка проектов %s: %w", ns.Path, err)
	}

	items := make([]provider.Project, 0, len(raw))
	for _, r := range raw {
		items = append(items, toProject(r))
	}
	return provider.Page[provider.Project]{Items: items, Next: cursor}, nil
}

// ProjectPipelines возвращает страницу пайплайнов проекта, свежие первыми
// (сортировка по идентификатору по убыванию).
func (c *Client) ProjectPipelines(ctx context.Context, projectPath string, req provider.PageRequest) (provider.Page[provider.Pipeline], error) {
	if !joburl.ValidProjectPath(projectPath) {
		return provider.Page[provider.Pipeline]{}, fmt.Errorf("gitlab: путь проекта %q недопустим: %w", projectPath, provider.ErrProjectNotFound)
	}

	base := fmt.Sprintf("/projects/%s/pipelines?order_by=id&sort=desc", url.PathEscape(projectPath))
	path, err := pagePath(base, req)
	if err != nil {
		return provider.Page[provider.Pipeline]{}, err
	}

	body, cursor, err := c.doPaged(ctx, path)
	if err != nil {
		if errors.Is(err, errStatusNotFound) {
			return provider.Page[provider.Pipeline]{}, fmt.Errorf("gitlab: проект %s на хосте %s не найден: %w", projectPath, c.host, provider.ErrPipelineNotFound)
		}
		return provider.Page[provider.Pipeline]{}, err
	}

	var raw []pipelineResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return provider.Page[provider.Pipeline]{}, fmt.Errorf("gitlab: разбор списка пайплайнов проекта %s: %w", projectPath, err)
	}

	items := make([]provider.Pipeline, 0, len(raw))
	for _, r := range raw {
		items = append(items, toPipeline(r))
	}
	return provider.Page[provider.Pipeline]{Items: items, Next: cursor}, nil
}

// PipelineJobs возвращает страницу джоб одного пайплайна pipelineID проекта
// projectPath. Переехал из gitlab.go (куда его поставил план 09-01) сюда, к
// остальным спискам, и получил постраничную форму — одностраничность вместе
// с её оговоркой о том, что список мог не поместиться в страницу, из плана
// 09-01 отсюда снята: это и есть обещание, которое Фаза 10 выполняет.
func (c *Client) PipelineJobs(ctx context.Context, projectPath string, pipelineID int64, req provider.PageRequest) (provider.Page[provider.Job], error) {
	if !joburl.ValidProjectPath(projectPath) {
		return provider.Page[provider.Job]{}, fmt.Errorf("gitlab: путь проекта %q недопустим: %w", projectPath, provider.ErrProjectNotFound)
	}

	base := fmt.Sprintf("/projects/%s/pipelines/%d/jobs", url.PathEscape(projectPath), pipelineID)
	path, err := pagePath(base, req)
	if err != nil {
		return provider.Page[provider.Job]{}, err
	}

	body, cursor, err := c.doPaged(ctx, path)
	if err != nil {
		if errors.Is(err, errStatusNotFound) {
			return provider.Page[provider.Job]{}, fmt.Errorf("gitlab: пайплайн %s#%d на хосте %s не найден либо недоступен: %w", projectPath, pipelineID, c.host, provider.ErrJobNotFound)
		}
		return provider.Page[provider.Job]{}, err
	}

	var raw []jobResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return provider.Page[provider.Job]{}, fmt.Errorf("gitlab: разбор списка джоб пайплайна %s#%d: %w", projectPath, pipelineID, err)
	}

	items := make([]provider.Job, 0, len(raw))
	for _, r := range raw {
		items = append(items, toJob(r, projectPath))
	}
	return provider.Page[provider.Job]{Items: items, Next: cursor}, nil
}
