// tree.go — экран дерева репозиториев (Фаза 10, BROW-01; Фаза 14, план
// 14-02): раскрытие, фильтр, две панели (дерево слева, пайплайны выбранного
// проекта справа), переключение фокуса между ними. Корень дерева больше не
// принадлежит одному хосту — каждый ключ становится своим корнем (MENU-03),
// и доступ к хосту запрашивается по требованию при раскрытии его корня.
// Единственный источник данных узла — пакет обхода (internal/browse) клиента
// ЕГО хоста: экран не ходит в сеть сам, не знает про страницы и сроки
// годности кэша и не собирает клиента обхода сам (это по-прежнему делает
// только корневая модель, internal/ui/app.go).
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

// treeKind различает вид строки дерева: хост (корень), группа, личное
// пространство человека или проект. Личное пространство — отдельный вид, а
// не группа с тем же именем: у него другой адрес в API (Namespace{Kind:
// NamespaceUser}) и другой смысл, и подмена одного другим рано или поздно
// показала бы чужие проекты.
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
	// treeKindHost — корень дерева на один ключ (Фаза 14, план 14-02, MENU-
	// 03): дописан в конец перечисления, чтобы не двигать существующие
	// значения. Отдельный вид, а не группа: у хоста нет ни пространства
	// имён, ни адреса в API — его дети берутся не одним запросом, а двумя
	// (личное пространство человека и группы верхнего уровня), ровно как
	// раньше собирался корень целиком.
	treeKindHost
)

// treeRow — одна строка дерева: вид, глубина вложенности, отображаемое имя,
// полный путь, пространство имён (для группы/личного пространства — чем
// спрашивать его проекты), сама доменная группа/проект, хост владельца,
// приписка и признаки раскрытости/загрузки детей.
type treeRow struct {
	Kind     treeKind
	Depth    int
	Name     string
	FullPath string
	NS       provider.Namespace

	// Host — хост, которому принадлежит узел (Фаза 14, план 14-02):
	// наследуется детьми при сборке (loadChildren ниже). Клиент, которым
	// спрашивается узел, выбирается по этому полю, а не по полю модели —
	// узел без хоста был бы узлом, который некому спросить.
	Host string
	// Meta — приглушённая приписка справа от имени; заполняется только у
	// корневой строки-хоста, куда переезжает имя пользователя этого хоста
	// (задача 2, internal/ui/app.go) — заголовок кадра не может назвать
	// один хост, когда их несколько.
	Meta string

	Group   provider.Group
	Project provider.Project

	Expanded    bool
	ChildrenSet bool
}

// FilterValue отдаёт то, по чему человек ищет строку: полный путь для
// группы/проекта/личного пространства, хост для строки-хоста (у неё пути
// нет). У строки-объяснения нет ни того, ни другого — она отдаёт пустое
// значение и потому не всплывает в отфильтрованном списке как «найденный
// проект».
func (r treeRow) FilterValue() string {
	switch r.Kind {
	case treeKindNote:
		return ""
	case treeKindHost:
		return r.Host
	}
	return r.FullPath
}

// noteRow собирает строку-объяснение под узлом owner — единственное место
// сборки такой строки в экране. Путь и хост узла строка носит с собой не для
// показа (рисуется только Name), а чтобы её ключ (nodeKey ниже) совпадал с
// ключом владельца: обновление на ней (g) повторяет запрос ТОГО узла, про
// который она говорит, а не постороннего.
func noteRow(owner treeRow, text string) treeRow {
	return treeRow{Kind: treeKindNote, Depth: owner.Depth + 1, Host: owner.Host, Name: text, FullPath: owner.FullPath}
}

// hostNodeSep — разделитель ключа узла (nodeKey ниже) между хостом и полным
// путём: символ вне печатаемого диапазона, которого нет ни в нормализованном
// хосте (token.NormalizeHost), ни в полном пути проекта или группы GitLab —
// склейка двух частей не создаёт коллизий. Тот же приём, которым уже
// склеиваются ключи кэша в internal/browse/browse.go.
const hostNodeSep = "\x00"

