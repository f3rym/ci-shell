// Package gitlab реализует internal/provider.Provider поверх GitLab REST API.
package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/f3rym/ci-shell/internal/provider"
	"github.com/f3rym/ci-shell/internal/token"
)

const (
	// maxResponseBytes ограничивает чтение тела ответа, чтобы раздутый или
	// зависший ответ GitLab не съел память и не повесил утилиту (T-01-05).
	maxResponseBytes = 10 * 1024 * 1024 // 10 МиБ
	// errorBodyLimit — сколько байт тела не-2xx ответа попадает в текст
	// ошибки (T-01-07: диагностическая ценность без риска раздутого вывода).
	errorBodyLimit = 512
)

// errStatusNotFound — внутренний маркер «сервер ответил 404». Конкретный
// смысл 404 зависит от эндпоинта (нет джобы или нет файла конфига), поэтому
// сами методы (JobByID, MergedConfig) оборачивают его в подходящую
// sentinel-ошибку provider — do() отвечает только за транспорт.
var errStatusNotFound = errors.New("gitlab: сервер ответил 404")

// Client — HTTP-клиент GitLab API. Реализует provider.Provider.
type Client struct {
	host       string
	token      token.Token
	httpClient *http.Client
	baseURL    string
}

// New создаёт клиент GitLab для указанного хоста с уже отрезолвленным
// токеном. Клиент сам окружение не читает — резолв токена делает пакет token.
func New(host string, tok token.Token) *Client {
	return &Client{
		host:  host,
		token: tok,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		baseURL: "https://" + host + "/api/v4",
	}
}

var _ provider.Provider = (*Client)(nil)

// do выполняет запрос к GitLab API с заголовком PRIVATE-TOKEN и лимитом на
// чтение тела ответа. Токен передаётся только заголовком — никогда в пути
// или строке запроса (T-01-01). Общая для всех методов клиента логика:
// аутентификация, лимит чтения, отображение 401/403 и прочих не-2xx кодов.
// Отображение 404 в конкретную sentinel-ошибку — на совести вызывающего
// метода, потому что смысл «не найдено» зависит от эндпоинта.
func (c *Client) do(ctx context.Context, method, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("gitlab: собрать запрос %s: %w", path, err)
	}
	req.Header.Set("PRIVATE-TOKEN", c.token.Secret)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab: запрос %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("gitlab: чтение ответа %s: %w", path, err)
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return body, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("gitlab: %s: %w", path, provider.ErrUnauthorized)
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("gitlab: %s: %w", path, errStatusNotFound)
	default:
		snippet := body
		if len(snippet) > errorBodyLimit {
			snippet = snippet[:errorBodyLimit]
		}
		return nil, fmt.Errorf("gitlab: %s: неожиданный статус %d: %s", path, resp.StatusCode, snippet)
	}
}

// jobResponse — часть ответа GET /projects/:id/jobs/:job_id, которая
// переносится в provider.Job.
type jobResponse struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Stage         string `json:"stage"`
	Status        string `json:"status"`
	FailureReason string `json:"failure_reason"`
	Ref           string `json:"ref"`
	WebURL        string `json:"web_url"`
	Commit        struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"commit"`
	Project struct {
		ID int64 `json:"id"`
	} `json:"project"`
	Runner struct {
		Description string `json:"description"`
	} `json:"runner"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// JobByID возвращает метаданные джобы по пути проекта и её id.
func (c *Client) JobByID(ctx context.Context, projectPath string, jobID int64) (provider.Job, error) {
	path := fmt.Sprintf("/projects/%s/jobs/%d", url.PathEscape(projectPath), jobID)
	body, err := c.do(ctx, http.MethodGet, path)
	if err != nil {
		if errors.Is(err, errStatusNotFound) {
			return provider.Job{}, fmt.Errorf("gitlab: джоба %s #%d не найдена: %w", projectPath, jobID, provider.ErrJobNotFound)
		}
		return provider.Job{}, err
	}

	var resp jobResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return provider.Job{}, fmt.Errorf("gitlab: разбор ответа джобы %s #%d: %w", projectPath, jobID, err)
	}

	job := provider.Job{
		ID:            resp.ID,
		Name:          resp.Name,
		Stage:         resp.Stage,
		Status:        resp.Status,
		FailureReason: resp.FailureReason,
		Ref:           resp.Ref,
		CommitSHA:     resp.Commit.ID,
		CommitTitle:   resp.Commit.Title,
		WebURL:        resp.WebURL,
		ProjectPath:   projectPath,
		ProjectID:     resp.Project.ID,
		RunnerDesc:    resp.Runner.Description,
	}
	if resp.StartedAt != nil {
		job.StartedAt = *resp.StartedAt
	}
	if resp.FinishedAt != nil {
		job.FinishedAt = *resp.FinishedAt
	}
	return job, nil
}

// MergedConfig — временная заглушка задачи 1. Реальная реализация появляется
// в задаче 2 (internal/provider/gitlab/config.go) и заменяет этот метод.
func (c *Client) MergedConfig(ctx context.Context, projectPath, sha string) (provider.Config, error) {
	return provider.Config{}, fmt.Errorf("gitlab: MergedConfig ещё не реализован: %w", provider.ErrNotSupported)
}

// FindFailedJob — автоопределение упавшей джобы без ссылки. Это требование
// NEXT-01 (этап 1 из idea, v2), в Фазе 1 не реализовано.
func (c *Client) FindFailedJob(ctx context.Context, repo, ref string) (provider.Job, error) {
	return provider.Job{}, fmt.Errorf("gitlab: автоопределение упавшей джобы без ссылки — см. NEXT-01: %w", provider.ErrNotSupported)
}

// Artifacts — скачивание артефактов предыдущих стадий. Это требование
// NEXT-03 (этап 1 из idea, v2), в Фазе 1 не реализовано.
func (c *Client) Artifacts(ctx context.Context, job provider.Job) ([]provider.Artifact, error) {
	return nil, fmt.Errorf("gitlab: скачивание артефактов — см. NEXT-03: %w", provider.ErrNotSupported)
}

// Variables — переменные проекта и группы через API. Это требование
// GLAB-02 (Фаза 2), в Фазе 1 не реализовано.
func (c *Client) Variables(ctx context.Context, job provider.Job) (map[string]string, error) {
	return nil, fmt.Errorf("gitlab: переменные проекта и группы — см. GLAB-02, Фаза 2: %w", provider.ErrNotSupported)
}
