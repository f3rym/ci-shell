// tree.go — экран дерева групп и проектов (Фаза 10, BROW-01): раскрытие,
// фильтр, две панели (дерево слева, пайплайны выбранного проекта справа),
// переключение фокуса между ними. Единственный источник данных — пакет
// обхода (internal/browse): экран не ходит в сеть сам и не знает ни про
// страницы, ни про сроки годности кэша.
package ui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"

	"github.com/f3rym/ci-shell/internal/browse"
	"github.com/f3rym/ci-shell/internal/provider"
	"github.com/f3rym/ci-shell/internal/textwidth"
)

// treeKind различает вид строки дерева: группа, личное пространство
// человека или проект. Личное пространство — отдельный вид, а не группа с
// тем же именем: у него другой адрес в API (Namespace{Kind: NamespaceUser})
// и другой смысл, и подмена одного другим рано или поздно показала бы чужие
// проекты.
type treeKind int

const (
	treeKindGroup treeKind = iota
	treeKindUser
	treeKindProject
	// treeKindNote — не узел дерева, а строка-объяснение под раскрытым
	// узлом: «источник вернул ноль записей» либо «источник ответил отказом»
	// с причиной и адресом запроса. До неё обе ситуации выглядели на экране
	// одинаково — раскрытый узел, под которым пусто, — и отличить пустое
	// пространство имён от отказа API было нечем. Строка не открывается (⏎
	// на ней не делает ничего) и не участвует в фильтре; обновление (g) на
	// ней повторяет запрос узла, под которым она стоит.
	treeKindNote
)

// treeRow — одна строка дерева: вид, глубина вложенности, отображаемое имя,
// полный путь, пространство имён (для группы/личного пространства — чем
// спрашивать его проекты), сама доменная группа/проект и признаки
// раскрытости/загрузки детей.
type treeRow struct {
	Kind     treeKind
	Depth    int
	Name     string
	FullPath string
	NS       provider.Namespace

	Group   provider.Group
	Project provider.Project

	Expanded    bool
	ChildrenSet bool
}

// FilterValue отдаёт полный путь: человек ищет по пути, а не по одному
// сегменту. У строки-объяснения пути нет вовсе — она отдаёт пустое значение
// и потому не всплывает в отфильтрованном списке как «найденный проект».
func (r treeRow) FilterValue() string {
	if r.Kind == treeKindNote {
		return ""
	}
	return r.FullPath
}

// noteRow собирает строку-объяснение под узлом owner — единственное место
// сборки такой строки в экране. Путь узла строка носит с собой не для показа
// (рисуется только Name), а чтобы обновление на ней (g) повторяло запрос
// ТОГО узла, про который она говорит, а не корня дерева.
func noteRow(owner treeRow, text string) treeRow {
	return treeRow{Kind: treeKindNote, Depth: owner.Depth + 1, Name: text, FullPath: owner.FullPath}
}

// treeDelegate — отрисовщик строки дерева для компонента списка Bubbles:
// одна высота, без разделителя между строками, обновление ничего не делает.
// focused — указатель, а не голое поле: список Bubbles не даёт способа
// подменить делегата на лету без пересборки компонента, и указатель на
// общий с treeModel признак фокуса (Фаза 13, план 13-02) позволяет
// treeModel.columnView перед каждой отрисовкой сказать делегату, видна ли
// сейчас ЭТА колонка (репозитории) в фокусе ленты, — без этого список
// рисовал бы инверсию курсора даже тогда, когда фокус ленты стоит на
// колонке пайплайнов правее, и на экране был бы курсор сразу в двух местах.
type treeDelegate struct {
	theme   Theme
	focused *bool
}

func (d treeDelegate) isFocused() bool {
	return d.focused != nil && *d.focused
}

