package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
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
// (09-UI-SPEC.md, «Строка подсказки», раздел «Джоба»).
type loopPhase int

const (
	phasePreparing loopPhase = iota
	phaseImageReady
	phaseHandingTerminal
	phaseLeftShell
	phaseBlocked
	// phaseRunning — идёт перезапуск шага, :rest или :clean (задача 3):
	// подсказка временно замещается HintRunning, кадр тикает спиннером.
	phaseRunning
	// phaseStepFixed/phaseStepStillFails — итог R/:R (FIXUI-01).
	phaseStepFixed
	phaseStepStillFails
	// phaseRestPassed/phaseRestFailed — итог :rest (FIXUI-02).
	phaseRestPassed
	phaseRestFailed
	// phaseCleanGreen/phaseCleanFails — итог :clean (FIXUI-02).
	phaseCleanGreen
	phaseCleanFails
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

	// runLabel/runOffset/runStep — состояние идущего прогона задачи 3
	// (phaseRunning): метка вида прогона для HintRunning, смещение среза
	// шагов (то же, что получила RetryStepCmd/RestCmd/CleanCmd) и абсолютный
	// номер шага, на котором прогон сейчас — считается из события
	// event.StepStarted (относительный индекс события + runOffset).
	runLabel  string
	runOffset int
	runStep   int

	spin spinner.Model

	// cmd — строка команды (задача 1, план 09-03): двоеточие открывает её и
	// передаёт ей фокус; пока она активна, клавиши уходят в неё, а не в
	// экран, и место строки клавиш занимает поле ввода (см. keyBar).
	cmd commandBar

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

	case runFinishedMsg:
		return m.applyRunFinished(msg), nil

	case commandMsg:
		return m.applyCommand(msg)

	case tea.KeyPressMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

// applyCommand — единственный переключатель, ведущий разобранную команду
// строки команды к тем же обработчикам, что и горячие клавиши: второго
// места запуска прогонов не появляется (startRun уже принимает вид прогона
// параметром, план 09-02). R/rest/clean зовут его же; A, commit, image и !
// наполняются задачами 2 и 3 этого плана тем же переключателем — второе
// место разбора команды не заводится.
func (m jobModel) applyCommand(msg commandMsg) (jobModel, tea.Cmd) {
	switch msg.Name {
	case "R":
		if m.canRun() && m.session.outcome.Failed != nil {
			return m.startRun(runRetry)
		}
	case "rest":
		if m.canRun() && m.session.outcome.Failed != nil {
			return m.startRun(runRest)
		}
	case "clean":
		if m.canRun() {
			return m.startRun(runClean)
		}
	case "q":
		return m, tea.Quit
	}
	return m, nil
}

// applyRunFinished разбирает итог одного из трёх прогонов задачи 3
// (RetryStepCmd/RestCmd/CleanCmd) и переводит петлю фикса в следующее
// состояние — строго по таблице строки подсказки контракта, по порядку
// прохождения: починил/не починил, догнал/не догнал, чисто/не чисто.
func (m jobModel) applyRunFinished(msg runFinishedMsg) jobModel {
	m.canceling = false

	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			// Отмена — не ошибка, а осознанное действие человека: экран
			// возвращается на предыдущее устойчивое состояние без клейма
			// отказа (символа/цвета) — контракт, «Долгие операции».
			m.phase = phaseLeftShell
			return m
		}
		// Поломка самого инструмента (docker, оборванный демон) не
		// превращает весь запуск в неудачу: человек уже получил шелл,
		// ценность уже доставлена — та же политика, что в обычном режиме.
		m.phase = phaseBlocked
		m.banner = msg.err.Error()
		return m
	}

	switch msg.kind {
	case runRetry:
		m.stepRows = stepRowsFromSession(m.session)
		if msg.outcome.Failed == nil {
			m.phase = phaseStepFixed
		} else {
			m.phase = phaseStepStillFails
		}
	case runRest:
		m.stepRows = stepRowsFromSession(m.session)
		if msg.outcome.Failed == nil {
			m.phase = phaseRestPassed
		} else {
			m.phase = phaseRestFailed
		}
	case runClean:
		// Чистый прогон гнал ВСЕ шаги джобы, а не хвост — отметки
		// пересобираются по его результату целиком.
		m.stepRows = stepRowsFromSession(m.session)
		if msg.outcome.Failed == nil {
			m.phase = phaseCleanGreen
		} else {
			m.phase = phaseCleanFails
		}
	}
	return m
}

// canRun истинно, когда экран не занят другой долгой операцией (подготовка,
// передача терминала, уже идущий прогон) — только тогда петля фикса может
// принять новую команду.
func (m jobModel) canRun() bool {
	switch m.phase {
	case phasePreparing, phaseHandingTerminal, phaseRunning:
		return false
	}
	return true
}