// hostKey — ключ строки-хоста: единственная формула для корня во всём
// проекте, используется и картами модели, и картой доступа корневой модели
// (internal/ui/app.go, opensHostCmd).
func hostKey(host string) string {
	return host + hostNodeSep
}

// nodeKey — единственная формула ключа узла дерева во всём проекте (Фаза 14,
// план 14-02): склеивает хост владельца и полный путь узла. Без хоста в
// ключе две группы с одинаковым путём на разных инстансах делили бы одну
// запись в карте детей, и проекты чужого инстанса показывались бы под своей
// группой — тот же класс дефекта, что подмена личного пространства группой
// (см. комментарий treeKindUser выше). Ключ строки-хоста — hostKey от её
// хоста; у строки-объяснения (noteRow выше) ключ автоматически совпадает с
// ключом её владельца, потому что она носит его же Host и FullPath.
func nodeKey(row treeRow) string {
	if row.Kind == treeKindHost {
		return hostKey(row.Host)
	}
	return row.Host + hostNodeSep + row.FullPath
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
		// Хост (Фаза 14) рисуется тем же символом свёрнутой/раскрытой
		// ветки, что и группа: у него тот же вопрос «раскрыт ли он», просто
		// без отступа — Depth строки-хоста всегда 0.
		if row.Expanded {
			glyph = GlyphExpanded
		} else {
			glyph = GlyphCollapsed
		}
	}

	// meta — приглушённая приписка справа от имени (Фаза 14): у строки-
	// хоста сюда попадает имя пользователя этого хоста, как только доступ к
	// нему получен; у остальных строк Meta всегда пуста.
	meta := ""
	if row.Meta != "" {
		meta = "  " + row.Meta
	}

	width := m.Width() - textwidth.Of(indent) - textwidth.Of(glyph) - RowIconGap - textwidth.Of(meta) - 2
	name := Truncate(row.Name, width)
	line := indent + glyph + strings.Repeat(" ", RowIconGap) + name

	if selected {
		line = d.theme.Selected.Render(line + meta + " " + GlyphCursor)
	} else {
		line = d.theme.Text.Render(line) + d.theme.Muted.Render(meta)
	}
	fmt.Fprint(w, line)
}

