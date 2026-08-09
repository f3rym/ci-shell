package provider

import "context"

// Пределы обхода списка. Список приходит от чужого сервера, и «сколько
// отдадут, столько и сложим в память» — это отказ в обслуживании самим
// себе (T-10-08, Фаза 10).
const (
	// DefaultPageSize — максимум, который GitLab отдаёт за страницу.
	DefaultPageSize = 100
	// MaxPages — предел числа запросов на один список.
	MaxPages = 200
	// MaxItems — предел числа записей, которые обход держит в памяти.
	MaxItems = 5000
)

// Walk — результат обхода всех страниц. Complete равен false, когда обход
// остановлен пределом, а не концом списка: потребитель обязан сказать об
// этом человеку, а не делать вид, что список полон.
type Walk[T any] struct {
	Items    []T
	Complete bool
}

// All обходит все страницы списка через fetch: нулевой курсор на первой
// итерации, дальше — Next предыдущей страницы. Выход по пустому Next даёт
// Complete=true; достижение любого из пределов (число страниц, число
// записей) или повторяющийся курсор (зациклившийся сервер не должен вешать
// утилиту) даёт Complete=false. Отмена контекста возвращается наружу как
// есть.
func All[T any](ctx context.Context, size int, fetch func(context.Context, PageRequest) (Page[T], error)) (Walk[T], error) {
	var items []T
	var cursor Cursor

	for fetched := 0; fetched < MaxPages; fetched++ {
		if err := ctx.Err(); err != nil {
			return Walk[T]{}, err
		}

		p, err := fetch(ctx, PageRequest{Cursor: cursor, Size: size})
		if err != nil {
			return Walk[T]{}, err
		}
		items = append(items, p.Items...)

		if len(items) > MaxItems {
			return Walk[T]{Items: items, Complete: false}, nil
		}
		if p.Next == "" {
			return Walk[T]{Items: items, Complete: true}, nil
		}
		if p.Next == cursor {
			return Walk[T]{Items: items, Complete: false}, nil
		}
		cursor = p.Next
	}

	return Walk[T]{Items: items, Complete: false}, nil
}
