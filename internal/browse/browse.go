// Package browse — единственное место, где встречаются постраничный обход
// (internal/provider.All) и кэш списков (internal/cache): экраны
// интерфейса не знают ни про страницы, ни про сроки годности, ни про имена
// файлов кэша — они просят список и получают список вместе с честными
// признаками «полон ли» и «из кэша ли». Пакет знает про провайдера и про
// кэш и больше ни про кого.
package browse

import (
	"context"
	"fmt"
	"time"

	"github.com/f3rym/ci-shell/internal/cache"
	"github.com/f3rym/ci-shell/internal/provider"
)

// Client — точка доступа к спискам одного хоста через провайдера p. Токен
// сюда не передаётся и здесь не хранится: значение провайдера уже умеет
// ходить в API, и второе место, знающее секрет, проекту не нужно.
type Client struct {
	p    provider.Provider
	host string
}

// New создаёт Client поверх провайдера p для хоста host — host входит в
// ключ кэша, чтобы списки разных хостов не пересекались.
func New(p provider.Provider, host string) *Client {
	return &Client{p: p, host: host}
}

// Result — список, уходящий в экран. Complete — полон ли обход (из
// результата обхода или из записи кэша), Cached — взято ли из кэша,
// FetchedAt — когда список получен на самом деле, чтобы экран мог сказать
// человеку «список от 12 минут назад», а не делать вид, что данные свежие.
type Result[T any] struct {
	Items     []T
	Complete  bool
	Cached    bool
	FetchedAt time.Time
}

// load — общая функция получения одного списка: при refresh — удаление
// записи кэша и сразу обход через provider.All(...); иначе — попытка
// чтения кэша (cache.Load) и на попадании возврат из него; на промахе —
// обход через provider.All(...), затем сохранение результата (cache.Save).
// Ошибка записи кэша НЕ становится ошибкой вызова: список уже получен, и
// не показать его из-за неудачной записи в кэш было бы худшим из решений —
// тот же принцип честной деградации, что и у сбора переменных из Фазы 6.
// Ошибка обхода возвращается наружу как есть.
func load[T any](ctx context.Context, k cache.Key, ttl time.Duration, refresh bool, fetch func(context.Context, provider.PageRequest) (provider.Page[T], error)) (Result[T], error) {
	if refresh {
		_ = cache.Drop(k)
	} else if entry, ok := cache.Load[T](k, ttl); ok {
		return Result[T]{Items: entry.Items, Complete: entry.Complete, Cached: true, FetchedAt: entry.FetchedAt}, nil
	}

	w, err := provider.All(ctx, provider.DefaultPageSize, fetch)
	if err != nil {
		return Result[T]{}, err
	}

	now := time.Now()
	_ = cache.Save(k, cache.Entry[T]{Items: w.Items, Complete: w.Complete, FetchedAt: now})

	return Result[T]{Items: w.Items, Complete: w.Complete, Cached: false, FetchedAt: now}, nil
}

// Groups возвращает список групп: пустой parent — верхний уровень, видимый
// токену; непустой — подгруппы группы с этим полным путём. Обход всех
// страниц делает provider.All(...) внутри общей load — срок годности
// TTLTree, потому что дерево групп меняется редко.
func (c *Client) Groups(ctx context.Context, parent string, refresh bool) (Result[provider.Group], error) {
	k := cache.Key{Host: c.host, Kind: "groups", Scope: parent}
	return load(ctx, k, cache.TTLTree, refresh, func(ctx context.Context, req provider.PageRequest) (provider.Page[provider.Group], error) {
		return c.p.Groups(ctx, parent, req)
	})
}

// Projects возвращает список проектов пространства имён ns. Обход всех
// страниц делает provider.All(...) внутри общей load — срок годности
// TTLTree, потому что список проектов меняется редко.
func (c *Client) Projects(ctx context.Context, ns provider.Namespace, refresh bool) (Result[provider.Project], error) {
	k := cache.Key{Host: c.host, Kind: "projects", Scope: string(ns.Kind) + "/" + ns.Path}
	return load(ctx, k, cache.TTLTree, refresh, func(ctx context.Context, req provider.PageRequest) (provider.Page[provider.Project], error) {
		return c.p.Projects(ctx, ns, req)
	})
}

// Pipelines возвращает список пайплайнов проекта. Обход всех страниц
// делает provider.All(...) внутри общей load — срок годности TTLRuns,
// потому что пайплайны меняются на глазах.
func (c *Client) Pipelines(ctx context.Context, projectPath string, refresh bool) (Result[provider.Pipeline], error) {
	k := cache.Key{Host: c.host, Kind: "pipelines", Scope: projectPath}
	return load(ctx, k, cache.TTLRuns, refresh, func(ctx context.Context, req provider.PageRequest) (provider.Page[provider.Pipeline], error) {
		return c.p.ProjectPipelines(ctx, projectPath, req)
	})
}

// Jobs возвращает список джоб пайплайна. Обход всех страниц делает
// provider.All(...) внутри общей load — срок годности TTLRuns, потому что
// джобы меняются на глазах.
func (c *Client) Jobs(ctx context.Context, projectPath string, pipelineID int64, refresh bool) (Result[provider.Job], error) {
	k := cache.Key{Host: c.host, Kind: "jobs", Scope: fmt.Sprintf("%s#%d", projectPath, pipelineID)}
	return load(ctx, k, cache.TTLRuns, refresh, func(ctx context.Context, req provider.PageRequest) (provider.Page[provider.Job], error) {
		return c.p.PipelineJobs(ctx, projectPath, pipelineID, req)
	})
}

// Log — сквозной вызов провайдера без кэша. Лог джобы может содержать
// значение секрета, которое GitLab не сумел замаскировать (многострочные
// значения не маскируются), и такой файл на диске пережил бы процесс. Лог
// живёт только в памяти экрана и только в пределах сессии.
func (c *Client) Log(ctx context.Context, projectPath string, jobID int64, opts provider.LogOptions) (provider.Log, error) {
	return c.p.JobLog(ctx, projectPath, jobID, opts)
}
