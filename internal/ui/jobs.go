package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"

	"github.com/f3rym/ci-shell/internal/browse"
	"github.com/f3rym/ci-shell/internal/joburl"
	"github.com/f3rym/ci-shell/internal/provider"
	"github.com/f3rym/ci-shell/internal/textwidth"
)

// logMinPanelLines — минимум строк, ниже которого панель лога переставала
// бы что-либо показывать (Фаза 17, FIT-04): заголовок панели + минимум 2
// строки её тела (logPanel.setSize уже держит пол в 2, internal/ui/joblog.go)
// + строка положения.
const logMinPanelLines = 4

// jobsModel — экран списка джоб пайплайна (TUI-03): таблица джоб сверху,
// панель лога упавшего шага под ней (Фаза 10, BROW-04), строка команды по
// двоеточию.
type jobsModel struct {
	ref joburl.Ref
	// b — клиент пакета обхода (Фаза 10, BROW-02): список джоб приходит
	// обойденным постранично и из кэша, пока он свеж — второго пути к
	// списку джоб в экране не заводится.
	b *browse.Client
	// job — метаданные открывающей джобы (вход по ссылке) либо синтетическая
	// запись с полями пайплайна (вход из дерева, newJobsModelFromPipeline):
	// в обоих случаях columnLabel и load() читают только PipelineID/Ref/
	// CommitSHA — второй ветки для этих двух полей не заводится.
	job provider.Job

	jobs     []provider.Job
	cursor   int
	complete bool
	cached   bool
	fetched  string
	// cursorPinned — человек уже поставил курсор явным действием (движение
	// клавишами либо восстановление позиции при возврате влево, Фаза 13,
	// план 13-03): поднимается в withCursor и при движении курсора клавишами
	// ниже. Правило BROW-04 «курсор на первую упавшую джобу» уступает
	// запомненной позиции ровно тогда, когда этот признак поднят — «вернулся
	// туда, откуда ушёл» сильнее «сразу показана упавшая», потому что это
	// про явное действие человека.
	cursorPinned bool

	loading bool
	loadErr string

	// log — панель лога упавшего шага настоящего прогона (Фаза 10,
	// BROW-04): курсор списка отдаёт ей джобу единственным вызовом, решения
	// о запросе (дребезг, память, «эта джоба не падала») принимает сама
	// панель.
	log logPanel
	// cmd — строка команды (тот же механизм, что на экране джобы,
	// applied ко второму экрану): пока она активна, клавиши уходят ей.
	cmd commandBar

	width  int
	height int

	theme Theme
	keys  KeyMap
	spin  spinner.Model
}

// Сообщения экрана списка джоб.
type jobsLoadedMsg struct {
	jobs     []provider.Job
	complete bool
	cached   bool
	fetched  string
}
type jobsFailedMsg struct{ reason string }
type openJobMsg struct{ job provider.Job }

// newJobsModelCommon собирает общую часть обоих конструкторов ниже —
// спиннер и панель лога (newLogPanel вызывается ровно здесь, одним местом
// на оба входа в экран).
func newJobsModelCommon(ref joburl.Ref, job provider.Job, b *browse.Client, loadErr string, theme Theme, keys KeyMap) jobsModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return jobsModel{
		ref: ref, job: job, b: b, loadErr: loadErr, theme: theme, keys: keys, spin: sp,
		log: newLogPanel(b, ref.ProjectPath),
	}
}

// newJobsModel строит модель списка джоб для входа по ссылке с заставки.
// Клиент обхода b собирается вызывающим (internal/ui/app.go) — единственное
// место в пакете, конструирующее browse.Client (то же самое, что уже
// собрало экран дерева); loadErr, если токен для хоста ссылки не резолвится,
// приходит уже готовой строкой, потому что до этого места клиента обхода
// собрать было не из чего.
func newJobsModel(ref joburl.Ref, job provider.Job, b *browse.Client, loadErr string, theme Theme, keys KeyMap) jobsModel {
	return newJobsModelCommon(ref, job, b, loadErr, theme, keys)
}

