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
)

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
	// в обоих случаях panelTitle и load() читают только PipelineID/Ref/
	// CommitSHA — второй ветки для этих двух полей не заводится.
	job provider.Job

	jobs     []provider.Job
	cursor   int
	complete bool
	cached   bool
	fetched  string

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

func (m jobsModel) setSize(width, height int) jobsModel {
	m.width = width
	m.height = height
	m.log = m.log.setSize(width-2*OuterMargin, LogPanelLines)
	return m
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
		m.jobs = msg.jobs
		m.complete = msg.complete
		m.cached = msg.cached
		m.fetched = msg.fetched
		// Курсор встаёт на первую упавшую джобу, если она есть, и на
		// первую строку, если её нет — «сразу показан» из требования: без
		// этого человеку пришлось бы искать упавшую джобу глазами (BROW-04).
		m.cursor = 0
		for i, j := range m.jobs {
			if j.Status == StatusFailed {
				m.cursor = i
				break
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

	case tea.KeyPressMsg:
		if m.cmd.active {
			return m.updateCommandKey(msg)
		}
		switch {
		case key.Matches(msg, m.keys.Up):
			if m.cursor > 0 {
				m.cursor--
				return m.selectCursor()
			}
			return m, nil
		case key.Matches(msg, m.keys.Down):
			if m.cursor < len(m.jobs)-1 {
				m.cursor++
				return m.selectCursor()
			}
			return m, nil
		case key.Matches(msg, m.keys.Open):
			if m.cursor >= 0 && m.cursor < len(m.jobs) {
				job := m.jobs[m.cursor]
				return m, func() tea.Msg { return openJobMsg{job: job} }
			}
			return m, nil
		case key.Matches(msg, m.keys.Refresh):
			m.loading = true
			return m, m.fetch(true)
		case key.Matches(msg, m.keys.Back):
			return m, func() tea.Msg { return backMsg{} }
		case key.Matches(msg, m.keys.Command):
			m.cmd = newCommandBar()
			return m, nil
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

// keyBar собирает строку клавиш экрана из короткой формы помощи раскладки
// (Фаза 12, POL-03) — полный перечень, включая обновление и командный
// режим, живёт на экране помощи (`?`).
func (m jobsModel) keyBar() string {
	if m.cmd.active {
		return m.cmd.input.View()
	}
	return KeyBar(m.theme, m.keys)
}

// panelTitle собирает заголовок панели: идентификатор пайплайна, ветку и
// сокращённый до восьми символов коммит — верхним регистром и приглушённым
// жирным, как требует контракт.
func (m jobsModel) panelTitle() string {
	sha := m.job.CommitSHA
	if len(sha) > 8 {
		sha = sha[:8]
	}
	title := fmt.Sprintf("ПАЙПЛАЙН #%d · %s · %s", m.job.PipelineID, m.job.Ref, sha)
	return m.theme.PanelTitle.Render(title)
}

// rowLine отрисовывает одну строку списка: символ статуса из единственной
// таблицы соответствия, зазор RowIconGap, имя джобы, статус словом ровно
// как в API (не переводится), приглушённое относительное время. Строка под
// курсором получает GlyphCursor справа и инверсию на всю строку — курсор и
// статус сосуществуют.
func (m jobsModel) rowLine(j provider.Job, selected bool) string {
	var glyph string
	if j.Status == "running" {
		// running — единственная анимированная строка: вместо символа
		// рисуется кадр общего спиннера экрана в акцентном стиле.
		glyph = m.theme.Accent.Render(m.spin.View())
	} else {
		glyph = m.theme.StatusStyle(j.Status).Render(StatusGlyph(j.Status))
	}

	name := Fit(j.Name, 24)
	note := ""
	if j.Status == "manual" {
		note = " " + m.theme.Muted.Render(ManualNote)
	}
	ago := m.theme.Muted.Render(Ago(j.FinishedAt))

	line := fmt.Sprintf("%s%s%s  %s%s  %s",
		glyph, strings.Repeat(" ", RowIconGap), name, j.Status, note, ago)
	if selected {
		line = m.theme.Selected.Render(line + " " + GlyphCursor)
	}
	return line
}

// bodyView собирает тело экрана: таблица джоб сверху (заголовок панели
// остаётся всегда, тело заменяется одной честной строкой при пустом списке
// или ошибке загрузки; неполный список и список из кэша называются
// отдельными приглушёнными строками под таблицей — BROW-02), панель лога
// под ней.
func (m jobsModel) bodyView() string {
	header := m.panelTitle()

	var jobsBody string
	switch {
	case m.loadErr != "":
		jobsBody = header + "\n" + fmt.Sprintf("не удалось получить список джоб: %s", m.loadErr)
	case !m.loading && len(m.jobs) == 0:
		jobsBody = header + "\n" + "нет джоб"
	default:
		var b strings.Builder
		b.WriteString(header)
		b.WriteString("\n")
		for i, j := range m.jobs {
			b.WriteString(m.rowLine(j, i == m.cursor))
			b.WriteString("\n")
		}
		if !m.complete {
			b.WriteString(m.theme.Muted.Render(fmt.Sprintf("показаны первые %d записей — список длиннее", len(m.jobs))))
			b.WriteString("\n")
		}
		if m.cached {
			b.WriteString(m.theme.Muted.Render(fmt.Sprintf("из кэша, %s назад", m.fetched)))
		}
		jobsBody = b.String()
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
			// джобы, если он уже разобран — на этом экране он не
			// запрашивается (запрос конфига принадлежит экрану джобы,
			// план 09-02), поэтому подстановка держится нулевой: врать
			// числами в подсказке нельзя.
			return HintJobFailed(j.Name, 0, 0)
		}
	}
	for _, j := range m.jobs {
		if j.Status == "running" || j.Status == "pending" {
			return HintPipelineRunning()
		}
	}
	return HintNoFailedJob()
}
