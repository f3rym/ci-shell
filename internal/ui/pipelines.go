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
	"github.com/f3rym/ci-shell/internal/textwidth"
)

// pipelineDebounce — задержка перед запросом пайплайнов после того, как
// курсор дерева встал на новый проект. Курсор в дереве ходит быстро, и
// запрос на каждое движение — это отказ в обслуживании, устроенный самому
// себе и чужому серверу.
const pipelineDebounce = 250 * time.Millisecond

// pipelinePanel — правая панель: пакет обхода и хост показываемого проекта
// (Фаза 14, план 14-02: приходят вместе с проектом, а не фиксируются при
// сборке — проект может принадлежать любому из корней дерева), таблица
// Bubbles, срез пайплайнов, признаки загрузки/полноты/кэша, время
// получения, текст ошибки, признак фокуса и счётчик поколения запроса
// (защита от дребезга при быстром движении курсора).
type pipelinePanel struct {
	// b, host — клиент обхода и хост проекта, которого показывает панель
	// сейчас (Фаза 14, план 14-02): панель не хранит клиента, зафиксированного
	// при сборке (newPipelinePanel ниже его не принимает) — второй экран
	// одного и того же дерева может открыть проект другого инстанса, и
	// клиент, зафиксированный однажды, спрашивал бы чужой хост ключом
	// первого.
	b    *browse.Client
	host string

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

	// width — ширина, отведённая колонке лентой. Панель обязана в неё
	// уложиться сама: снаружи, из columnView, длины её строк уже не видно,
	// а всё, что шире, Lip Gloss переносит на следующую строку — колонка
	// раздувается по высоте, рамка рвётся, и кадр перестаёт сходиться
	// (обзор v1.0.0, живой замер: тело 109 отдавало строку в 112).
	width int

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

// openPipelineMsg — открытие пайплайна на панели: хост, проект и выбранный
// пайплайн уходят экрану списка джоб единственным сообщением (Фаза 14, план
// 14-02: экран списка джоб собирается корневой моделью и должен знать,
// чьим доступом его собирать).
type openPipelineMsg struct {
	host     string
	project  provider.Project
	pipeline provider.Pipeline
}

// tableCellPadding, tableColumnCount — надбавка, которую table.DefaultStyles()
// (charm.land/bubbles/v2/table, не наш код) назначает Header и Cell:
// Padding(0, 1) — по 1 ячейке слева и справа НА КАЖДУЮ отрисованную колонку
// таблицы. newPipelinePanel ниже эти стили не переопределяет (переопределён
// только Selected), поэтому эта надбавка действует. table.headersView и
// table.renderRow сначала обрезают содержимое ячейки РОВНО до col.Width, а
// ЗАТЕМ оборачивают его этим стилем — итоговая ширина отрисованной строки
// таблицы всегда на tableColumnCount*tableCellPadding ячеек БОЛЬШЕ суммы
// Width всех колонок. pipelineColumns не вычитал эту надбавку (план 17-02,
// задача 3, объявленный «сумма столбцов равна width») — сумма пяти Width
// действительно была равна width, но НАСТОЯЩАЯ отрисованная строка получалась
// шире width на 10 ячеек, и Lip Gloss (App.boxed, internal/ui/app.go)
// переносил переполнившийся хвост строки словом — «когда» уезжало на вторую
// строку (FIT-05, живой обзор после плана 17-02: дефект остался, несмотря на
// объявленное закрытие).
const (
	tableCellPadding = 2
	tableColumnCount = 5
)

// pipelineColumns считает ширины столбцов таблицы пайплайнов от ширины
// колонки width (Фаза 13, план 13-02): символ статуса и номер держат свою
// малую ширину — расширять их от лишней ширины окна незачем, — а весь
// остаток делится между веткой и коммитом. Заголовки колонок таблицы
// выключены — заголовок несёт лента (App.columnView), не таблица.
//
// Сумма symbolWidth+numberWidth+branch+commit+whenWidth+tableColumnCount*
// tableCellPadding (реальная ширина отрисованной строки, а не только сумма
// Width) теперь РОВНО width, а не «не меньше» её, как раньше (Фаза 17,
// FIT-05): нижние пороги ширины «ветка»/«коммит» форсировали остаток вверх
// независимо от реально доступной ширины — при узкой колонке итоговая сумма
// пяти столбцов превышала переданный width, и Lip Gloss переносил
// переполнившийся хвост строки словом, из-за чего «когда» уезжало на вторую
// строку. FIT-05 требует, чтобы ужималось СОДЕРЖИМОЕ столбцов, а не ломался
// ряд — урезание branch/commit вплоть до нуля вместо навязанного минимума
// это и делает.
func pipelineColumns(width int) []table.Column {
	const (
		symbolWidth = 1
		numberWidth = 8
		whenWidth   = 12
	)
	// Доступная ширина за вычетом отступов, которые таблица Bubbles
	// добавляет каждому столбцу сама.
	avail := width - tableColumnCount*tableCellPadding
	if avail < 0 {
		avail = 0
	}

	// Обнуления одной лишь доли «ветка»/«коммит» мало: при колонке уже
	// несжимаемой части (символ, номер и «когда» — 31 ячейка вместе с
	// отступами) сумма фиксированных ширин всё равно превышала отведённое,
	// и Lip Gloss переносил «когда» на вторую строку, разрывая шапку и ряды
	// (обзор v1.0.0, CR-03). Поэтому ужимаются и фиксированные столбцы, в
	// порядке убывания того, без чего строка ещё читается: сначала «когда»,
	// затем номер; символ статуса не отдаётся до последнего — без него ряд
	// перестаёт отвечать на главный вопрос «упало или прошло».
	symbol, number, when := symbolWidth, numberWidth, whenWidth
	if symbol+number+when > avail {
		when = max(0, avail-symbol-number)
		if when == 0 {
			number = max(0, avail-symbol)
			if number == 0 {
				symbol = max(0, avail)
			}
		}
	}

	rest := avail - symbol - number - when
	if rest < 0 {
		rest = 0
	}
	branch := rest * 2 / 3
	commit := rest - branch
	return []table.Column{
		{Title: "", Width: symbol},
		{Title: "#", Width: number},
		{Title: "ветка", Width: branch},
		{Title: "коммит", Width: commit},
		{Title: "когда", Width: when},
	}
}

// naturalWidth — часть общего контракта колонки (Фаза 17, FIT-02): символ
// статуса, номер и «когда» держат ту же фиксированную ширину, что и
// pipelineColumns; «ветка» — по самой длинной строке уже загруженных
// пайплайнов (не меньше branchFloor); «коммит» сокращается до 8 символов
// всегда (rows()), второй меры для него не заводится — это ровно то
// содержимое, ради которого пайплайны в примере пользователя должны
// получить БОЛЬШЕ ширины, чем короткое дерево слева. branchFloor/commitWidth
// — желаемая ширина этой колонки САМОЙ ПО СЕБЕ (сколько ей хотелось бы),
// а не навязанный пол суммы столбцов при недостатке места — тот пол убран
// из pipelineColumns задачей 3 плана 17-02 (FIT-05), и константы здесь
// намеренно переименованы, чтобы не путать эти два разных смысла.
func (p pipelinePanel) naturalWidth() int {
	const (
		symbolWidth = 1
		numberWidth = 8
		whenWidth   = 12
		branchFloor = 12
		commitWidth = 8
	)
	branch := branchFloor
	for _, pl := range p.pipelines {
		if w := textwidth.Of(pl.Ref); w > branch {
			branch = w
		}
	}
	return symbolWidth + numberWidth + branch + commitWidth + whenWidth
}

// newPipelinePanel собирает таблицу Bubbles: символ статуса, номер, ветка,
// сокращённый коммит, относительное время. Ширина столбцов — временная,
// первый setSize (ниже) пересчитает её от настоящей ширины колонки. Клиент
// обхода панель больше не принимает при сборке (Фаза 14, план 14-02) — он
// приходит вместе с каждым проектом (setProject ниже).
func newPipelinePanel(theme Theme) pipelinePanel {
	t := table.New(table.WithColumns(pipelineColumns(ColumnMin)), table.WithFocused(false))
	styles := table.DefaultStyles()
	// Стиль выбранной строки — инверсия видео (Reverse(true), тот же приём,
	// что и Theme.Selected), как требует контракт; собственного цвета
	// выделения не задаётся.
	styles.Selected = theme.Selected.Reverse(true)
	t.SetStyles(styles)

	return pipelinePanel{table: t, theme: theme}
}

// setProject меняет показываемый проект вместе с хостом и клиентом обхода,
// которому он принадлежит (Фаза 14, план 14-02): уже загруженный проект того
// же хоста остаётся из памяти без нового запроса; новый проект запускает
// дребезг задержкой pipelineDebounce.
func (p pipelinePanel) setProject(host string, b *browse.Client, project provider.Project) (pipelinePanel, tea.Cmd) {
	if p.hasProject && p.host == host && p.project.FullPath == project.FullPath {
		return p, nil
	}
	p.host = host
	p.b = b
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
// завершилась, игнорируется; без клиента (см. pipelineDebounceMsg ниже)
// запрос тоже не уходит — путь недостижим (см. ту же ветвь), но защита
// стоит и здесь на случай зажатой клавиши между вкладками.
func (p pipelinePanel) refresh() (pipelinePanel, tea.Cmd) {
	if !p.hasProject || p.loading || p.b == nil {
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
		if p.b == nil {
			// Путь недостижим: проект приходит только из уже загруженного
			// узла дерева, а узел загружается только когда доступ к его
			// хосту уже есть (needHostMsg/hostReadyMsg, internal/ui/tree.go,
			// internal/ui/app.go). Ветвь — защита инварианта, а не логика:
			// без неё погасший клиент привёл бы к панике на b.Pipelines
			// вместо честной строки отказа с именем хоста.
			p.loadErr = fmt.Sprintf("нет доступа к хосту %s", p.host)
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

// setSize передаёт таблице ширину и высоту КОЛОНКИ (Фаза 13, план 13-02) и
// пересчитывает от неё ширины столбцов (pipelineColumns) — растяжение
// таблицы при большем окне живёт здесь, а не в отрисовке (idea-0.3.1 §3,
// «панели становятся крупнее, а не оставляют пустоту справа»).
func (p pipelinePanel) setSize(width, height int) pipelinePanel {
	p.width = width
	p.table.SetWidth(width)
	p.table.SetHeight(height)
	p.table.SetColumns(pipelineColumns(width))
	return p
}

// view отрисовывает тело колонки без заголовка (Фаза 13, план 13-02):
// заголовок с именем проекта рисует корневая модель уточнением заголовка
// колонки (App.updateInner, projectPickedMsg) — второй заголовок здесь был
// бы мусором.
func (p pipelinePanel) view() string {
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

	parts := []string{body}
	if p.hasProject && !p.complete {
		parts = append(parts, p.theme.Muted.Render(fmt.Sprintf("показаны первые %d записей — список длиннее", len(p.pipelines))))
	}
	if p.hasProject && p.cached {
		parts = append(parts, p.theme.Muted.Render(fmt.Sprintf("из кэша, %s назад", Ago(p.fetched))))
	}

	// Последний рубеж: каждая строка обрезается по ширине панели. Расчёт
	// столбцов выше уже вычитает надбавку таблицы, но живой замер показал,
	// что отрисованная шапка всё равно оказывается шире тела колонки, а
	// строки-хвосты («показаны первые», «из кэша») ширины не знали вовсе.
	// Источник лишних ячеек внутри таблицы Bubbles не установлен — поэтому
	// здесь стоит не расчёт, а именно ограничение: панель не выпускает
	// наружу ничего шире того, что ей отвели.
	out := strings.Join(parts, "\n")
	if p.width > 0 {
		lines := strings.Split(out, "\n")
		for i, ln := range lines {
			lines[i] = Fit(ln, p.width)
		}
		out = strings.Join(lines, "\n")
	}
	return out
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

// cursorAt — часть общего контракта колонки (Фаза 13, план 13-03): номер
// выбранной строки таблицы и номер пайплайна под курсором строкой — номер
// пайплайна уникален в пределах проекта, а колонка всегда про один проект.
// Пустая либо ещё не выбравшая проект панель возвращает нулевые значения.
func (p pipelinePanel) cursorAt() (int, string) {
	if pl, ok := p.selected(); ok {
		return p.table.Cursor(), fmt.Sprintf("%d", pl.IID)
	}
	return 0, ""
}

// withCursor — часть общего контракта колонки (Фаза 13, план 13-03): поиск
// пайплайна с номером key среди загруженных, установка курсора таблицы на
// его позицию; не найден — курсор встаёт на index, прижатый к границам
// списка. Тот же порядок «сначала ключ, затем прижатый номер», что и у
// остальных трёх моделей колонок.
func (p pipelinePanel) withCursor(index int, key string) pipelinePanel {
	if len(p.pipelines) == 0 {
		p.table.SetCursor(0)
		return p
	}
	if key != "" {
		for i, pl := range p.pipelines {
			if fmt.Sprintf("%d", pl.IID) == key {
				p.table.SetCursor(i)
				return p
			}
		}
	}
	idx := index
	if idx < 0 {
		idx = 0
	} else if idx >= len(p.pipelines) {
		idx = len(p.pipelines) - 1
	}
	p.table.SetCursor(idx)
	return p
}

// openDeeper — часть общего контракта колонки (Фаза 13): обёртка над open()
// ниже, чтобы имя вызова было одним на все колонки. Существующее открытие
// пайплайна под курсором остаётся единственной реализацией — второй не
// заводится.
func (p pipelinePanel) openDeeper() (pipelinePanel, tea.Cmd) {
	return p, p.open()
}

// open отдаёт openPipelineMsg для пайплайна под курсором, если он есть, вместе
// с хостом, которому принадлежит проект (Фаза 14, план 14-02).
func (p pipelinePanel) open() tea.Cmd {
	pl, ok := p.selected()
	if !ok {
		return nil
	}
	host, project := p.host, p.project
	return func() tea.Msg { return openPipelineMsg{host: host, project: project, pipeline: pl} }
}
