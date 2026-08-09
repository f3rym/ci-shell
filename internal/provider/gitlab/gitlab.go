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
	// apiRoot — корень пути GitLab REST API. nextCursor проверяет, что
	// адрес следующей страницы, присланный сервером, не уводит запрос с
	// токеном за пределы этого корня (T-10-01).
	apiRoot = "/api/v4"
)

// errStatusNotFound — внутренний маркер «сервер ответил 404». Конкретный
// смысл 404 зависит от эндпоинта (нет джобы или нет файла конфига), поэтому
// сами методы (JobByID, MergedConfig, Groups, ...) оборачивают его в
// подходящую sentinel-ошибку provider — транспорт отвечает только за
// доставку.
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
	c := &Client{
		host:  host,
		token: tok,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			// The token header is a custom header: unlike Authorization, Go's
			// http.Client forwards it verbatim on cross-host redirects. Any
			// redirect on /api/v4 is abnormal, so do not follow redirects at
			// all — the 3xx response surfaces as an ordinary non-2xx error
			// (WR-01, T-01-01).
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	c.baseURL = "https://" + c.host + apiRoot
	return c
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

// request — общая часть транспорта: собирает запрос, ставит заголовок с
// токеном (заголовком и только заголовком — токен по-прежнему никогда не
// попадает ни в путь, ни в строку запроса), добавляет переданные заголовки
// (например, диапазон для хвоста лога), выполняет запрос и разбирает статус.
//
// На успехе тело НЕ читается — возвращается ответ с открытым телом, и
// закрыть его обязан вызывающий; на любом не-успехе тело читается
// ограниченным читателем, редактируется от значения токена, обрезается по
// границе руны и закрывается прямо здесь. Редактирование идёт до обрезки, а
// не после — это уже исправленный в Фазе 1 дефект (CR-01, T-01-09), и
// повторять его нельзя.
func (c *Client) request(ctx context.Context, method, path string, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("gitlab: собрать запрос %s: %w", path, err)
	}
	req.Header.Set("PRIVATE-TOKEN", c.token.Secret)
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab: запрос %s: %s", path, c.redact(err.Error()))
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("gitlab: чтение ответа %s: %s", path, c.redact(err.Error()))
	}

	switch {
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

// do — тонкая обёртка над request для одиночных (не постраничных) запросов:
// без дополнительных заголовков, читает тело ограниченным читателем на
// maxResponseBytes, закрывает его. Сигнатура и поведение прежние — ни один
// существующий вызывающий не правится.
func (c *Client) do(ctx context.Context, method, path string) ([]byte, error) {
	resp, err := c.request(ctx, method, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("gitlab: чтение ответа %s: %s", path, c.redact(err.Error()))
	}
	return body, nil
}

// doPaged — то же, что do, плюс курсор следующей страницы, взятый из
// заголовка связей ответа (BROW-02, Фаза 10).
func (c *Client) doPaged(ctx context.Context, path string) ([]byte, provider.Cursor, error) {
	resp, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, "", fmt.Errorf("gitlab: чтение страницы %s: %s", path, c.redact(err.Error()))
	}

	cursor, err := c.nextCursor(resp.Header.Get("Link"))
	if err != nil {
		return nil, "", err
	}
	return body, cursor, nil
}

// nextCursor разбирает заголовок связей (RFC 8288 Link) ответа GitLab:
// части через запятую, нужна та, у которой отношение rel="next"; адрес
// берётся из угловых скобок.
//
// Дальше — три проверки, и все три обязательны: схема только https, хост в
// точности совпадает с хостом клиента, путь начинается с корня API. Любая
// непройденная проверка — ошибка, обёрнутая вокруг ErrBadPageLink, с
// указанием ожидаемого хоста. Заголовок пришёл от сервера, а следующий
// запрос уйдёт с токеном пользователя — «сходи по адресу, который я тебе
// скажу» от чужого сервера — это ровно тот способ увести токен на чужой
// хост, от которого в Фазе 1 уже закрыты редиректы (T-01-01, T-10-01).
//
// Возвращается не адрес целиком, а относительный путь с запросом (то есть
// адрес за вычетом корня API), потому что дальше он пойдёт в do/doPaged,
// который сам приставит базовый адрес.
//
// Отсутствие нужного отношения — не ошибка: пустой курсор, конец списка.
//
// Выбор пагинации: берётся обычная offset-пагинация, потому что заголовок
// связей отдают все списочные эндпоинты, а keyset — не все; переход на
// keyset не потребует ни правки интерфейса, ни правки потребителей ровно
// потому, что курсор непрозрачен (задача 1). Собственный адрес следующей
// страницы не собирается никогда — только тот, что прислал сервер.
func (c *Client) nextCursor(link string) (provider.Cursor, error) {
	if link == "" {
		return "", nil
	}

	for _, part := range strings.Split(link, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, `rel="next"`) {
			continue
		}

		start := strings.Index(part, "<")
		end := strings.Index(part, ">")
		if start < 0 || end < 0 || end <= start {
			return "", fmt.Errorf("gitlab: заголовок связей повреждён: %w", provider.ErrBadPageLink)
		}
		raw := part[start+1 : end]

		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("gitlab: адрес следующей страницы не разобрать: %w", provider.ErrBadPageLink)
		}
		if u.Scheme != "https" {
			return "", fmt.Errorf("gitlab: адрес следующей страницы использует не https: %w", provider.ErrBadPageLink)
		}
		if u.Host != c.host {
			return "", fmt.Errorf("gitlab: адрес следующей страницы указывает не на хост %s: %w", c.host, provider.ErrBadPageLink)
		}
		if !strings.HasPrefix(u.Path, apiRoot) {
			return "", fmt.Errorf("gitlab: адрес следующей страницы вне корня API %s: %w", apiRoot, provider.ErrBadPageLink)
		}

		rel := strings.TrimPrefix(u.Path, apiRoot)
		if u.RawQuery != "" {
			rel += "?" + u.RawQuery
		}
		return provider.Cursor(rel), nil
	}

	return "", nil
}

// pagePath строит путь запроса первой либо последующей страницы списка,
// адресуемого base. Пустой req.Cursor — первая страница: к base
// добавляется размер страницы (req.Size, а при нуле —
// provider.DefaultPageSize) с правильным разделителем в зависимости от
// того, есть ли в base строка запроса. Непустой req.Cursor — путь курсора,
// но только после проверки, что он начинается с косой черты и не содержит
// ни схемы, ни сегмента перехода на уровень выше.
//
// Курсор — собственное произведение клиента (nextCursor), и всё же он
// проверяется повторно, потому что между его выдачей и употреблением лежит
// слой моделей интерфейса, а ошибка там не должна превращаться в запрос по
// чужому адресу (T-10-02).
func pagePath(base string, req provider.PageRequest) (string, error) {
	if req.Cursor == "" {
		size := req.Size
		if size <= 0 {
			size = provider.DefaultPageSize
		}
		sep := "?"
		if strings.Contains(base, "?") {
			sep = "&"
		}
		return fmt.Sprintf("%s%sper_page=%d", base, sep, size), nil
	}

	cursor := string(req.Cursor)
	if !strings.HasPrefix(cursor, "/") || strings.Contains(cursor, "://") || strings.Contains(cursor, "..") {
		return "", fmt.Errorf("gitlab: курсор страницы повреждён: %w", provider.ErrBadPageLink)
	}
	return cursor, nil
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