func (d treeDelegate) Height() int                       { return 1 }
func (d treeDelegate) Spacing() int                       { return 0 }
func (d treeDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (d treeDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	row, ok := item.(treeRow)
	if !ok {
		return
	}

	indent := strings.Repeat(" ", row.Depth*TreeIndent)
	selected := index == m.Index() && d.isFocused()

	// Строка-объяснение рисуется без символа раскрытия и приглушённым: она
	// ничего не открывает, и выглядеть как узел дерева не должна. Курсор на
	// ней остаётся видимым (инверсия строки), но символа курсора она не
	// получает — открывать здесь нечего.
	if row.Kind == treeKindNote {
		width := m.Width() - textwidth.Of(indent) - 2
		line := indent + Truncate(row.Name, width)
		if selected {
			fmt.Fprint(w, d.theme.Selected.Render(line))
			return
		}
		fmt.Fprint(w, d.theme.Muted.Render(line))
		return
	}

	var glyph string
	switch row.Kind {
	case treeKindProject:
		glyph = strings.Repeat(" ", textwidth.Of(GlyphCollapsed))
	default:
		if row.Expanded {
			glyph = GlyphExpanded
		} else {
			glyph = GlyphCollapsed
		}
	}

	width := m.Width() - textwidth.Of(indent) - textwidth.Of(glyph) - RowIconGap - 2
	name := Truncate(row.Name, width)
	line := indent + glyph + strings.Repeat(" ", RowIconGap) + name

	if selected {
		line = d.theme.Selected.Render(line + " " + GlyphCursor)
	} else {
		line = d.theme.Text.Render(line)
	}
	fmt.Fprint(w, line)
}

// treeModel — экран дерева: пакет обхода, хост, пользователь, компонент
// списка Bubbles, корневые строки, дети по полному пути родителя, признаки
// полноты/кэша/времени по тому же ключу, правая панель (пайплайны) и фокус.
type treeModel struct {
	b    *browse.Client
	host string
	user provider.User

	list list.Model

	roots    []treeRow
	children map[string][]treeRow

	complete  map[string]bool
	cached    map[string]bool
	fetchedAt map[string]time.Time

	// loadErr — отказ загрузки КОРНЯ: только он способен оставить экран без
	// дерева вовсе, и только он замещает тело панели.
	loadErr string
	// nodeErr — отказ загрузки конкретного узла по его ключу: причина вместе
	// с адресом запроса, пришедшая от провайдера. Отказ узла больше не
	// стирает дерево с экрана — он показывается строкой под самим узлом
	// (rebuild ниже), потому что «этот узел не открылся» и «списка нет» —
	// разные события, и одинаково выглядеть они не должны.
	nodeErr map[string]string
	loading bool
	// pending — ключ узла (пустая строка — корень), для которого сейчас
	// идёт загрузка; повторное обновление того же узла, пока она не
	// завершилась, игнорируется.
	pending map[string]bool

	runs  pipelinePanel
	focus treeFocus

	// listFocused — тот же указатель, что держит treeDelegate списка
	// (Фаза 13, план 13-02): columnView выставляет его перед каждой
	// отрисовкой колонки репозиториев в значение переданного ей focused.
	listFocused *bool

	height int

	theme Theme
	keys  KeyMap
}

type treeFocus int

const (
	focusTree treeFocus = iota
	focusRuns
)

// Сообщения экрана дерева.
type treeLoadedMsg struct {
	parent   string
	rows     []treeRow
	complete bool
	cached   bool
	fetched  time.Time
}
// treeFailedMsg — узел parent не загрузился. reason приходит уже очищенным
// (provider.SafeText): текст ошибки провайдера несёт кусок тела ответа
// чужого сервера, а отсюда он уходит и в строку дерева, и в строку
// подсказки — управляющая последовательность в нём перерисовала бы кадр
// (T-10-03).
type treeFailedMsg struct {
	parent string
	reason string
}

// projectPickedMsg — проект под курсором дерева: правая панель пайплайнов
// слушает это сообщение и решает сама, запрашивать ли пайплайны заново
// (дребезг, «уже показан» — internal/ui/pipelines.go, setProject), а
// корневая модель — держать ли в ленте колонку пайплайнов (Фаза 13,
// internal/ui/app.go).
type projectPickedMsg struct {
	project provider.Project
}

// newTreeModel собирает экран дерева: компонент списка Bubbles с включённой
// фильтрацией и выключенными собственными заголовком, строкой состояния и
// справкой — второй заголовок внутри панели был бы мусором, кадр собирает
// корневая модель.
func newTreeModel(b *browse.Client, host string, user provider.User, theme Theme, keys KeyMap) treeModel {
	focused := new(bool)
	l := list.New(nil, treeDelegate{theme: theme, focused: focused}, 0, 0)
	l.SetFilteringEnabled(true)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)

	return treeModel{
		b: b, host: host, user: user,
		list:        l,
		listFocused: focused,
		children:    map[string][]treeRow{},
		complete:    map[string]bool{},
		cached:      map[string]bool{},
		fetchedAt:   map[string]time.Time{},
		pending:     map[string]bool{},
		nodeErr:     map[string]string{},
		runs:        newPipelinePanel(b, theme),
		theme:       theme,
		keys:        keys,
	}
}