// startRun запускает один из трёх прогонов задачи 3 по виду kind — единый
// обработчик, который уже сегодня обслуживает клавишу перезапуска шага, а
// после плана 09-03 обслужит и командный режим (:rest, :clean) без второго
// места запуска прогонов.
func (m jobModel) startRun(kind runKind) (jobModel, tea.Cmd) {
	m.phase = phaseRunning
	switch kind {
	case runRetry:
		m.runLabel = "перезапуск шага"
		m.runOffset = m.session.outcome.FailedIndex - 1
		return m, m.session.RetryStepCmd(context.Background())
	case runRest:
		m.runLabel = RunLabelRest
		m.runOffset = m.session.outcome.FailedIndex
		return m, m.session.RestCmd(context.Background())
	case runClean:
		m.runLabel = RunLabelClean
		m.runOffset = 0
		return m, m.session.CleanCmd(context.Background())
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
		// e.Index считается от начала СРЕЗА, переданного в runner.RunSteps —
		// runOffset переводит его в абсолютный номер шага джобы. Для самого
		// первого прогона (PrepareCmd) срез — это все шаги целиком, а
		// runOffset остаётся нулевым значением по умолчанию, поэтому формула
		// работает без отдельной ветки.
		abs := m.runOffset + e.Index
		m.runStep = abs
		for i := range m.stepRows {
			switch {
			case m.stepRows[i].Index == abs:
				m.stepRows[i].Status = "running"
			case m.stepRows[i].Index < abs && m.stepRows[i].Status != "failed":
				m.stepRows[i].Status = "success"
			}
		}
		if m.cursor < 0 {
			m.cursor = abs - 1
		}
	case event.StepsSummary:
		// Итог обхода расставляет отметки уже через sessionReadyMsg/
		// runFinishedMsg (задача 3) — здесь разбирать нечего, но случай
		// назван явно, чтобы список событий читался как исчерпывающий.
	}
	return m
}

// updateKey разбирает нажатие клавиши экрана джобы. Пока строка команды
// активна, клавиши уходят в неё, а не в раскладку экрана — единственное
// исключение выше самого переключателя, потому что поле ввода обязано
// увидеть каждый символ, включая те, что совпадают с горячими клавишами
// экрана (например, "R" внутри набираемой команды).
func (m jobModel) updateKey(msg tea.KeyPressMsg) (jobModel, tea.Cmd) {
	if m.cmd.active {
		return m.updateCommandKey(msg)
	}

	switch {
	case key.Matches(msg, m.keys.Cancel):
		if m.phase == phasePreparing || m.phase == phaseRunning {
			m.session.Cancel()
			m.canceling = true
		}
		return m, nil

	case key.Matches(msg, m.keys.Command):
		// Двоеточие открывает строку команды и передаёт ей фокус (задача 1,
		// план 09-03) — тот же приём поля ввода Bubbles, что и у заставки
		// (internal/ui/splash.go): Focus() при создании, textinput.Blink
		// командой, чтобы курсор начал мигать сразу.
		m.cmd = newCommandBar()
		m.cmd.active = true
		return m, textinput.Blink

	case key.Matches(msg, m.keys.Shell) && m.phase == phaseImageReady:
		// Нажатие НЕ возвращает механизм передачи терминала сразу: сначала
		// фаза переводится в phaseHandingTerminal, и возвращается команда,
		// немедленно шлющая shellHandoffMsg — она гарантирует, что кадр с
		// подсказкой HintHandingTerminal нарисован до передачи терминала.
		m.phase = phaseHandingTerminal
		return m, func() tea.Msg { return shellHandoffMsg{} }

	case key.Matches(msg, m.keys.Retry) && m.canRun() && m.session.outcome.Failed != nil:
		// Клавиша перезапуска шага (FIXUI-01) — доступна и работает уже
		// здесь; :rest и :clean вводятся командным режимом плана 09-03, но
		// зовут тот же startRun.
		return m.startRun(runRetry)

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

// updateCommandKey обрабатывает нажатие клавиши, пока строка команды
// активна. Подтверждение разбирает набранное и шлёт commandMsg тем же
// переключателем, что и клавиши (applyCommand); отказ разбора остаётся
// строкой над подсказкой (keyBar) и НЕ закрывает поле — человек может
// поправить набранное и подтвердить снова. Отмена по привязке возврата
// закрывает поле, ничего не выполняя. Любая другая клавиша уходит в поле
// ввода как обычный ввод текста.
func (m jobModel) updateCommandKey(msg tea.KeyPressMsg) (jobModel, tea.Cmd) {
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

// keyBar собирает строку клавиш экрана джобы: шелл, повторить шаг, командный
// режим, назад, выход (перенос правок клавишей A подключает задача 2). Пока
// строка команды активна, на её месте — само поле ввода, а последняя ошибка
// разбора (если есть) показывается строкой НАД ним тем же приёмом, что и
// строка подсказки (RenderHint) — контракт требует, чтобы отказ не закрывал
// поле, и чтобы настоящая строка подсказки внизу экрана осталась на месте
// без исключений (она рисуется отдельно, в App.View).
func (m jobModel) keyBar() string {
	if m.cmd.active {
		var b strings.Builder
		if m.cmd.err != "" {
			b.WriteString(RenderHint(m.theme, m.cmd.err))
			b.WriteString("\n")
		}
		b.WriteString(m.cmd.input.View())
		return b.String()
	}
	return KeyBar(m.theme, m.keys.Shell, m.keys.Retry, m.keys.Command, m.keys.Back, m.keys.Quit)
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
	case m.phase == phaseRunning:
		return HintRunning(m.runLabel, m.runStep, len(m.stepRows))
	case m.phase == phaseBlocked && m.blockedCanceled:
		return "прервано пользователем — esc вернёт к списку джоб"
	case m.phase == phaseBlocked:
		return HintBlocked(m.banner)
	case m.phase == phaseHandingTerminal:
		return HintHandingTerminal()
	case m.phase == phaseLeftShell:
		return HintLeftShell()
	case m.phase == phaseStepFixed:
		return HintStepFixed(m.runOffset + 1)
	case m.phase == phaseStepStillFails:
		return HintStepStillFails(m.runOffset + 1)
	case m.phase == phaseRestPassed:
		return HintRestPassed()
	case m.phase == phaseRestFailed:
		return HintRestFailed(m.session.outcome.FailedIndex)
	case m.phase == phaseCleanGreen:
		return HintCleanGreen()
	case m.phase == phaseCleanFails:
		return HintCleanFails(m.session.outcome.FailedIndex)
	case m.phase == phaseImageReady:
		return HintImageReady(m.session.img.Ref)
	}
	return ""
}
