// pipelines.go — правая панель экрана дерева (Фаза 10, BROW-03): последние
// пайплайны проекта под курсором и переход к списку джоб выбранного
// пайплайна. Не отдельный экран: мокап idea-0.3.0 §1 держит дерево и список
// в одном кадре, и уводить человека на отдельный экран ради четырёх строк
// значило бы терять из виду то, откуда он пришёл.
package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/table"

	"github.com/f3rym/ci-shell/internal/browse"
	"github.com/f3rym/ci-shell/internal/provider"
)

// pipelineDebounce — задержка перед запросом пайплайнов после того, как
// курсор дерева встал на новый проект. Курсор в дереве ходит быстро, и
// запрос на каждое движение — это отказ в обслуживании, устроенный самому
// себе и чужому серверу.
const pipelineDebounce = 250 * time.Millisecond

// pipelinePanel — правая панель: пакет обхода, показываемый проект, таблица
// Bubbles, срез пайплайнов, признаки загрузки/полноты/кэша, время
// получения, текст ошибки, признак фокуса и счётчик поколения запроса
// (защита от дребезга при быстром движении курсора).
type pipelinePanel struct {
	b *browse.Client

	project    provider.Project
	hasProject bool

	table     table.Model
	pipelines []provider.Pipeline

	loading  bool
	loadErr  string
	complete bool
	cached   bool
	fetched  time.Time

	focused    bool
	generation int

	theme Theme
}

// Сообщения правой панели.
type pipelinesLoadedMsg struct {
	projectPath string
	generation  int
	pipelines   []provider.Pipeline
	complete    bool
	cached      bool
	fetched     time.Time
}
type pipelinesFailedMsg struct {
	projectPath string
	generation  int
	reason      string
}
type pipelineDebounceMsg struct {
	projectPath string
	generation  int
}

// openPipelineMsg — открытие пайплайна на панели: проект и выбранный
// пайплайн уходят экрану списка джоб единственным сообщением.
type openPipelineMsg struct {
	project  provider.Project
	pipeline provider.Pipeline
}

// newPipelinePanel собирает таблицу Bubbles: символ статуса, номер, ветка,
// сокращённый коммит, относительное время. Заголовки колонок таблицы
// выключены — заголовок несёт панель (panelTitle ниже), не таблица.
func newPipelinePanel(b *browse.Client, theme Theme) pipelinePanel {
	cols := []table.Column{
		{Title: "", Width: 1},
		{Title: "#", Width: 8},
		{Title: "ветка", Width: 16},
		{Title: "коммит", Width: 8},
		{Title: "когда", Width: 12},
	}
	t := table.New(table.WithColumns(cols), table.WithFocused(false))
	styles := table.DefaultStyles()
	// Стиль выбранной строки — инверсия видео (Reverse(true), тот же приём,
	// что и Theme.Selected), как требует контракт; собственного цвета
	// выделения не задаётся.
	styles.Selected = theme.Selected.Reverse(true)
	t.SetStyles(styles)

	return pipelinePanel{b: b, table: t, theme: theme}
}

// setProject меняет показываемый проект: уже загруженный проект остаётся из
// памяти без нового запроса; новый проект запускает дребезг задержкой
// pipelineDebounce.
func (p pipelinePanel) setProject(project provider.Project) (pipelinePanel, tea.Cmd) {
	if p.hasProject && p.project.FullPath == project.FullPath {
		return p, nil
	}
	p.project = project
	p.hasProject = true
	p.loadErr = ""
	p.generation++
	gen, path := p.generation, project.FullPath
	return p, tea.Tick(pipelineDebounce, func(time.Time) tea.Msg {
		return pipelineDebounceMsg{projectPath: path, generation: gen}
	})
}

// fetch запрашивает пайплайны проекта через пакет обхода — постранично и с
// кэшем.
func (p pipelinePanel) fetch(gen int, refresh bool) tea.Cmd {
	b, path := p.b, p.project.FullPath
	return func() tea.Msg {
		res, err := b.Pipelines(context.Background(), path, refresh)
		if err != nil {
			return pipelinesFailedMsg{projectPath: path, generation: gen, reason: err.Error()}
		}
		return pipelinesLoadedMsg{
			projectPath: path, generation: gen,
			pipelines: res.Items, complete: res.Complete, cached: res.Cached, fetched: res.FetchedAt,
		}
	}
}

// refresh перезапрашивает пайплайны показанного проекта, минуя кэш —
// клавиша обновления. Повторное обновление, пока предыдущая загрузка не
// завершилась, игнорируется.
func (p pipelinePanel) refresh() (pipelinePanel, tea.Cmd) {
	if !p.hasProject || p.loading {
		return p, nil
	}
	p.generation++
	p.loading = true
	return p, p.fetch(p.generation, true)
}

