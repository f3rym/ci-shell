package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/lipgloss/v2"

	"github.com/f3rym/ci-shell/internal/event"
	"github.com/f3rym/ci-shell/internal/render"
)

// stepRow — одна строка панели шагов: номер, секция, команда и текущее
// состояние (те же слова статуса, что у джоб в internal/ui/jobs.go).
type stepRow struct {
	Index   int
	Section string
	Command string
	Status  string
}

// envRow — одна строка панели окружения: ключ и уже принятое решение о
// показе значения (render.DisplayValue, посчитано ровно один раз при сборке
// envRowsFromSession).
type envRow struct {
	Key     string
	Display string
}

// loopPhase — состояние петли фикса, по таблице строки подсказки контракта
// (09-UI-SPEC.md, «Строка подсказки», раздел «Джоба»). Пять состояний этого
// плана; остальные (перезапуск шага, прогон оставшихся, чистый прогон)
// добавляет задача 3.
type loopPhase int

const (
	phasePreparing loopPhase = iota
	phaseImageReady
	phaseHandingTerminal
	phaseLeftShell
	phaseBlocked
)

// shellHandoffMsg гарантирует, что последний полный кадр с подсказкой
// HintHandingTerminal нарисован ДО того, как Bubble Tea снимет контроль над
// терминалом — иначе на экране остался бы зависший кадр спиннера или
// недорисованная строка (09-UI-SPEC.md, «Вход и выход», пункт 2).
type shellHandoffMsg struct{}

// logPanelLines — сколько последних строк ограниченного буфера лога сессии
// показывает панель лога.
const logPanelLines = 14

// jobModel — экран джобы: панели шагов, окружения и лога, вход в шелл, петля
// фикса.
type jobModel struct {
	session *Session
	theme   Theme
	keys    KeyMap

	stepRows []stepRow
	envRows  []envRow

	// cursor — номер выбранного шага (считая от нуля), общий для панели
	// шагов и заголовка панели лога; двигают его ↑↓/jk. -1 — шаг ещё не
	// выбирали (лог не открыт). Полноценный Bubbles viewport (постраничная
	// прокрутка длинных логов) здесь не заведён: панель лога показывает
	// последние logPanelLines строк ограниченного буфера сессии — этого
	// достаточно для «хвоста упавшего шага» контракта, а постраничная
	// навигация по длинным логам принадлежит Фазе 10 (BROW-02).
	cursor int

	phase loopPhase
	// pulling/pullImage — идёт тяга образа (для preparingView и hintText).
	// Полноценный индикатор прогресса Bubbles (progress) с числом слоёв
	// здесь не заведён: доменный слой (internal/runner) не эмитит событие с
	// числом слоёв — только спиннер и текст, как и в obычном режиме тяга
	// образа не разбирается построчно (idea-0.3.0 §4, «Ключевая находка»).
	pulling   bool
	pullImage string

	// canceling — Ctrl-C нажат, идёт отмена текущей долгой операции.
	canceling bool

	// banner — однострочный баннер над строкой подсказки. blockedCanceled
	// истинно, когда причина — отмена человеком (Session.Cancel), а не
	// поломка инструмента: банnер тогда рисуется приглушённым, не danger.
	banner          string
	blockedCanceled bool

	// logErr — обрыв чтения лога. Буфер лога живёт только в памяти и не
	// читает файлов, поэтому это поле сегодня не заполняется никем — ветка
	// сохранена, чтобы честное пустое/ошибочное состояние контракта
	// (09-UI-SPEC.md, «Состояния пустоты и ошибок по экранам») не потерялось
	// при первом реальном источнике обрыва.
	logErr string

	spin spinner.Model

	width, height int
}

// newJobModel строит модель экрана джобы поверх уже собранной сессии.
func newJobModel(session *Session, theme Theme, keys KeyMap) jobModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return jobModel{
		session: session,
		theme:   theme,
		keys:    keys,
		cursor:  -1,
		phase:   phasePreparing,
		spin:    sp,
	}
}

// init запускает тик спиннера и подготовку сессии.
func (m jobModel) init() tea.Cmd {
	return tea.Batch(m.spin.Tick, m.session.PrepareCmd(context.Background()))
}