// newJobsModelFromPipeline строит модель списка джоб для входа из дерева
// групп и проектов (Фаза 10, BROW-03): пайплайн уже известен, метаданных
// открывающей джобы нет вовсе. Клиент обхода — тот же, что уже собрал
// экран дерева; второго клиента в интерфейсе не появляется.
func newJobsModelFromPipeline(b *browse.Client, host string, project provider.Project, pipeline provider.Pipeline, theme Theme, keys KeyMap) jobsModel {
	ref := joburl.Ref{Host: host, ProjectPath: project.FullPath}
	job := provider.Job{
		PipelineID:  pipeline.ID,
		PipelineIID: pipeline.IID,
		Ref:         pipeline.Ref,
		CommitSHA:   pipeline.SHA,
		ProjectPath: project.FullPath,
	}
	return newJobsModelCommon(ref, job, b, "", theme, keys)
}

// setSize передаёт колонке джоб ширину и высоту (Фаза 13, план 13-02):
// ширина приходит от ленты (App.visibleLayout), а не от кадра целиком.
// Панель лога настоящего прогона (BROW-04) получает ту же ширину колонки и
// число строк, посчитанное от высоты, оставшейся под списком джоб — рамка
// кадра (заголовок, отступы, строка клавиш, строка подсказки) вычитается
// тем же приёмом, что и у treeModel.setSize, список джоб не ограничен
// числом видимых строк (BROW-02); минимум в две строки держит сама панель
// лога (logPanel.setSize) — отдельной константы темы под число строк
// больше нет.
func (m jobsModel) setSize(width, height int) jobsModel {
	m.width = width
	m.height = height
	// Накладные расходы колонки — та же именованная константа, что и у
	// treeModel.setSize (Фаза 17, ColumnBodyOverhead, internal/ui/theme.go).
	// Минимум панели лога держит сама logPanel.setSize (internal/ui/joblog.go,
	// не трогается этим планом) — маленький или отрицательный logHeight не
	// ломает вызов, только сокращает число видимых строк лога. Список джоб
	// теперь ограничен m.visibleRows() (Фаза 17, FIT-04) — остаток высоты
	// уходит панели лога, а не бесконечному списку.
	bodyHeight := height - ColumnBodyOverhead
	if bodyHeight < 0 {
		bodyHeight = 0
	}
	logHeight := bodyHeight - m.visibleRows() - 1
	m.log = m.log.setSize(width, logHeight)
	return m
}

// visibleRows — часть FIT-04: сколько строк списка джоб уместится в
// колонку при её текущей высоте, если оставить панели лога минимум
// logMinPanelLines строк и одну пустую строку-разделитель. Метод, а не
// поле, посчитанное один раз в setSize: список джоб грузится ПОСЛЕ
// setSize (jobsLoadedMsg приходит позже, асинхронно), и поле, посчитанное
// по ещё пустому m.jobs, никогда не увидело бы настоящую длину списка.
func (m jobsModel) visibleRows() int {
	bodyHeight := m.height - ColumnBodyOverhead
	if bodyHeight < 0 {
		bodyHeight = 0
	}
	n := bodyHeight - logMinPanelLines - 1
	if n < 0 {
		n = 0
	}
	if n > len(m.jobs) {
		n = len(m.jobs)
	}
	return n
}

// naturalWidth — часть общего контракта колонки (Фаза 17, FIT-02): та же
// формула reserved, что уже строит rowLine, сложением вместо вычитания —
// самая широкая строка джобы без обрезки.
func (m jobsModel) naturalWidth() int {
	if len(m.jobs) == 0 {
		return ColumnMin
	}
	max := 0
	for _, j := range m.jobs {
		statusText := Plain(j.Status)
		noteWidth := 0
		if j.Status == "manual" {
			noteWidth = 1 + textwidth.Of(ManualNote)
		}
		agoText := Ago(j.FinishedAt)
		reserved := 1 + RowIconGap + 2 + textwidth.Of(statusText) + noteWidth + 2 + textwidth.Of(agoText)
		w := reserved + textwidth.Of(j.Name)
		if w > max {
			max = w
		}
	}
	return max
}

// load запускает загрузку джоб пайплайна командой в отдельной горутине —
// контракт запрещает подвешивать интерфейс любой операцией, ходящей в сеть.
// Список приходит через пакет обхода: обойден постранично целиком и взят из
// кэша, пока он свеж (BROW-02).
func (m jobsModel) load() tea.Cmd {
	return m.fetch(false)
}