// setSize передаёт моделям обеих колонок дерева (репозитории, пайплайны)
// ширину ИХ колонки, а не долю всей ширины кадра, — ширину каждой посчитала
// лента (Фаза 13, план 13-02, App.columnWidthFor), второго расчёта здесь не
// заводится. Высота считается прежним способом.
func (m treeModel) setSize(reposWidth, pipelinesWidth, height int) treeModel {
	m.height = height
	bodyHeight := height - 8
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	m.list.SetSize(reposWidth, bodyHeight)
	m.runs = m.runs.setSize(pipelinesWidth, bodyHeight)
	return m
}

// setColumnFocus — часть общего контракта колонки (Фаза 13): колонка
// репозиториев ставит внутренний фокус на список и снимает фокус с панели
// пайплайнов, колонка пайплайнов — наоборот. Постановка и снятие фокуса у
// самой панели уже есть (pipelinePanel.focus/blur) — вторых здесь не
// заводится. Внутренний признак фокуса остаётся полем модели: он нужен
// отрисовке и выбору строки подсказки, но меняется теперь только этим
// методом — фокус раздаёт лента, а не экран сам себе.
func (m treeModel) setColumnFocus(id columnID) treeModel {
	if id == colPipelines {
		m.focus = focusRuns
		m.runs = m.runs.focus()
		return m
	}
	m.focus = focusTree
	m.runs = m.runs.blur()
	return m
}

// loadRoot собирает корень дерева из двух источников: личное пространство
// человека первым (мокап idea-0.3.0 §1) и группы верхнего уровня, видимые
// токену.
func (m treeModel) loadRoot(refresh bool) tea.Cmd {
	b, user := m.b, m.user
	return func() tea.Msg {
		ctx := context.Background()

		userRow := treeRow{
			Kind: treeKindUser, Name: user.Name, FullPath: user.Username,
			NS: provider.Namespace{Kind: provider.NamespaceUser, Path: user.Username},
		}
		rows := []treeRow{userRow}

		groups, err := b.Groups(ctx, "", refresh)
		if err != nil {
			return treeFailedMsg{parent: "", reason: provider.SafeText(err.Error())}
		}
		for _, g := range groups.Items {
			rows = append(rows, treeRow{
				Kind: treeKindGroup, Name: g.Name, FullPath: g.FullPath, Group: g,
				NS: provider.Namespace{Kind: provider.NamespaceGroup, Path: g.FullPath},
			})
		}

		return treeLoadedMsg{parent: "", rows: rows, complete: groups.Complete, cached: groups.Cached, fetched: groups.FetchedAt}
	}
}

