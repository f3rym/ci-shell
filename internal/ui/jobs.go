package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"

	"github.com/f3rym/ci-shell/internal/joburl"
	"github.com/f3rym/ci-shell/internal/provider"
	"github.com/f3rym/ci-shell/internal/provider/gitlab"
	"github.com/f3rym/ci-shell/internal/token"
)

// jobsModel — экран списка джоб пайплайна (TUI-03): одна полноширинная
// панель без дерева слева (09-UI-SPEC.md, «Границы этой фазы»).
type jobsModel struct {
	ref joburl.Ref
	p   provider.Provider
	// job — метаданные открывающей джобы, пришедшие с заставки: второй
	// запрос за ними список джоб не делает.
	job provider.Job

	jobs   []provider.Job
	cursor int

	loading bool
	loadErr string

	width  int
	height int

	theme Theme
	keys  KeyMap
	spin  spinner.Model
}

// Сообщения экрана списка джоб.
type jobsLoadedMsg struct{ jobs []provider.Job }
type jobsFailedMsg struct{ reason string }
type openJobMsg struct{ job provider.Job }

// newJobsModel строит модель списка джоб. Значение интерфейса провайдера
// собирается здесь же: сообщение jobRefMsg несёт только ref и job, поэтому
// резолв токена и клиент GitLab — забота этой модели, а не заставки.
func newJobsModel(ref joburl.Ref, job provider.Job, theme Theme, keys KeyMap) jobsModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	m := jobsModel{ref: ref, job: job, theme: theme, keys: keys, spin: sp}

	tok, err := token.Resolve(ref.Host)
	if err != nil {
		m.loadErr = err.Error()
		return m
	}
	m.p = gitlab.New(ref.Host, tok)
	return m
}

func (m jobsModel) setSize(width, height int) jobsModel {
	m.width = width
	m.height = height
	return m
}

// load запускает загрузку джоб пайплайна командой в отдельной горутине —
// контракт запрещает подвешивать интерфейс любой операцией, ходящей в
// сеть.
func (m jobsModel) load() tea.Cmd {
	loadErr := m.loadErr
	p := m.p
	ref := m.ref
	pipelineID := m.job.PipelineID

	fetch := func() tea.Msg {
		if p == nil {
			return jobsFailedMsg{reason: loadErr}
		}
		// Постраничный обход и кэш для этого экрана — план 10-03; здесь
		// сигнатура приводится к новой форме первой страницей, чтобы
		// дерево кода оставалось согласованным на границе плана 10-01.
		page, err := p.PipelineJobs(context.Background(), ref.ProjectPath, pipelineID, provider.PageRequest{})
		if err != nil {
			return jobsFailedMsg{reason: err.Error()}
		}
		return jobsLoadedMsg{jobs: page.Items}
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
			return m, m.load()
		}
	}
	return m, nil
}

// keyBar собирает строку клавиш экрана: перемещение, открытие, обновление,
// выход. Фильтра и помощи в ней нет — их клавиши принадлежат Фазам 10 и
// 12, и их отсутствие здесь осознанное.
func (m jobsModel) keyBar() string {
	return KeyBar(m.theme, m.keys.Up, m.keys.Open, m.keys.Refresh, m.keys.Quit)
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
// заменяется одной честной строкой при пустом списке или ошибке загрузки.
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
	if len(m.jobs) == 100 {
		b.WriteString(m.theme.Muted.Render("показаны первые 100 джоб — постраничный обход появится позже"))
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
		if j.Status == "failed" {
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
