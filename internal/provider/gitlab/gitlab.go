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
	"strings"
	"time"
	"unicode/utf8"

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
			// PRIVATE-TOKEN is a custom header: unlike Authorization, Go's
			// http.Client forwards it verbatim on cross-host redirects. Any
			// redirect on /api/v4 is abnormal, so do not follow redirects at
			// all — the 3xx response surfaces as an ordinary non-2xx error
			// (WR-01, T-01-01).
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		baseURL: "https://" + host + "/api/v4",
	}
}

var _ provider.Provider = (*Client)(nil)

// redact заменяет все вхождения значения токена в s на плейсхолдер.
// GitLab и промежуточные прокси иногда отражают заголовки запроса в теле
// ответа не-2xx — без этого фильтра токен уехал бы в терминал пользователя
// и в его баг-репорт (T-01-09).
func (c *Client) redact(s string) string {
	if c.token.Secret == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token.Secret, "<токен скрыт>")
}

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
		return nil, fmt.Errorf("gitlab: запрос %s: %s", path, c.redact(err.Error()))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("gitlab: чтение ответа %s: %s", path, c.redact(err.Error()))
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return body, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("gitlab: %s: токен (%s) отклонён: %w", path, c.token.String(), provider.ErrUnauthorized)
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("gitlab: %s: %w", path, errStatusNotFound)
	default:
		// Redact the FULL body before truncating: truncating first can cut a
		// reflected token at the snippet boundary, and the partial token then
		// escapes strings.ReplaceAll and leaks into terminal output (CR-01,
		// T-01-09).
		redacted := c.redact(string(body))
		if len(redacted) > errorBodyLimit {
			cut := errorBodyLimit
			// Back off to a rune boundary so the snippet does not end in a
			// broken multi-byte sequence.
			for cut > 0 && !utf8.RuneStart(redacted[cut]) {
				cut--
			}
			redacted = redacted[:cut]
		}
		return nil, fmt.Errorf("gitlab: %s: неожиданный статус %d: %s", path, resp.StatusCode, redacted)
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
	Tag           bool   `json:"tag"`
	WebURL        string `json:"web_url"`
	Commit        struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"commit"`
	Project struct {
		ID int64 `json:"id"`
	} `json:"project"`
	Pipeline struct {
		ID  int64 `json:"id"`
		IID int64 `json:"iid"`
	} `json:"pipeline"`
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
			return provider.Job{}, fmt.Errorf("gitlab: джоба %s#%d на хосте %s не найдена: %w", projectPath, jobID, c.host, provider.ErrJobNotFound)
		}
		return provider.Job{}, err
	}

	var resp jobResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return provider.Job{}, fmt.Errorf("gitlab: разбор ответа джобы %s #%d: %w", projectPath, jobID, err)
	}

	return toJob(resp, projectPath), nil
}

// toJob переносит одну запись ответа API (полученную и через JobByID, и
// через PipelineJobs) в доменный provider.Job — единственное место сборки,
// чтобы два места сборки из одного и того же ответа не разошлись при
// первом добавленном поле.
func toJob(resp jobResponse, projectPath string) provider.Job {
	job := provider.Job{
		ID:            resp.ID,
		Name:          resp.Name,
		Stage:         resp.Stage,
		Status:        resp.Status,
		FailureReason: resp.FailureReason,
		Ref:           resp.Ref,
		Tag:           resp.Tag,
		CommitSHA:     resp.Commit.ID,
		CommitTitle:   resp.Commit.Title,
		WebURL:        resp.WebURL,
		ProjectPath:   projectPath,
		ProjectID:     resp.Project.ID,
		PipelineID:    resp.Pipeline.ID,
		PipelineIID:   resp.Pipeline.IID,
		RunnerDesc:    resp.Runner.Description,
	}
	if resp.StartedAt != nil {
		job.StartedAt = *resp.StartedAt
	}
	if resp.FinishedAt != nil {
		job.FinishedAt = *resp.FinishedAt
	}
	return job
}

// PipelineJobs возвращает джобы одного пайплайна pipelineID проекта
// projectPath — одна страница до ста джоб (per_page=100). Постраничный
// обход и кэш — требование BROW-02 Фазы 10; полнота страницы видна
// вызывающему по длине среза — честную оговорку о неполном списке строит
// подписчик (internal/ui/jobs.go), а не этот метод.
func (c *Client) PipelineJobs(ctx context.Context, projectPath string, pipelineID int64) ([]provider.Job, error) {
	path := fmt.Sprintf("/projects/%s/pipelines/%d/jobs?per_page=100", url.PathEscape(projectPath), pipelineID)
	body, err := c.do(ctx, http.MethodGet, path)
	if err != nil {
		if errors.Is(err, errStatusNotFound) {
			return nil, fmt.Errorf("gitlab: пайплайн %s#%d на хосте %s не найден либо недоступен: %w", projectPath, pipelineID, c.host, provider.ErrJobNotFound)
		}
		return nil, err
	}

	var raw []jobResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("gitlab: разбор списка джоб пайплайна %s#%d: %w", projectPath, pipelineID, err)
	}

	jobs := make([]provider.Job, 0, len(raw))
	for _, r := range raw {
		jobs = append(jobs, toJob(r, projectPath))
	}
	return jobs, nil
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
