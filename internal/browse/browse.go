// Package browse — единственное место, где встречаются постраничный обход
// (internal/provider.All) и кэш списков: экраны интерфейса не знают ни про
// страницы, ни про имена узлов дерева — они просят список и получают
// список вместе с честными признаками «полон ли» и «из кэша ли». Пакет
// знает про провайдера и держит кэш и больше ни про кого.
//
// Кэш живёт только в памяти этого значения Client и ровно на время сессии
// (решение пользователя ради экономии milestone v0.3.0): персистентного
// дискового кэша со сроком годности здесь нет — только карта в памяти,
// которую ручное обновление (refresh=true) перезаписывает свежим обходом.
// Постраничный обход при этом полный: сотни проектов организации видны
// целиком, только повторное открытие интерфейса начинает обход заново.
package browse

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/f3rym/ci-shell/internal/provider"
)

// Client — точка доступа к спискам одного хоста через провайдера p, с
// кэшем списков в памяти на время жизни этого значения. Токен сюда не
// передаётся и здесь не хранится: значение провайдера уже умеет ходить в
// API, и второе место, знающее секрет, проекту не нужно.
type Client struct {
	p    provider.Provider
	host string

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// cacheEntry — запись кэша в памяти: items несёт срез конкретного типа
// (Group/Project/Pipeline/Job) как any — Client не параметризован, а
// хранить в одной карте записи разных типов иначе, кроме как через any,
// нечем; тип восстанавливается обратным приведением в load[T].
type cacheEntry struct {
	items     any
	complete  bool
	fetchedAt time.Time
}

// New создаёт Client поверх провайдера p для хоста host.
func New(p provider.Provider, host string) *Client {
	return &Client{p: p, host: host, cache: map[string]cacheEntry{}}
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

// load — общая функция получения одного списка: при refresh — обход через
// provider.All(...) и перезапись записи кэша; иначе — попытка чтения кэша
// в памяти и на попадании возврат из неё; на промахе — обход, затем
// сохранение результата. Кэш никогда не бывает причиной отказа: ошибка
// обхода возвращается наружу как есть, а сама запись в память ошибок не
// возвращает.
func load[T any](ctx context.Context, c *Client, key string, refresh bool, fetch func(context.Context, provider.PageRequest) (provider.Page[T], error)) (Result[T], error) {
	if !refresh {
		c.mu.Lock()
		e, ok := c.cache[key]
		c.mu.Unlock()
		if ok {
			items, _ := e.items.([]T)
			return Result[T]{Items: items, Complete: e.complete, Cached: true, FetchedAt: e.fetchedAt}, nil
		}
	}

	w, err := provider.All(ctx, provider.DefaultPageSize, fetch)
	if err != nil {
		return Result[T]{}, err
	}

	now := time.Now()
	c.mu.Lock()
	c.cache[key] = cacheEntry{items: w.Items, complete: w.Complete, fetchedAt: now}
	c.mu.Unlock()

	return Result[T]{Items: w.Items, Complete: w.Complete, Cached: false, FetchedAt: now}, nil
}

// Groups возвращает список групп: пустой parent — верхний уровень, видимый
// токену; непустой — подгруппы группы с этим полным путём. Обход всех
// страниц делает provider.All(...) внутри общей load.
func (c *Client) Groups(ctx context.Context, parent string, refresh bool) (Result[provider.Group], error) {
	key := "groups\x00" + parent
	return load(ctx, c, key, refresh, func(ctx context.Context, req provider.PageRequest) (provider.Page[provider.Group], error) {
		return c.p.Groups(ctx, parent, req)
	})
}

// Projects возвращает список проектов пространства имён ns. Обход всех
// страниц делает provider.All(...) внутри общей load.
func (c *Client) Projects(ctx context.Context, ns provider.Namespace, refresh bool) (Result[provider.Project], error) {
	key := "projects\x00" + string(ns.Kind) + "/" + ns.Path
	return load(ctx, c, key, refresh, func(ctx context.Context, req provider.PageRequest) (provider.Page[provider.Project], error) {
		return c.p.Projects(ctx, ns, req)
	})
}

// Pipelines возвращает список пайплайнов проекта. Обход всех страниц
// делает provider.All(...) внутри общей load.
func (c *Client) Pipelines(ctx context.Context, projectPath string, refresh bool) (Result[provider.Pipeline], error) {
	key := "pipelines\x00" + projectPath
	return load(ctx, c, key, refresh, func(ctx context.Context, req provider.PageRequest) (provider.Page[provider.Pipeline], error) {
		return c.p.ProjectPipelines(ctx, projectPath, req)
	})
}

// Jobs возвращает список джоб пайплайна. Обход всех страниц делает
// provider.All(...) внутри общей load.
func (c *Client) Jobs(ctx context.Context, projectPath string, pipelineID int64, refresh bool) (Result[provider.Job], error) {
	key := fmt.Sprintf("jobs\x00%s#%d", projectPath, pipelineID)
	return load(ctx, c, key, refresh, func(ctx context.Context, req provider.PageRequest) (provider.Page[provider.Job], error) {
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