// loadChildren запрашивает детей группы или личного пространства: сначала
// подгруппы (только для групп — у личного пространства их нет), затем
// проекты, без включения проектов подгрупп — подгруппы уже отдельные узлы
// дерева.
func (m treeModel) loadChildren(row treeRow, refresh bool) tea.Cmd {
	b := m.b
	depth := row.Depth + 1
	return func() tea.Msg {
		ctx := context.Background()
		parent := row.FullPath

		var rows []treeRow
		complete := true
		var cached bool
		fetched := time.Now()

		if row.Kind == treeKindGroup {
			sub, err := b.Groups(ctx, row.FullPath, refresh)
			if err != nil {
				return treeFailedMsg{parent: parent, reason: provider.SafeText(err.Error())}
			}
			for _, g := range sub.Items {
				rows = append(rows, treeRow{
					Kind: treeKindGroup, Depth: depth, Name: g.Name, FullPath: g.FullPath, Group: g,
					NS: provider.Namespace{Kind: provider.NamespaceGroup, Path: g.FullPath},
				})
			}
			complete = complete && sub.Complete
			cached = cached || sub.Cached
			fetched = sub.FetchedAt
		}

		pr, err := b.Projects(ctx, row.NS, refresh)
		if err != nil {
			return treeFailedMsg{parent: parent, reason: provider.SafeText(err.Error())}
		}
		for _, p := range pr.Items {
			rows = append(rows, treeRow{Kind: treeKindProject, Depth: depth, Name: p.Name, FullPath: p.FullPath, Project: p})
		}
		complete = complete && pr.Complete
		cached = cached || pr.Cached
		if pr.FetchedAt.After(fetched) {
			fetched = pr.FetchedAt
		}

		return treeLoadedMsg{parent: parent, rows: rows, complete: complete, cached: cached, fetched: fetched}
	}
}

func (m treeModel) update(msg tea.Msg) (treeModel, tea.Cmd) {
	switch msg := msg.(type) {
	case treeLoadedMsg:
		m.loading = false
		delete(m.pending, msg.parent)
		delete(m.nodeErr, msg.parent)
		m.complete[msg.parent] = msg.complete
		m.cached[msg.parent] = msg.cached
		m.fetchedAt[msg.parent] = msg.fetched
		if msg.parent == "" {
			m.loadErr = ""
			m.roots = msg.rows
		} else {
			// Запись заводится и для пустого ответа: «узел загружен, детей
			// ноль» и «узел ещё не загружен» — разные состояния, и различает
			// их именно наличие записи (rebuild ниже читает его же).
			m.children[msg.parent] = msg.rows
			m.setExpanded(msg.parent, true)
		}
		m.rebuild()
		return m, nil

	case treeFailedMsg:
		m.loading = false
		delete(m.pending, msg.parent)
		m.nodeErr[msg.parent] = msg.reason
		if msg.parent == "" {
			// Отказ корня — дерева нет вовсе, показывать причину больше негде,
			// кроме тела панели.
			m.loadErr = msg.reason
		} else {
			// Отказ узла раскрывает узел, чтобы причина встала строкой прямо
			// под ним. Дети при этом НЕ появляются (ChildrenSet остаётся
			// ложным, пока в children нет записи), поэтому повторное ⏎ на узле
			// пробует загрузку заново, а не «сворачивает пустоту».
			m.setExpanded(msg.parent, true)
		}
		m.rebuild()
		return m, nil

	case pipelineDebounceMsg, pipelinesLoadedMsg, pipelinesFailedMsg:
		var cmd tea.Cmd
		m.runs, cmd = m.runs.update(msg)
		return m, cmd

	case projectPickedMsg:
		var cmd tea.Cmd
		m.runs, cmd = m.runs.setProject(msg.project)
		return m, cmd

	case tea.PasteMsg:
		// Вставка из буфера приходит отдельным сообщением, а не клавишей
		// (скобочная вставка Bubble Tea v2) — та же ветвь, что и у поля
		// ввода заставки. Осмысленна она здесь ровно в одном состоянии: при
		// открытом фильтре списка. Компонент списка Bubbles передаёт вставку
		// своему полю фильтра сам (handleFiltering), поэтому вставлять текст
		// в значение фильтра руками не нужно.
		if m.focus == focusTree && m.list.FilterState() == list.Filtering {
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return m, cmd
		}
		return m, nil

	case tea.KeyPressMsg:
		if m.focus == focusTree && m.list.FilterState() == list.Filtering {
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return m, cmd
		}

		// Клавиши возврата и открытия (esc, ⏎, →, ←) до модели больше не
		// доходят — их забирает закон ленты в корневой модели
		// (internal/ui/app.go, navFor/openDeeper): второе толкование стрелки
		// здесь было бы ровно той поломкой, которую фаза чинит. Ветвь «фокус
		// на правой панели» остаётся, но открытия больше не содержит — она
		// передаёт панели клавиши движения и обновления.
		switch {
		case m.focus == focusRuns:
			if key.Matches(msg, m.keys.Refresh) {
				var cmd tea.Cmd
				m.runs, cmd = m.runs.refresh()
				return m, cmd
			}
			var cmd tea.Cmd
			m.runs, cmd = m.runs.update(msg)
			return m, cmd

		case key.Matches(msg, m.keys.Refresh):
			return m.refreshCursor()
		}

		var navCmd tea.Cmd
		m.list, navCmd = m.list.Update(msg)
		var pickCmd tea.Cmd
		m, pickCmd = m.onCursorMoved()
		return m, tea.Batch(navCmd, pickCmd)
	}
	return m, nil
}