func (m jobModel) setSize(width, height int) jobModel {
	m.width = width
	m.height = height
	return m
}

// update обрабатывает сообщения экрана джобы: тик спиннера, готовность или
// отказ сессии, событие фазы 8 (EventMsg), передачу и возврат терминала,
// клавиши.
func (m jobModel) update(msg tea.Msg) (jobModel, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case sessionReadyMsg:
		m.phase = phaseImageReady
		m.pulling = false
		m.stepRows = stepRowsFromSession(m.session)
		m.envRows = envRowsFromSession(m.session)
		if msg.outcome.Failed != nil {
			m.cursor = msg.outcome.FailedIndex - 1
		}
		return m, nil

	case sessionFailedMsg:
		m.canceling = false
		m.phase = phaseBlocked
		m.blockedCanceled = msg.canceled
		m.banner = msg.reason
		return m, nil

	case EventMsg:
		return m.applyEvent(msg.Event), nil

	case shellHandoffMsg:
		return m, m.session.ShellCmd(context.Background())

	case shellFinishedMsg:
		m.phase = phaseLeftShell
		m.banner = ""
		if msg.err != nil {
			m.banner = msg.err.Error()
		}
		// Возврат из механизма передачи терминала обязан сопровождаться
		// полной перерисовкой — частичная оставляет мусор на экране
		// (контракт, «Вход и выход», пункт 2; idea-0.3.0 §7). Статусы шагов
		// при этом НЕ меняются: шелл сам по себе шаг не перезапускает.
		return m, tea.ClearScreen

	case tea.KeyPressMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

// applyEvent разбирает событие фазы 8 переключателем по типу и двигает
// состояние экрана — интерфейс не превращает событие в текст для печати, а
// превращает в состояние модели (второй подписчик тех же событий).
func (m jobModel) applyEvent(e event.Event) jobModel {
	switch e := e.(type) {
	case event.ImagePulling:
		m.pulling = true
		m.pullImage = e.Image
	case event.ImageLocal:
		m.pulling = false
	case event.ContainerStarting:
		m.pulling = false
	case event.ContainerReady:
		m.pulling = false
	case event.StepStarted:
		for i := range m.stepRows {
			switch {
			case m.stepRows[i].Index == e.Index:
				m.stepRows[i].Status = "running"
			case m.stepRows[i].Index < e.Index && m.stepRows[i].Status != "failed":
				m.stepRows[i].Status = "success"
			}
		}
		if m.cursor < 0 {
			m.cursor = e.Index - 1
		}
	case event.StepsSummary:
		// Итог обхода расставляет отметки уже через sessionReadyMsg/
		// runFinishedMsg (задача 3) — здесь разбирать нечего, но случай
		// назван явно, чтобы список событий читался как исчерпывающий.
	}
	return m
}

// updateKey разбирает нажатие клавиши экрана джобы.
func (m jobModel) updateKey(msg tea.KeyPressMsg) (jobModel, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Cancel):
		if m.phase == phasePreparing {
			m.session.Cancel()
			m.canceling = true
		}
		return m, nil

	case key.Matches(msg, m.keys.Shell) && m.phase == phaseImageReady:
		// Нажатие НЕ возвращает механизм передачи терминала сразу: сначала
		// фаза переводится в phaseHandingTerminal, и возвращается команда,
		// немедленно шлющая shellHandoffMsg — она гарантирует, что кадр с
		// подсказкой HintHandingTerminal нарисован до передачи терминала.
		m.phase = phaseHandingTerminal
		return m, func() tea.Msg { return shellHandoffMsg{} }

	case key.Matches(msg, m.keys.Up):
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil

	case key.Matches(msg, m.keys.Down):
		if m.cursor < len(m.stepRows)-1 {
			m.cursor++
		}
		return m, nil
	}
	return m, nil
}

// stepRowsFromSession строит срез строк шагов из уже готового результата
// сессии: шаги до упавшего — успешные, упавший — отказ, шаги после него не
// выполнялись вовсе (set -e останавливает скрипт, internal/runner/steps.go).
func stepRowsFromSession(s *Session) []stepRow {
	rows := make([]stepRow, 0, len(s.steps))
	for i, st := range s.steps {
		status := "success"
		switch {
		case s.outcome.Failed != nil && i+1 == s.outcome.FailedIndex:
			status = "failed"
		case s.outcome.Failed != nil && i+1 > s.outcome.FailedIndex:
			status = "created"
		}
		rows = append(rows, stepRow{Index: i + 1, Section: st.Section, Command: st.Command, Status: status})
	}
	return rows
}

