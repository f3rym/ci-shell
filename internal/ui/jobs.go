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

// jobsModel — экран списка джоб пайплайна (TUI-03): одна полноширинная
// панель без дерева слева (09-UI-SPEC.md, «Границы этой фазы»).
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

// newJobsModel строит модель списка джоб для входа по ссылке с заставки.
// Клиент обхода b собирается вызывающим (internal/ui/app.go) — единственное
// место в пакете, конструирующее browse.Client (то же самое, что уже
// собрало экран дерева); loadErr, если токен для хоста ссылки не резолвится,
// приходит уже готовой строкой, потому что до этого места клиента обхода
// собрать было не из чего.
func newJobsModel(ref joburl.Ref, job provider.Job, b *browse.Client, loadErr string, theme Theme, keys KeyMap) jobsModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return jobsModel{ref: ref, job: job, b: b, loadErr: loadErr, theme: theme, keys: keys, spin: sp}
}

// newJobsModelFromPipeline строит модель списка джоб для входа из дерева
// групп и проектов (Фаза 10, BROW-03): пайплайн уже известен, метаданных
// открывающей джобы нет вовсе. Клиент обхода — тот же, что уже собрал
// экран дерева; второго клиента в интерфейсе не появляется.
func newJobsModelFromPipeline(b *browse.Client, host string, project provider.Project, pipeline provider.Pipeline, theme Theme, keys KeyMap) jobsModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	ref := joburl.Ref{Host: host, ProjectPath: project.FullPath}
	job := provider.Job{
		PipelineID:  pipeline.ID,
		PipelineIID: pipeline.IID,
		Ref:         pipeline.Ref,
		CommitSHA:   pipeline.SHA,
		ProjectPath: project.FullPath,
	}
	return jobsModel{ref: ref, job: job, b: b, theme: theme, keys: keys, spin: sp}
}

func (m jobsModel) setSize(width, height int) jobsModel {
	m.width = width
	m.height = height
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

func (m jobsModel) update(msg tea.Msg) (jobsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case jobsLoadedMsg:
		m.loading = false
		m.loadErr = ""
		m.jobs = msg.jobs
		m.complete = msg.complete
		m.cached = msg.cached
		m.fetched = msg.fetched
		if m.cursor >= len(m.jobs) {
			m.cursor = 0
		}
		return m, nil
	case jobsFailedMsg:
		m.loading = false
		m.loadErr = msg.reason
		return m, nil
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.keys.Up):
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case key.Matches(msg, m.keys.Down):
			if m.cursor < len(m.jobs)-1 {
				m.cursor++
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
		}
	}
	return m, nil
}

// keyBar собирает строку клавиш экрана: перемещение, открытие, назад,
// обновление, выход.
func (m jobsModel) keyBar() string {
	return KeyBar(m.theme, m.keys.Up, m.keys.Open, m.keys.Back, m.keys.Refresh, m.keys.Quit)
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

// bodyView собирает тело панели: заголовок остаётся всегда, тело
// заменяется одной честной строкой при пустом списке или ошибке загрузки;
// неполный список и список из кэша называются отдельными приглушёнными
// строками под таблицей, а не выдаются за свежие и полные (BROW-02).
func (m jobsModel) bodyView() string {
	header := m.panelTitle()

	switch {
	case m.loadErr != "":
		return header + "\n" + fmt.Sprintf("не удалось получить список джоб: %s", m.loadErr)
	case !m.loading && len(m.jobs) == 0:
		return header + "\n" + "нет джоб"
	}

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
	return b.String()
}

// hintText выбирает строку подсказки ровно по таблице контракта.
func (m jobsModel) hintText() string {
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