func (m jobsModel) fetch(refresh bool) tea.Cmd {
	loadErr := m.loadErr
	b := m.b
	ref := m.ref
	pipelineID := m.job.PipelineID

	fetch := func() tea.Msg {
		if b == nil {
			return jobsFailedMsg{reason: loadErr}
		}
		res, err := b.Jobs(context.Background(), ref.ProjectPath, pipelineID, refresh)
		if err != nil {
			return jobsFailedMsg{reason: err.Error()}
		}
		fetched := ""
		if res.Cached {
			fetched = Ago(res.FetchedAt)
		}
		return jobsLoadedMsg{jobs: res.Items, complete: res.Complete, cached: res.Cached, fetched: fetched}
	}
	return tea.Batch(fetch, m.spin.Tick)
}

// selectCursor отдаёт джобу под курсором панели лога единственным вызовом —
// решения о запросе (дребезг, память, «не падала») принимает сама панель.
func (m jobsModel) selectCursor() (jobsModel, tea.Cmd) {
	if m.cursor < 0 || m.cursor >= len(m.jobs) {
		return m, nil
	}
	cmd := m.log.Select(m.jobs[m.cursor])
	return m, cmd
}

// setColumnFocus — часть общего контракта колонки (Фаза 13): у колонки джоб
// внутренних частей нет (в отличие от дерева и экрана джобы), и метод
// объявляется ради единообразия вызова из единственной точки передачи
// фокуса (App.focusColumn) — он честно ничего не меняет, а не изображает
// работу.
func (m jobsModel) setColumnFocus(id columnID) jobsModel {
	return m
}

// cursorAt — часть общего контракта колонки (Фаза 13, план 13-03): номер
// курсора и имя джобы под ним. Курсор вне границ списка (список ещё пуст)
// возвращает нулевые значения.
func (m jobsModel) cursorAt() (int, string) {
	if m.cursor < 0 || m.cursor >= len(m.jobs) {
		return 0, ""
	}
	return m.cursor, m.jobs[m.cursor].Name
}

// withCursor — часть общего контракта колонки (Фаза 13, план 13-03): поиск
// джобы с именем key, иначе прижатый к границам номер index. Постановка
// курсора отсюда — явное действие («вернулся туда, откуда ушёл»), поэтому
// cursorPinned поднимается здесь же: следующая перезагрузка списка (update,
// jobsLoadedMsg) больше не переставляет курсор на первую упавшую джобу
// безусловно (BROW-04 уступает запомненной позиции). Метод не трогает
// панель лога (m.log) — восстановление позиции не возвращает команд ни в
// одном из четырёх контрактов колонок этой фазы, а выбор джобы для панели
// лога уже делает selectCursor там, где движение курсора действительно
// команду возвращает.
func (m jobsModel) withCursor(index int, key string) jobsModel {
	m.cursorPinned = true
	if len(m.jobs) == 0 {
		m.cursor = 0
		return m
	}
	if key != "" {
		for i, j := range m.jobs {
			if j.Name == key {
				m.cursor = i
				return m
			}
		}
	}
	idx := index
	if idx < 0 {
		idx = 0
	} else if idx >= len(m.jobs) {
		idx = len(m.jobs) - 1
	}
	m.cursor = idx
	return m
}

// openDeeper — часть общего контракта колонки (Фаза 13): джоба под курсором
// отдаётся сообщением открытия джобы (тело прежней ветви клавиши открытия).
func (m jobsModel) openDeeper() (jobsModel, tea.Cmd) {
	if m.cursor >= 0 && m.cursor < len(m.jobs) {
		job := m.jobs[m.cursor]
		return m, func() tea.Msg { return openJobMsg{job: job} }
	}
	return m, nil
}