// openDeeper — открыть то, что под курсором дерева (колонка репозиториев,
// часть общего контракта колонки, Фаза 13): на строке-объяснении ничего не
// происходит; на группе или личном пространстве переключается раскрытость
// и, если дети ещё не загружены, запускается их загрузка — раскрытие ветки
// это углубление ВНУТРИ колонки репозиториев, а не переход в колонку
// правее; на проекте отдаётся проект правой панели сообщением выбранного
// проекта — корневая модель превратит его в открытие колонки пайплайнов.
// Постановка фокуса на правую панель отсюда убрана: фокус раздаёт лента
// (setColumnFocus выше).
func (m treeModel) openDeeper() (treeModel, tea.Cmd) {
	row, ok := m.selected()
	if !ok {
		return m, nil
	}
	// Строка-объяснение ничего не открывает. Без этой проверки ⏎ на ней
	// уходил бы загружать узел с пустым путём — то есть корень — и стирал бы
	// дерево отказом разбора пустого пространства имён.
	if row.Kind == treeKindNote {
		return m, nil
	}
	if row.Kind == treeKindProject {
		project := row.Project
		return m, func() tea.Msg { return projectPickedMsg{project: project} }
	}

	if !row.ChildrenSet {
		if m.pending[row.FullPath] {
			return m, nil
		}
		m.pending[row.FullPath] = true
		m.loading = true
		return m, m.loadChildren(row, false)
	}

	m.setExpanded(row.FullPath, !row.Expanded)
	m.rebuild()
	return m, nil
}

// refreshCursor — обновление узла под курсором, минуя кэш; зажатая клавиша
// не превращается в очередь запросов.
func (m treeModel) refreshCursor() (treeModel, tea.Cmd) {
	row, ok := m.selected()
	// Строка-объяснение узлом не является: обновление на ней повторяет
	// запрос того узла, под которым она стоит (путь узла она носит с собой).
	// Не найден — обновляется корень; узлом с пустым путём строка-объяснение
	// не притворяется никогда.
	if ok && row.Kind == treeKindNote {
		row, ok = m.findRow(row.FullPath)
	}
	nodeKey := ""
	if ok {
		nodeKey = row.FullPath
	}
	if m.pending[nodeKey] {
		return m, nil
	}
	m.pending[nodeKey] = true
	m.loading = true
	if !ok || nodeKey == "" {
		return m, m.loadRoot(true)
	}
	return m, m.loadChildren(row, true)
}

// findRow ищет узел дерева по полному пути среди корней и уже загруженных
// детей. Строки-объяснения из поиска исключены намеренно: они носят путь
// СВОЕГО узла, и без исключения поиск нашёл бы саму строку-объяснение вместо
// узла, про который она говорит.
func (m treeModel) findRow(path string) (treeRow, bool) {
	if path == "" {
		return treeRow{}, false
	}
	for _, r := range m.roots {
		if r.Kind != treeKindNote && r.FullPath == path {
			return r, true
		}
	}
	for _, rows := range m.children {
		for _, r := range rows {
			if r.Kind != treeKindNote && r.FullPath == path {
				return r, true
			}
		}
	}
	return treeRow{}, false
}

func (m treeModel) selected() (treeRow, bool) {
	item := m.list.SelectedItem()
	row, ok := item.(treeRow)
	return row, ok
}