func (p pipelinePanel) update(msg tea.Msg) (pipelinePanel, tea.Cmd) {
	switch msg := msg.(type) {
	case pipelineDebounceMsg:
		if msg.generation != p.generation {
			return p, nil
		}
		p.loading = true
		return p, p.fetch(msg.generation, false)
	case pipelinesLoadedMsg:
		if msg.generation != p.generation {
			return p, nil
		}
		p.loading = false
		p.loadErr = ""
		p.pipelines = msg.pipelines
		p.complete = msg.complete
		p.cached = msg.cached
		p.fetched = msg.fetched
		p.table.SetRows(p.rows())
		return p, nil
	case pipelinesFailedMsg:
		if msg.generation != p.generation {
			return p, nil
		}
		p.loading = false
		p.loadErr = msg.reason
		return p, nil
	case tea.KeyPressMsg:
		if !p.focused {
			return p, nil
		}
		var cmd tea.Cmd
		p.table, cmd = p.table.Update(msg)
		return p, cmd
	}
	return p, nil
}

// rows строит строки таблицы: символ статуса из единственной таблицы
// соответствия (статус пайплайна размечается теми же словами, что и статус
// джобы — отдельной таблицы для него не заводится), стилем StatusStyle того
// же статуса.
func (p pipelinePanel) rows() []table.Row {
	rows := make([]table.Row, 0, len(p.pipelines))
	for _, pl := range p.pipelines {
		sha := pl.SHA
		if len(sha) > 8 {
			sha = sha[:8]
		}
		glyph := p.theme.StatusStyle(pl.Status).Render(StatusGlyph(pl.Status))
		rows = append(rows, table.Row{glyph, fmt.Sprintf("#%d", pl.IID), pl.Ref, sha, p.theme.Muted.Render(Ago(pl.UpdatedAt))})
	}
	return rows
}

// selected возвращает пайплайн под курсором таблицы.
func (p pipelinePanel) selected() (provider.Pipeline, bool) {
	i := p.table.Cursor()
	if i < 0 || i >= len(p.pipelines) {
		return provider.Pipeline{}, false
	}
	return p.pipelines[i], true
}

// focus/blur переключают активность таблицы. Без фокуса панель рисуется
// теми же строками, но без выделения — человек должен видеть, где он
// находится.
func (p pipelinePanel) focus() pipelinePanel {
	p.focused = true
	p.table.Focus()
	return p
}
func (p pipelinePanel) blur() pipelinePanel {
	p.focused = false
	p.table.Blur()
	return p
}

// setSize передаёт таблице ширину и высоту панели; ширины колонок остаются
// фиксированными (заданы в newPipelinePanel).
func (p pipelinePanel) setSize(width, height int) pipelinePanel {
	p.table.SetWidth(width)
	p.table.SetHeight(height)
	return p
}

// view отрисовывает панель: заголовок всегда, тело — по состоянию.
func (p pipelinePanel) view(width int) string {
	title := p.theme.PanelTitle.Render(fmt.Sprintf("ПАЙПЛАЙНЫ %s", Truncate(p.projectTitle(), width)))

	var body string
	switch {
	case !p.hasProject:
		body = "выберите проект слева"
	case p.loadErr != "":
		body = fmt.Sprintf("не удалось получить пайплайны: %s", p.loadErr)
	case p.loading:
		body = "тяну пайплайны…"
	case len(p.pipelines) == 0:
		body = "в проекте ещё не было пайплайнов"
	default:
		body = p.table.View()
	}

	parts := []string{title, body}
	if p.hasProject && !p.complete {
		parts = append(parts, p.theme.Muted.Render(fmt.Sprintf("показаны первые %d записей — список длиннее", len(p.pipelines))))
	}
	if p.hasProject && p.cached {
		parts = append(parts, p.theme.Muted.Render(fmt.Sprintf("из кэша, %s назад", Ago(p.fetched))))
	}
	return strings.Join(parts, "\n")
}

func (p pipelinePanel) projectTitle() string {
	if !p.hasProject {
		return ""
	}
	return p.project.FullPath
}

// hintText выбирает строку подсказки правой панели ровно по её пяти
// состояниям.
func (p pipelinePanel) hintText() string {
	switch {
	case !p.hasProject:
		return HintPipelinesPick()
	case p.loadErr != "":
		return HintPipelinesFailed(p.loadErr)
	case p.loading:
		return HintPipelinesLoading(p.project.FullPath)
	case len(p.pipelines) == 0:
		return HintPipelinesEmpty(p.project.FullPath)
	}
	if pl, ok := p.selected(); ok {
		return HintPipelineOpen(pl.IID)
	}
	return HintPipelinesPick()
}

// open отдаёт openPipelineMsg для пайплайна под курсором, если он есть.
func (p pipelinePanel) open() tea.Cmd {
	pl, ok := p.selected()
	if !ok {
		return nil
	}
	project := p.project
	return func() tea.Msg { return openPipelineMsg{project: project, pipeline: pl} }
}