func (m jobsModel) update(msg tea.Msg) (jobsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		logCmd := m.log.update(msg)
		return m, tea.Batch(cmd, logCmd)

	case jobsLoadedMsg:
		m.loading = false
		m.loadErr = ""
		// cursorPinned (Фаза 13, план 13-03): человек уже стоял на
		// конкретной джобе явным действием — её имя запоминается ДО того,
		// как m.jobs перезапишется новым списком, потому что старый
		// m.cursor после перезаписи указывает уже на другую джобу, если
		// порядок или состав списка изменился.
		pinnedName := ""
		if m.cursorPinned && m.cursor >= 0 && m.cursor < len(m.jobs) {
			pinnedName = m.jobs[m.cursor].Name
		}
		m.jobs = msg.jobs
		m.complete = msg.complete
		m.cached = msg.cached
		m.fetched = msg.fetched
		// Панель лога пересчитывается ПОСЛЕ замены списка (Фаза 17,
		// FIT-04): при первом setSize (до загрузки) m.jobs был пуст, и
		// logHeight занял всю высоту — без пересчёта список джоб рисовался
		// бы выше отведённого, а лог ниже, той же поломкой, которую эта
		// задача чинит, только отложенной на один кадр.
		m = m.setSize(m.width, m.height)
		if m.cursorPinned {
			// Возврат туда, откуда ушёл, сильнее правила первого показа —
			// курсор ищет прежнюю джобу по имени, а не сбрасывается на
			// первую упавшую (BROW-04 уступает запомненной позиции).
			m.cursor = 0
			for i, j := range m.jobs {
				if j.Name == pinnedName {
					m.cursor = i
					break
				}
			}
		} else {
			// Курсор встаёт на первую упавшую джобу, если она есть, и на
			// первую строку, если её нет — «сразу показан» из требования:
			// без этого человеку пришлось бы искать упавшую джобу глазами
			// (BROW-04). Применяется только пока человек ещё не подтвердил
			// свою позицию явным действием.
			m.cursor = 0
			for i, j := range m.jobs {
				if j.Status == StatusFailed {
					m.cursor = i
					break
				}
			}
		}
		return m.selectCursor()

	case jobsFailedMsg:
		m.loading = false
		m.loadErr = msg.reason
		return m, nil

	case logDebounceMsg, logLoadedMsg, logFailedMsg:
		cmd := m.log.update(msg)
		return m, cmd

	case commandMsg:
		return m.applyCommand(msg)

	case tea.PasteMsg:
		// Вставка в открытую строку команды: приходит отдельным сообщением,
		// а не клавишей (скобочная вставка Bubble Tea v2). Вне открытой
		// строки команды вставлять на этом экране некуда.
		if m.cmd.active {
			var cmd tea.Cmd
			m.cmd.input, cmd = m.cmd.input.Update(msg)
			return m, cmd
		}
		return m, nil

	case tea.KeyPressMsg:
		if m.cmd.active {
			return m.updateCommandKey(msg)
		}
		// Клавиши возврата и открытия (esc, ⏎, →, ←) до модели больше не
		// доходят — их забирает закон ленты в корневой модели
		// (internal/ui/app.go, navFor/openDeeper).
		switch {
		case key.Matches(msg, m.keys.Up):
			// cursorPinned (Фаза 13, план 13-03): движение клавишей — тоже
			// явное действие человека, ровно как withCursor; следующая
			// перезагрузка списка обязана уступить и этой позиции, а не
			// только позиции, восстановленной при возврате.
			m.cursorPinned = true
			if m.cursor > 0 {
				m.cursor--
				return m.selectCursor()
			}
			return m, nil
		case key.Matches(msg, m.keys.Down):
			m.cursorPinned = true
			if m.cursor < len(m.jobs)-1 {
				m.cursor++
				return m.selectCursor()
			}
			return m, nil
		case key.Matches(msg, m.keys.Refresh):
			m.loading = true
			return m, m.fetch(true)
		case key.Matches(msg, m.keys.Log):
			// Клавиша лога (Фаза 15, план 15-01, задача 3): зовёт открытие
			// просмотрщика панели для джобы под курсором — ту же функцию,
			// что и команда :log ниже (applyCommand, "log"), чтобы обе вели
			// в одну и ту же панель, а не собирали открытие каждая по-своему
			// (idea-0.3.1 §4).
			if m.cursor >= 0 && m.cursor < len(m.jobs) {
				return m, m.log.Open(m.jobs[m.cursor])
			}
			return m, nil
		case key.Matches(msg, m.keys.Command):
			// Открытие — общим хелпером (internal/ui/command.go), тем же, что
			// и на экране джобы: без поднятого признака активности строка
			// команды не получала ввод, не рисовалась и не считалась полем
			// ввода — двоеточие здесь не делало ничего, а вместе с ним были
			// недоступны :log и :q (CR-05 обзора v0.3.0).
			var cmd tea.Cmd
			m.cmd, cmd = openCommandBar()
			return m, cmd
		}
	}
	return m, nil
}