// envRowsFromSession строит срез строк окружения. render.DisplayValue —
// единственная точка решения о показе значения, зовётся здесь ровно один
// раз на переменную: собственной ветки «показывать или нет» в этом файле
// нет ни одной.
func envRowsFromSession(s *Session) []envRow {
	rows := make([]envRow, 0, len(s.environment.Vars))
	for _, v := range s.environment.Vars {
		rows = append(rows, envRow{Key: v.Key, Display: render.DisplayValue(v)})
	}
	return rows
}

// panelWidths считает ширины левой (шаги) и правой (окружение) панелей по
// формуле контракта: доступная ширина за вычетом отступов и зазора делится в
// долю StepsSharePercent, но левая не меньше StepsPanelMin, правая не меньше
// EnvPanelMin, а вся лишняя ширина при росте окна уходит в правую панель.
func (m jobModel) panelWidths() (left, right int) {
	avail := m.width - 2*OuterMargin - PanelGap
	if avail < StepsPanelMin+EnvPanelMin {
		avail = StepsPanelMin + EnvPanelMin
	}
	left = avail * StepsSharePercent / 100
	if left < StepsPanelMin {
		left = StepsPanelMin
	}
	right = avail - left
	if right < EnvPanelMin {
		right = EnvPanelMin
	}
	return left, right
}

// stepsPanel рисует панель шагов: заголовок верхним регистром приглушённым
// жирным, затем по строке на шаг.
func (m jobModel) stepsPanel() string {
	left, _ := m.panelWidths()
	title := m.theme.PanelTitle.Render("ШАГИ")
	var b strings.Builder
	b.WriteString(title)
	b.WriteString("\n")
	for i, r := range m.stepRows {
		b.WriteString(m.stepLine(r, i == m.cursor, left))
		b.WriteString("\n")
	}
	return lipgloss.NewStyle().Width(left).Render(strings.TrimRight(b.String(), "\n"))
}

// stepLine отрисовывает одну строку шага: символ состояния из единственной
// таблицы соответствия (theme.go), зазор, номер, обрезанная по ширине
// панели команда; строка под курсором получает символ курсора справа и
// инверсию на всю строку. Обрезка — функцией темы (Truncate), ручного
// выравнивания форматным глаголом нет.
func (m jobModel) stepLine(r stepRow, selected bool, width int) string {
	glyph := m.stepGlyph(r)
	cmdWidth := width - 6
	if cmdWidth < 1 {
		cmdWidth = 1
	}
	line := fmt.Sprintf("%s%s%d %s", glyph, strings.Repeat(" ", RowIconGap), r.Index, Truncate(r.Command, cmdWidth))
	if selected {
		return m.theme.Selected.Render(line + " " + GlyphCursor)
	}
	return line
}

// stepGlyph — символ состояния шага; running рисуется общим спиннером
// экрана в акцентном стиле, ровно как у running-джобы в internal/ui/jobs.go.
func (m jobModel) stepGlyph(r stepRow) string {
	if r.Status == "running" {
		return m.theme.Accent.Render(m.spin.View())
	}
	return m.theme.StatusStyle(r.Status).Render(StatusGlyph(r.Status))
}

// envPanel рисует панель окружения: заголовок с числом переменных,
// значение — через уже принятое решение о показе (envRow.Display, посчитано
// render.DisplayValue при сборке), под таблицей — приглушённые строки о
// недостающих значениях и первой оговорке сборки окружения.
func (m jobModel) envPanel() string {
	_, right := m.panelWidths()
	title := m.theme.PanelTitle.Render(fmt.Sprintf("ОКРУЖЕНИЕ (%d)", len(m.envRows)))

	if len(m.envRows) == 0 {
		return title + "\n" + fmt.Sprintf("не удалось собрать окружение: %s", "переменных нет")
	}

	var b strings.Builder
	b.WriteString(title)
	b.WriteString("\n")
	for _, r := range m.envRows {
		line := fmt.Sprintf("%s=%s", r.Key, r.Display)
		b.WriteString(Truncate(line, right))
		b.WriteString("\n")
	}
	if missing := len(m.session.environment.Missing); missing > 0 {
		b.WriteString(m.theme.Muted.Render(fmt.Sprintf("не хватает %d значений — впишите их командой ci secrets", missing)))
		b.WriteString("\n")
	}
	if len(m.session.environment.Notices) > 0 {
		b.WriteString(m.theme.Muted.Render(m.session.environment.Notices[0]))
		b.WriteString("\n")
	}
	return lipgloss.NewStyle().Width(right).Render(strings.TrimRight(b.String(), "\n"))
}