// onCursorMoved отдаёт проект под курсором правой панели через
// projectPickedMsg, когда курсор встал на строку проекта — дребезг и «уже
// загружен, запроса нет» решает сама панель (setProject).
func (m treeModel) onCursorMoved() (treeModel, tea.Cmd) {
	if row, ok := m.selected(); ok && row.Kind == treeKindProject {
		project := row.Project
		return m, func() tea.Msg { return projectPickedMsg{project: project} }
	}
	return m, nil
}

func (m *treeModel) setExpanded(path string, expanded bool) {
	set := func(rows []treeRow) bool {
		for i := range rows {
			if rows[i].FullPath == path {
				rows[i].Expanded = expanded
				if _, ok := m.children[path]; ok || rows[i].Kind == treeKindProject {
					rows[i].ChildrenSet = true
				}
				return true
			}
		}
		return false
	}
	if set(m.roots) {
		return
	}
	for k, rows := range m.children {
		if set(rows) {
			m.children[k] = rows
			return
		}
	}
}

// rebuild собирает плоский список для компонента: корни, а за каждым
// раскрытым узлом — его дети с глубиной на единицу больше, рекурсивно.
// Дерево живёт как плоский список именно потому, что компонент списка
// Bubbles умеет фильтровать и прокручивать плоский список, — своего дерева
// с прокруткой и фильтром здесь не пишется (D-04).
func (m *treeModel) rebuild() {
	var flat []treeRow
	var walk func(rows []treeRow)
	walk = func(rows []treeRow) {
		for _, r := range rows {
			flat = append(flat, r)
			if r.Kind == treeKindProject || r.Kind == treeKindNote || !r.Expanded {
				continue
			}

			kids, loaded := m.children[r.FullPath]
			walk(kids)

			// Раскрытый узел, под которым ничего нет, обязан сказать, почему
			// его пусто: отказ источника и честный ноль записей — разные
			// события, и раньше оба выглядели как пустое место (это и была
			// половина поломки «список репозиториев пуст»).
			if reason := m.nodeErr[r.FullPath]; reason != "" {
				flat = append(flat, noteRow(r, NoteSourceRefused(reason)))
				continue
			}
			if loaded && len(kids) == 0 {
				flat = append(flat, noteRow(r, NoteSourceEmpty()))
			}
		}
	}
	walk(m.roots)

	items := make([]list.Item, len(flat))
	for i, r := range flat {
		items[i] = r
	}
	m.list.SetItems(items)
}

// bodyView собирает тело колонки репозиториев (Фаза 13, план 13-02):
// заголовок теперь рисует корневая модель (App.columnView) — здесь только
// тело, которое заменяется честной строкой при первой загрузке, пустом
// дереве или ошибке. focused решает, показывает ли список курсор инверсией
// (treeDelegate.isFocused, через m.listFocused): вне фокуса ленты второго
// курсора на экране быть не должно.
func (m treeModel) bodyView(width int, focused bool) string {
	// «Корень ещё не отвечал» — это факт наличия записи о полноте, а не
	// поднятый где-то признак загрузки: сама загрузка корня запускается
	// корневой моделью (app.go, browseMsg), и m.loading там не поднимается —
	// первый кадр из-за этого утверждал «ни одной группы и ни одного
	// проекта» ещё до первого ответа API.
	_, rootAnswered := m.complete[""]

	var body string
	switch {
	case !rootAnswered && m.loadErr == "":
		body = "тяну список…"
	case m.loadErr != "" && len(m.roots) == 0:
		// Причина замещает тело панели ТОЛЬКО когда дерева нет вовсе. Раньше
		// любой отказ любого узла стирал уже показанное дерево с экрана —
		// человек терял и то, что успело загрузиться, и место, где сломалось.
		body = NoteSourceRefused(m.loadErr)
	case len(m.roots) == 0:
		body = NoteSourceEmpty()
	default:
		*m.listFocused = focused
		body = m.list.View()
	}

	parts := []string{body}
	// «Не знаем» и «неполон» — разные вещи: до первого treeLoadedMsg карта
	// пуста, и голое !m.complete[""] печатало под «тяну список…» строку
	// «показаны первые 0 записей — список длиннее» (WR-09 обзора v0.3.0).
	if loaded, known := m.complete[""]; known && !loaded {
		parts = append(parts, m.theme.Muted.Render(fmt.Sprintf("показаны первые %d записей — список длиннее", len(m.roots))))
	}
	if m.cached[""] {
		parts = append(parts, m.theme.Muted.Render(fmt.Sprintf("из кэша, %s назад", Ago(m.fetchedAt[""]))))
	}
	return strings.Join(parts, "\n")
}