// updateCommandKey — та же механика, что и на экране джобы (internal/ui/job.go,
// updateCommandKey): подтверждение разбирает набранное и шлёт commandMsg тем
// же переключателем (applyCommand); отказ разбора остаётся строкой над
// подсказкой и не закрывает поле; возврат закрывает поле, ничего не
// выполняя; любая другая клавиша уходит в поле ввода.
func (m jobsModel) updateCommandKey(msg tea.KeyPressMsg) (jobsModel, tea.Cmd) {
	switch {
	case msg.String() == "enter":
		raw := m.cmd.input.Value()
		parsed, err := parseCommand(raw)
		if err != nil {
			m.cmd.err = err.Error()
			return m, nil
		}
		m.cmd = commandBar{}
		return m, func() tea.Msg { return parsed }
	case key.Matches(msg, m.keys.Back):
		m.cmd = commandBar{}
		return m, nil
	}
	var cmd tea.Cmd
	m.cmd.input, cmd = m.cmd.input.Update(msg)
	return m, cmd
}

// applyCommand — единственная команда, осмысленная на этом экране: полный
// лог джобы под курсором (:log). Второй точки разбора команд в проекте не
// заводится (internal/ui/command.go, parseCommand).
func (m jobsModel) applyCommand(msg commandMsg) (jobsModel, tea.Cmd) {
	switch msg.Name {
	case "log":
		if m.cursor >= 0 && m.cursor < len(m.jobs) {
			cmd := m.log.Full(m.jobs[m.cursor])
			return m, cmd
		}
	case "q":
		// Выход идёт через корневую модель (internal/ui/app.go, quitCmd):
		// точка выхода в проекте одна, и она убирает сессию — CR-01.
		return m, func() tea.Msg { return quitMsg{} }
	}
	return m, nil
}

// keyBar собирает строку клавиш колонки джоб (Фаза 15, план 15-01, задача 3,
// JOB-04): собственные записи колонки — клавиша лога, обновление и
// командный режим — идут ПЕРВЫМИ, затем общий хвост закона ленты и пары
// помощь/выход (KeyBar ниже сам решает, чем пожертвовать при нехватке
// ширины). То, ради чего колонка открыта (лог упавшего шага), стоит первым
// и не вытесняется общим хвостом закона.
func (m jobsModel) keyBar() string {
	if m.cmd.active {
		return m.cmd.input.View()
	}
	return KeyBar(m.theme, m.keys, Hint(m.keys.Log), Hint(m.keys.Refresh), Hint(m.keys.Command))
}

// columnLabel собирает уточнение заголовка колонки джоб (Фаза 13, план
// 13-02): идентификатор пайплайна, ветку и сокращённый до восьми символов
// коммит — ровно то же самое, что раньше печатала собственная шапка панели.
// Корневая модель ставит его в column.label при открытии колонки джоб
// (internal/ui/app.go, jobRefMsg/openPipelineMsg), а рисует лента
// (columnTitle(colJobs) + label) — второго заголовка внутри тела колонки не
// заводится.
func (m jobsModel) columnLabel() string {
	sha := m.job.CommitSHA
	if len(sha) > 8 {
		sha = sha[:8]
	}
	return fmt.Sprintf("#%d · %s · %s", m.job.PipelineID, Plain(m.job.Ref), sha)
}

// rowLine отрисовывает одну строку списка: символ статуса из единственной
// таблицы соответствия, зазор RowIconGap, имя джобы, статус словом ровно
// как в API (не переводится), приглушённое относительное время. Строка под
// курсором получает GlyphCursor справа и инверсию на всю строку — курсор и
// статус сосуществуют, но инверсией рисуется только при selected (её решает
// вызывающий: i == m.cursor && focused — вне фокуса ленты второго курсора
// на экране быть не должно, Фаза 13, план 13-02).
//
// Имя джобы подгоняется к ширине КОЛОНКИ width, а не к фиксированным 24
// ячейкам: остаток считается после символа состояния (1 ячейка, замкнутый
// набор символов темы), зазора, слова статуса, пометки ручного запуска и
// относительного времени — той же графемной мерой (internal/textwidth), что
// и везде; ручного выравнивания форматным глаголом на пользовательской
// строке (имя джобы) здесь нет.
func (m jobsModel) rowLine(j provider.Job, selected bool, width int) string {
	var glyph string
	if j.Status == "running" {
		// running — единственная анимированная строка: вместо символа
		// рисуется кадр общего спиннера экрана в акцентном стиле.
		glyph = m.theme.Accent.Render(m.spin.View())
	} else {
		glyph = m.theme.StatusStyle(j.Status).Render(StatusGlyph(j.Status))
	}

	statusText := Plain(j.Status)
	noteWidth := 0
	note := ""
	if j.Status == "manual" {
		noteWidth = 1 + textwidth.Of(ManualNote)
		note = " " + m.theme.Muted.Render(ManualNote)
	}
	agoText := Ago(j.FinishedAt)
	ago := m.theme.Muted.Render(agoText)

	reserved := 1 + RowIconGap + 2 + textwidth.Of(statusText) + noteWidth + 2 + textwidth.Of(agoText)
	nameWidth := width - reserved
	if nameWidth < 1 {
		nameWidth = 1
	}
	name := Fit(j.Name, nameWidth)

	line := fmt.Sprintf("%s%s%s  %s%s  %s",
		glyph, strings.Repeat(" ", RowIconGap), name, statusText, note, ago)
	if selected {
		line = m.theme.Selected.Render(line + " " + GlyphCursor)
	}
	return line
}