// treeModel — экран дерева: перечень хостов (порядок корней), доступ по
// хосту, компонент списка Bubbles, корневые строки, дети по ключу узла,
// признаки полноты/кэша/времени по тому же ключу, правая панель (пайплайны)
// и фокус.
type treeModel struct {
	// hosts — перечень хостов в порядке корней (Фаза 14, план 14-02): столько
	// корней, сколько ключей в файле (MENU-03) — собираются синхронно, без
	// единого запроса в сеть.
	hosts []string
	// access — доступ по хосту: заполняется по мере раскрытия корней (см.
	// needHostMsg/hostReadyMsg ниже), а не целиком при входе. Тип hostAccess
	// объявлен здесь, а заполняет карту корневая модель (internal/ui/app.go)
	// — единственный сборщик клиента обхода остаётся там.
	access map[string]hostAccess

	list list.Model

	roots    []treeRow
	children map[string][]treeRow

	complete  map[string]bool
	cached    map[string]bool
	fetchedAt map[string]time.Time

	// nodeErr — отказ загрузки конкретного узла по его ключу (nodeKey): причина
	// вместе с адресом запроса, пришедшая от провайдера. Отказ узла не стирает
	// дерево с экрана — он показывается строкой под самим узлом (rebuild
	// ниже), потому что «этот узел не открылся» и «списка нет» — разные
	// события, и одинаково выглядеть они не должны. Тот же приём обслуживает
	// и отказ доступа к хосту целиком (internal/ui/app.go, openHostCmd) — его
	// ключ узла — hostKey хоста, и причина ложится под сам корень.
	nodeErr map[string]string
	loading bool
	// pending — ключ узла, для которого сейчас идёт загрузка (или ожидается
	// доступ к хосту); повторное обновление того же узла, пока она не
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

// hostAccess — доступ к одному хосту дерева репозиториев (Фаза 14, план
// 14-02): значение интерфейса провайдера, клиент обхода поверх него и
// пользователь, спрошенный CurrentUser. Тип объявлен здесь (дерево описывает
// форму доступа, которым спрашивает узлы), а заполняет карту доступа
// корневая модель (internal/ui/app.go) — единственный сборщик клиента
// обхода остаётся там (инвариант фазы 10): второй сборщик в дереве означал
// бы два кэша списков на один хост и удвоенный трафик к чужому серверу.
type hostAccess struct {
	Host     string
	Provider provider.Provider
	Client   *browse.Client
	User     provider.User
}

type treeFocus int

const (
	focusTree treeFocus = iota
	focusRuns
)

// Сообщения экрана дерева. Поле «родитель» переименовано в «ключ узла»
// (key, Фаза 14, план 14-02): после введения nodeKey это уже не путь, и
// старое имя врало бы читателю.
type treeLoadedMsg struct {
	key      string
	rows     []treeRow
	complete bool
	cached   bool
	fetched  time.Time
}

// treeFailedMsg — узел с ключом key не загрузился. reason приходит уже
// очищенным (provider.SafeText): текст ошибки провайдера несёт кусок тела
// ответа чужого сервера, а отсюда он уходит и в строку дерева, и в строку
// подсказки — управляющая последовательность в нём перерисовала бы кадр
// (T-10-03). Тем же сообщением приходит и отказ доступа к хосту целиком
// (internal/ui/app.go, openHostCmd) — второй формы отказа узла в проекте
// нет.
type treeFailedMsg struct {
	key    string
	reason string
}

// needHostMsg — дерево просит корневую модель открыть хост (Фаза 14, план
// 14-02): собирать клиента обхода умеет ровно одно место проекта —
// корневая модель (инвариант фазы 10), и заводить второй сборщик в дереве
// значило бы два кэша списков на один хост и удвоенный трафик к чужому
// серверу.
type needHostMsg struct {
	host string
}

// hostReadyMsg — готовый доступ к хосту, собранный корневой моделью
// (internal/ui/app.go, openHostCmd).
type hostReadyMsg struct {
	access hostAccess
}

// projectPickedMsg — проект под курсором дерева вместе с хостом, которому он
// принадлежит: правая панель пайплайнов слушает это сообщение и решает сама,
// запрашивать ли пайплайны заново (дребезг, «уже показан» —
// internal/ui/pipelines.go, setProject), а корневая модель — держать ли в
// ленте колонку пайплайнов (Фаза 13, internal/ui/app.go).
type projectPickedMsg struct {
	host    string
	project provider.Project
}

// newTreeModel собирает дерево синхронно из перечня хостов hosts — по
// строке-корню на хост, без единого запроса в сеть (Фаза 14, план 14-02):
// перечень ключей известен из файла, и сеть здесь не нужна. Доступ first —
// доступ первого хоста, уже проверенный заставкой (CurrentUser), кладётся в
// карту сразу, а его имя пользователя уходит в приписку первой корневой
// строки; возвращается команда загрузки детей этого хоста — человек,
// выбравший в меню репозитории, обязан сразу увидеть свои группы, а не
// пустой список из одной строки.
func newTreeModel(hosts []string, first hostAccess, theme Theme, keys KeyMap) (treeModel, tea.Cmd) {
	focused := new(bool)
	l := list.New(nil, treeDelegate{theme: theme, focused: focused}, 0, 0)
	l.SetFilteringEnabled(true)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)

	m := treeModel{
		hosts:       hosts,
		access:      map[string]hostAccess{},
		list:        l,
		listFocused: focused,
		children:    map[string][]treeRow{},
		complete:    map[string]bool{},
		cached:      map[string]bool{},
		fetchedAt:   map[string]time.Time{},
		pending:     map[string]bool{},
		nodeErr:     map[string]string{},
		runs:        newPipelinePanel(theme),
		theme:       theme,
		keys:        keys,
	}

	roots := make([]treeRow, 0, len(hosts))
	for _, h := range hosts {
		roots = append(roots, treeRow{Kind: treeKindHost, Host: h, Name: h})
	}
	m.roots = roots
	// Запись о полноте выставляется тут же: перечень корней уже известен
	// целиком (собран из файла ключей выше), и «тяну список…» над уже
	// готовым перечнем было бы той же поломкой, которую фаза 10 уже чинила
	// признаком «корень ещё не отвечал» — только теперь применительно к
	// перечню, который в сеть вообще не ходит.
	m.complete[""] = true
	m.rebuild()

	var cmd tea.Cmd
	if len(hosts) > 0 {
		host := hosts[0]
		m.access[host] = first
		if row, ok := m.findRow(hostKey(host)); ok {
			cmd = m.loadChildren(row, false)
		}
	}

	return m, cmd
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

// loadChildren запрашивает детей узла клиентом ЕГО хоста (Фаза 14, план
// 14-02): клиент берётся из карты доступа по хосту СТРОКИ, а не из поля
// модели — второго способа выбрать клиента в файле нет. Для строки-хоста —
// личное пространство человека и группы верхнего уровня (тело прежней
// loadRoot, которой в проекте больше нет: корень дерева синхронен и в сеть
// не ходит). Для группы — сначала подгруппы (только для групп — у личного
// пространства их нет), затем проекты, без включения проектов подгрупп —
// подгруппы уже отдельные узлы дерева. Для личного пространства — только
// проекты.
func (m treeModel) loadChildren(row treeRow, refresh bool) tea.Cmd {
	access := m.access[row.Host]
	b := access.Client
	host := row.Host
	depth := row.Depth + 1
	return func() tea.Msg {
		ctx := context.Background()
		key := nodeKey(row)

		if row.Kind == treeKindHost {
			userRow := treeRow{
				Kind: treeKindUser, Depth: depth, Host: host, Name: access.User.Name, FullPath: access.User.Username,
				NS: provider.Namespace{Kind: provider.NamespaceUser, Path: access.User.Username},
			}
			rows := []treeRow{userRow}

			groups, err := b.Groups(ctx, "", refresh)
			if err != nil {
				return treeFailedMsg{key: key, reason: provider.SafeText(err.Error())}
			}
			for _, g := range groups.Items {
				rows = append(rows, treeRow{
					Kind: treeKindGroup, Depth: depth, Host: host, Name: g.Name, FullPath: g.FullPath, Group: g,
					NS: provider.Namespace{Kind: provider.NamespaceGroup, Path: g.FullPath},
				})
			}
			return treeLoadedMsg{key: key, rows: rows, complete: groups.Complete, cached: groups.Cached, fetched: groups.FetchedAt}
		}

		var rows []treeRow
		complete := true
		var cached bool
		fetched := time.Now()

		if row.Kind == treeKindGroup {
			sub, err := b.Groups(ctx, row.FullPath, refresh)
			if err != nil {
				return treeFailedMsg{key: key, reason: provider.SafeText(err.Error())}
			}
			for _, g := range sub.Items {
				rows = append(rows, treeRow{
					Kind: treeKindGroup, Depth: depth, Host: host, Name: g.Name, FullPath: g.FullPath, Group: g,
					NS: provider.Namespace{Kind: provider.NamespaceGroup, Path: g.FullPath},
				})
			}
			complete = complete && sub.Complete
			cached = cached || sub.Cached
			fetched = sub.FetchedAt
		}

		pr, err := b.Projects(ctx, row.NS, refresh)
		if err != nil {
			return treeFailedMsg{key: key, reason: provider.SafeText(err.Error())}
		}
		for _, p := range pr.Items {
			rows = append(rows, treeRow{Kind: treeKindProject, Depth: depth, Host: host, Name: p.Name, FullPath: p.FullPath, Project: p})
		}
		complete = complete && pr.Complete
		cached = cached || pr.Cached
		if pr.FetchedAt.After(fetched) {
			fetched = pr.FetchedAt
		}

		return treeLoadedMsg{key: key, rows: rows, complete: complete, cached: cached, fetched: fetched}
	}
}

func (m treeModel) update(msg tea.Msg) (treeModel, tea.Cmd) {
	switch msg := msg.(type) {
	case treeLoadedMsg:
		m.loading = false
		delete(m.pending, msg.key)
		delete(m.nodeErr, msg.key)
		m.complete[msg.key] = msg.complete
		m.cached[msg.key] = msg.cached
		m.fetchedAt[msg.key] = msg.fetched
		// Запись заводится и для пустого ответа: «узел загружен, детей
		// ноль» и «узел ещё не загружен» — разные состояния, и различает
		// их именно наличие записи (rebuild ниже читает его же).
		m.children[msg.key] = msg.rows
		m.setExpanded(msg.key, true)
		m.rebuild()
		return m, nil

	case treeFailedMsg:
		m.loading = false
		delete(m.pending, msg.key)
		m.nodeErr[msg.key] = msg.reason
		// Отказ узла раскрывает узел, чтобы причина встала строкой прямо
		// под ним (в том числе и под корнем-хостом, если отказал сам доступ
		// к нему, internal/ui/app.go). Дети при этом НЕ появляются
		// (ChildrenSet остаётся ложным, пока в children нет записи), поэтому
		// повторное ⏎/g на узле пробует загрузку заново, а не «сворачивает
		// пустоту».
		m.setExpanded(msg.key, true)
		m.rebuild()
		return m, nil

	case hostReadyMsg:
		// Доступ к хосту получен (internal/ui/app.go, openHostCmd): доступ
		// кладётся в карту, имя пользователя — в приписку корневой строки
		// этого хоста, признак ожидания снимается, и запускается загрузка
		// детей хоста — человек не должен нажимать ⏎ дважды.
		m.access[msg.access.Host] = msg.access
		key := hostKey(msg.access.Host)
		delete(m.pending, key)
		for i := range m.roots {
			if m.roots[i].Kind == treeKindHost && m.roots[i].Host == msg.access.Host {
				m.roots[i].Meta = msg.access.User.Name
			}
		}
		m.rebuild()
		if row, ok := m.findRow(key); ok {
			return m, m.loadChildren(row, false)
		}
		return m, nil

	case pipelineDebounceMsg, pipelinesLoadedMsg, pipelinesFailedMsg:
		var cmd tea.Cmd
		m.runs, cmd = m.runs.update(msg)
		return m, cmd

	case projectPickedMsg:
		var cmd tea.Cmd
		access := m.access[msg.host]
		m.runs, cmd = m.runs.setProject(msg.host, access.Client, msg.project)
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
// происходит; на проекте отдаётся проект (вместе с его хостом) правой
// панели сообщением выбранного проекта — корневая модель превратит его в
// открытие колонки пайплайнов; на строке-хоста, к которой ещё нет доступа,
// поднимается признак ожидания по ключу узла и уходит просьба открыть хост
// (needHostMsg) корневой модели — второй сборщик клиента обхода в дереве не
// заводится (инвариант фазы 10); на хосте с доступом, на группе или на
// личном пространстве переключается раскрытость и, если дети ещё не
// загружены, запускается их загрузка — раскрытие ветки это углубление
// ВНУТРИ колонки репозиториев, а не переход в колонку правее. Постановка
// фокуса на правую панель отсюда убрана: фокус раздаёт лента
// (setColumnFocus выше).
func (m treeModel) openDeeper() (treeModel, tea.Cmd) {
	row, ok := m.selected()
	if !ok {
		return m, nil
	}
	// Строка-объяснение ничего не открывает. Без этой проверки ⏎ на ней
	// уходил бы загружать посторонний узел.
	if row.Kind == treeKindNote {
		return m, nil
	}
	if row.Kind == treeKindProject {
		project, host := row.Project, row.Host
		return m, func() tea.Msg { return projectPickedMsg{project: project, host: host} }
	}

	key := nodeKey(row)

	if row.Kind == treeKindHost {
		if _, has := m.access[row.Host]; !has {
			if m.pending[key] {
				return m, nil
			}
			m.pending[key] = true
			host := row.Host
			return m, func() tea.Msg { return needHostMsg{host: host} }
		}
	}

	if !row.ChildrenSet {
		if m.pending[key] {
			return m, nil
		}
		m.pending[key] = true
		m.loading = true
		return m, m.loadChildren(row, false)
	}

	m.setExpanded(key, !row.Expanded)
	m.rebuild()
	return m, nil
}

// refreshCursor — обновление узла под курсором, минуя кэш; зажатая клавиша
// не превращается в очередь запросов. Ветвь «узел не найден — обновляем
// корень» из проекта ушла вместе с loadRoot (Фаза 14, план 14-02): перечень
// корней приходит из файла ключей, а не из сети, и обновлять в нём нечего.
// На строке-хосте без доступа обновление ведёт себя как открытие — просит
// доступ; с доступом — повторяет запрос его детей.
func (m treeModel) refreshCursor() (treeModel, tea.Cmd) {
	row, ok := m.selected()
	// Строка-объяснение узлом не является: обновление на ней повторяет
	// запрос того узла, под которым она стоит (её ключ совпадает с ключом
	// владельца, noteRow выше).
	if ok && row.Kind == treeKindNote {
		row, ok = m.findRow(nodeKey(row))
	}
	if !ok {
		return m, nil
	}
	key := nodeKey(row)
	if m.pending[key] {
		return m, nil
	}
	if row.Kind == treeKindHost {
		if _, has := m.access[row.Host]; !has {
			m.pending[key] = true
			host := row.Host
			return m, func() tea.Msg { return needHostMsg{host: host} }
		}
	}
	m.pending[key] = true
	m.loading = true
	return m, m.loadChildren(row, true)
}

// findRow ищет узел дерева по его ключу (nodeKey) среди корней и уже
// загруженных детей. Строки-объяснения из поиска исключены намеренно: их
// ключ совпадает с ключом владельца, и без исключения поиск нашёл бы саму
// строку-объяснение вместо узла, про который она говорит.
func (m treeModel) findRow(key string) (treeRow, bool) {
	if key == "" {
		return treeRow{}, false
	}
	for _, r := range m.roots {
		if r.Kind != treeKindNote && nodeKey(r) == key {
			return r, true
		}
	}
	for _, rows := range m.children {
		for _, r := range rows {
			if r.Kind != treeKindNote && nodeKey(r) == key {
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

// onCursorMoved отдаёт проект под курсором (вместе с его хостом) правой
// панели через projectPickedMsg, когда курсор встал на строку проекта —
// дребезг и «уже загружен, запроса нет» решает сама панель (setProject).
func (m treeModel) onCursorMoved() (treeModel, tea.Cmd) {
	if row, ok := m.selected(); ok && row.Kind == treeKindProject {
		project, host := row.Project, row.Host
		return m, func() tea.Msg { return projectPickedMsg{project: project, host: host} }
	}
	return m, nil
}

func (m *treeModel) setExpanded(key string, expanded bool) {
	set := func(rows []treeRow) bool {
		for i := range rows {
			if nodeKey(rows[i]) == key {
				rows[i].Expanded = expanded
				if _, ok := m.children[key]; ok || rows[i].Kind == treeKindProject {
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

// rebuild собирает плоский список для компонента: корни (строки-хосты), а за
// каждым раскрытым узлом — его дети с глубиной на единицу больше,
// рекурсивно. Дерево живёт как плоский список именно потому, что компонент
// списка Bubbles умеет фильтровать и прокручивать плоский список, — своего
// дерева с прокруткой и фильтром здесь не пишется (D-04).
func (m *treeModel) rebuild() {
	var flat []treeRow
	var walk func(rows []treeRow)
	walk = func(rows []treeRow) {
		for _, r := range rows {
			flat = append(flat, r)
			if r.Kind == treeKindProject || r.Kind == treeKindNote || !r.Expanded {
				continue
			}

			key := nodeKey(r)
			kids, loaded := m.children[key]
			walk(kids)

			// Раскрытый узел, под которым ничего нет, обязан сказать, почему
			// его пусто: отказ источника и честный ноль записей — разные
			// события, и раньше оба выглядели как пустое место (это и была
			// половина поломки «список репозиториев пуст»). Тот же приём
			// показывает и отказ доступа к самому хосту (internal/ui/app.go).
			if reason := m.nodeErr[key]; reason != "" {
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
// тело. Пустой перечень хостов — единственная причина, по которой корень
// дерева может оказаться пуст (Фаза 14, план 14-02): сеть здесь ни при чём —
// попасть на этот экран без ключей нельзя, потому что пункт меню недоступен
// (menuModel.enabled), но честная ветвь остаётся ради самого факта пустого
// состояния, а не ради достижимого пути. focused решает, показывает ли
// список курсор инверсией (treeDelegate.isFocused, через m.listFocused): вне
// фокуса ленты второго курсора на экране быть не должно.
func (m treeModel) bodyView(width int, focused bool) string {
	if len(m.roots) == 0 {
		return NoteSourceEmpty()
	}
	*m.listFocused = focused
	return m.list.View()
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
// выбранной строки списка и ключ узла (nodeKey — Фаза 14, план 14-02:
// единственная формула ключа узла проекта, второй ключ здесь не заводится).
// Пустое дерево (курсор ни на чём не стоит) возвращает нулевые значения:
// запоминать в этом случае нечего.
func (m treeModel) cursorAt() (int, string) {
	if row, ok := m.selected(); ok {
		return m.list.Index(), nodeKey(row)
	}
	return 0, ""
}

// withCursor — часть общего контракта колонки (Фаза 13, план 13-03):
// сначала ищется узел по ключу (m.findRow) — если он всё ещё существует,
// курсор списка встаёт на его позицию в уже пересобранном плоском списке;
// не найден — курсор встаёт на index, прижатый к границам списка. Порядок
// обязан быть именно таким: пересборка плоского списка после раскрытия узла
// и после загрузки детей меняет номера строк, поэтому «помню номер» соврало
// бы после первого же обновления — ключ узла переживает перезагрузку, а
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
				if row, ok := it.(treeRow); ok && row.Kind != treeKindNote && nodeKey(row) == key {
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
// пяти формулировок; при фокусе на дереве — из формулировок дерева, включая
// три состояния строки-хоста (Фаза 14, план 14-02).
func (m treeModel) hintText() string {
	if m.focus == focusRuns {
		return m.runs.hintText()
	}

	switch {
	case len(m.roots) == 0:
		return HintTreeNoKeys()
	case m.list.FilterState() == list.FilterApplied || m.list.FilterState() == list.Filtering:
		return HintTreeFiltered(len(m.list.VisibleItems()))
	}

	if row, ok := m.selected(); ok {
		switch row.Kind {
		case treeKindNote:
			// Курсор стоит на строке-объяснении: подсказка называет то же
			// самое, что видит человек, и следующий шаг — повтор запроса.
			return HintTreeNote(row.Name)
		case treeKindProject:
			return HintTreeOpenProject(row.Name)
		case treeKindHost:
			switch {
			case m.pending[hostKey(row.Host)]:
				return HintTreeHostOpening(row.Host)
			case row.Expanded:
				return HintTreeHostCollapse(row.Host)
			default:
				return HintTreeHostExpand(row.Host)
			}
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