// logPanel рисует панель лога: по умолчанию (после готовности сессии, когда
// упавший шаг известен) открыт хвост упавшего шага; пока шаг не выбран —
// честная строка ожидания; при обрыве чтения — честная строка отказа вместо
// содержимого.
func (m jobModel) logPanel() string {
	width := m.width - 2*OuterMargin
	if width < 1 {
		width = 1
	}
	title := m.theme.PanelTitle.Render(fmt.Sprintf("ЛОГ ШАГА %d", m.cursor+1))

	switch {
	case m.cursor < 0:
		return title + "\n" + m.theme.Muted.Render("лог появится, когда вы выберете шаг")
	case m.logErr != "":
		return title + "\n" + fmt.Sprintf("лог недоступен: %s", m.logErr)
	}

	lines := lastLines(m.session.Log(), logPanelLines)
	body := strings.Join(lines, "\n")
	return title + "\n" + lipgloss.NewStyle().Width(width).Render(body)
}

// lastLines возвращает последние n строк lines.
func lastLines(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}

// preparingView — тело экрана, пока сессия готовится (до sessionReadyMsg):
// кадр продолжает тикать спиннером, а не подвешивает интерфейс (тяга образа
// и прогон шагов идут в горутине команды).
func (m jobModel) preparingView() string {
	label := "готовлю джобу…"
	if m.pulling {
		label = fmt.Sprintf("тяну образ %s…", m.pullImage)
	}
	return m.spin.View() + " " + m.theme.Text.Render(label)
}

// bodyView собирает тело экрана джобы: две панели рядом (шаги, окружение),
// под ними панель лога на всю ширину минус отступ от краёв, и однострочный
// баннер над строкой подсказки при отказе.
func (m jobModel) bodyView() string {
	if m.phase == phasePreparing {
		return m.preparingView()
	}

	left := m.stepsPanel()
	right := m.envPanel()
	top := lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", PanelGap), right)
	body := top + "\n\n" + m.logPanel()

	if m.banner != "" {
		style := m.theme.Danger
		if m.blockedCanceled {
			// Отменённая человеком операция не помечается цветом отказа —
			// это осознанное действие, а не поломка.
			style = m.theme.Muted
		}
		body += "\n\n" + style.Render(m.banner)
	}
	return body
}

// keyBar собирает строку клавиш экрана джобы: шелл, назад, выход. Клавиша
// повторить шаг подключается задачей 3 вместе с рабочим обработчиком —
// показывать неработающую клавишу раньше времени не нужно.
func (m jobModel) keyBar() string {
	return KeyBar(m.theme, m.keys.Shell, m.keys.Back, m.keys.Quit)
}

// hintText — (jobModel) hintText() string, единственная функция отображения
// фазы петли фикса в текст подсказки: она зовёт именованные формулировки
// (internal/ui/hint.go) и ничего не собирает на месте.
func (m jobModel) hintText() string {
	switch {
	case m.canceling:
		return HintCanceling()
	case m.phase == phasePreparing && m.pulling:
		return HintPullingImage(m.pullImage)
	case m.phase == phaseBlocked && m.blockedCanceled:
		return "прервано пользователем — esc вернёт к списку джоб"
	case m.phase == phaseBlocked:
		return HintBlocked(m.banner)
	case m.phase == phaseHandingTerminal:
		return HintHandingTerminal()
	case m.phase == phaseLeftShell:
		return HintLeftShell()
	case m.phase == phaseImageReady:
		return HintImageReady(m.session.img.Ref)
	}
	return ""
}