// columnView — тело ОДНОЙ колонки дерева без заголовка (Фаза 13, план
// 13-02): заголовок рисует корневая модель (App.columnView), второй здесь
// был бы мусором. Репозитории и пайплайны — общий контракт колонки одной и
// той же модели: id называет, какая из двух колонок сейчас рисуется.
func (m treeModel) columnView(id columnID, width int, focused bool) string {
	if id == colPipelines {
		return m.runs.view()
	}
	return m.bodyView(width, focused)
}

// cursorAt — часть общего контракта колонки (Фаза 13, план 13-03): номер
// выбранной строки списка и путь строки под курсором. Путь узла уже
// уникален и уже используется как ключ во всех картах модели (findRow) —
// второго ключа здесь не заводится. Пустое дерево (курсор ни на чём не
// стоит) возвращает нулевые значения: запоминать в этом случае нечего.
func (m treeModel) cursorAt() (int, string) {
	if row, ok := m.selected(); ok {
		return m.list.Index(), row.FullPath
	}
	return 0, ""
}

// withCursor — часть общего контракта колонки (Фаза 13, план 13-03):
// сначала ищется узел по пути (m.findRow) — если он всё ещё существует,
// курсор списка встаёт на его позицию в уже пересобранном плоском списке;
// не найден — курсор встаёт на index, прижатый к границам списка. Порядок
// обязан быть именно таким: пересборка плоского списка после раскрытия узла
// и после загрузки детей меняет номера строк, поэтому «помню номер» соврало
// бы после первого же обновления — путь узла переживает перезагрузку, а
// номер строки нет.
func (m treeModel) withCursor(index int, key string) treeModel {
	items := m.list.Items()
	if len(items) == 0 {
		m.list.Select(0)
		return m
	}
	if key != "" {
		if _, ok := m.findRow(key); ok {
			for i, it := range items {
				if row, ok := it.(treeRow); ok && row.Kind != treeKindNote && row.FullPath == key {
					m.list.Select(i)
					return m
				}
			}
		}
	}
	idx := index
	if idx < 0 {
		idx = 0
	} else if idx >= len(items) {
		idx = len(items) - 1
	}
	m.list.Select(idx)
	return m
}

// hintText выбирает строку подсказки: при фокусе на правой панели — из её
// пяти формулировок; при фокусе на дереве — из восьми формулировок дерева.
// Подсказка всегда описывает следующий шаг того места, где человек сейчас
// находится.
func (m treeModel) hintText() string {
	if m.focus == focusRuns {
		return m.runs.hintText()
	}

	switch {
	case m.loading && len(m.roots) == 0:
		return HintTreeLoading()
	case m.loadErr != "":
		return HintTreeFailed(m.loadErr)
	case len(m.roots) == 0:
		return HintTreeEmpty()
	case m.list.FilterState() == list.FilterApplied || m.list.FilterState() == list.Filtering:
		return HintTreeFiltered(len(m.list.VisibleItems()))
	case m.cached[""]:
		return HintTreeStale(Ago(m.fetchedAt[""]))
	}

	if row, ok := m.selected(); ok {
		switch row.Kind {
		case treeKindNote:
			// Курсор стоит на строке-объяснении: подсказка называет то же
			// самое, что видит человек, и следующий шаг — повтор запроса.
			return HintTreeNote(row.Name)
		case treeKindProject:
			return HintTreeOpenProject(row.Name)
		default:
			if row.Expanded {
				return HintTreeCollapse(row.Name)
			}
			return HintTreeExpand(row.Name)
		}
	}
	return HintTreeLoading()
}

// keyBar собирает строку клавиш экрана дерева из короткой формы помощи
// раскладки (Фаза 12, POL-03).
func (m treeModel) keyBar() string {
	return KeyBar(m.theme, m.keys)
}