// columnView собирает тело КОЛОНКИ джоб без заголовка (Фаза 13, план
// 13-02): список джоб заменяется одной честной строкой при пустом списке
// или ошибке загрузки; неполный список и список из кэша называются
// отдельными приглушёнными строками под ним (BROW-02); панель лога
// упавшего шага настоящего прогона (BROW-04) остаётся ПОД списком внутри
// ТОЙ ЖЕ колонки и получает ширину колонки — решение, а не умолчание:
// колонка окружения·секретов·лога правее принадлежит уже поднятой
// локальной сессии, а лог настоящего прогона GitLab читается без всякой
// сессии — это разные источники, и складывать их в одну колонку значило бы
// соврать про то, что человек видит. Полноценный просмотрщик лога — Фаза
// 15, и она же решает судьбу этого предпросмотра.
func (m jobsModel) columnView(width int, focused bool) string {
	var jobsBody string
	switch {
	case m.loadErr != "":
		jobsBody = fmt.Sprintf("не удалось получить список джоб: %s", m.loadErr)
	case !m.loading && len(m.jobs) == 0:
		jobsBody = "нет джоб"
	default:
		var b strings.Builder
		first, count, hidden := windowWithHint(len(m.jobs), m.visibleRows(), m.cursor)
		for i := first; i < first+count; i++ {
			b.WriteString(m.rowLine(m.jobs[i], i == m.cursor && focused, width))
			b.WriteString("\n")
		}
		if hidden > 0 {
			b.WriteString(m.theme.Muted.Render(hiddenNote(count, len(m.jobs), hidden)))
			b.WriteString("\n")
		}
		if !m.complete {
			b.WriteString(m.theme.Muted.Render(fmt.Sprintf("показаны первые %d записей — список длиннее", len(m.jobs))))
			b.WriteString("\n")
		}
		if m.cached {
			b.WriteString(m.theme.Muted.Render(fmt.Sprintf("из кэша, %s назад", m.fetched)))
		}
		jobsBody = strings.TrimRight(b.String(), "\n")
	}

	var out strings.Builder
	out.WriteString(jobsBody)
	out.WriteString("\n\n")
	out.WriteString(m.log.view(m.theme))
	if m.cmd.err != "" {
		out.WriteString("\n")
		out.WriteString(m.theme.Danger.Render(m.cmd.err))
	}
	return out.String()
}

// hintText выбирает строку подсказки: состояния лога впереди пяти
// состояний списка (задача 2, план 10-03) — подсказка обязана говорить про
// то, что человек видит прямо сейчас, а сейчас он смотрит в лог.
func (m jobsModel) hintText() string {
	if m.log.hasJob {
		switch {
		case m.log.loadErr != "":
			return HintLogUnavailable(m.log.loadErr)
		case m.log.fullShow:
			return HintLogFull()
		case m.log.loading:
			return HintLogLoading()
		}
		if l, ok := m.log.currentLog(); ok {
			return HintLogTail(l.Section)
		}
	}

	switch {
	case m.loadErr != "":
		return HintRetryJobs()
	case len(m.jobs) == 0 && !m.loading:
		return HintPipelineEmpty()
	}

	for _, j := range m.jobs {
		if j.Status == StatusFailed {
			// Номер упавшего шага и их общее число берутся из конфига
			// джобы — на этом экране он не запрашивается (запрос конфига
			// принадлежит экрану джобы, план 09-02). Поэтому берётся
			// формулировка без чисел, а не нулевая подстановка: «упала на
			// шаге 0 из 0» — тоже враньё числами (WR-13 обзора v0.3.0).
			return HintJobFailedUnknownStep(j.Name)
		}
	}
	for _, j := range m.jobs {
		if j.Status == "running" || j.Status == "pending" {
			return HintPipelineRunning()
		}
	}
	return HintNoFailedJob()
}
