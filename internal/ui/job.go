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
	"github.com/f3rym/ci-shell/internal/textwidth"
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
	// phaseApplyConfirm — A/:A показал список изменённых файлов, перенос ещё
	// не подтверждён (задача 2, FIXUI-03).
	phaseApplyConfirm
	// phaseApplied — правки перенесены трёхсторонним мерджем, ещё не
	// закоммичены.
	phaseApplied
	// phaseCommitted — :commit зафиксировал перенесённое.
	phaseCommitted
)

// shellHandoffMsg гарантирует, что последний полный кадр с подсказкой
// HintHandingTerminal нарисован ДО того, как Bubble Tea снимет контроль над
// терминалом — иначе на экране остался бы зависший кадр спиннера или
// недорисованная строка (09-UI-SPEC.md, «Вход и выход», пункт 2).
type shellHandoffMsg struct{}

// openDetailMsg — просьба открыть колонку окружения·секретов·лога (Фаза 13,
// NAV-01…NAV-02): единственная колонка фазы, которая не приходит из сети,
// поэтому у неё нет своих данных для загрузки — сообщение существует ради
// того же единого правила, по которому открываются все остальные колонки
// (internal/ui/app.go, openDeeper).
type openDetailMsg struct{}

// detailView — вид правой колонки экрана джобы: окружение, секреты, лог
// шага, ровно в порядке idea-0.3.1 §6.
type detailView int

const (
	detailEnv detailView = iota
	detailSecrets
	detailLog
)

// nextDetail — следующий вид по кольцу (Фаза 15, план 15-02). Назад по
// видам ведёт то же самое кольцо: стрелка влево принадлежит ленте и уводит
// в колонку шагов (закон Фазы 13), и заводить ей второй смысл внутри
// колонки значило бы вернуть ровно ту путаницу, которую Фаза 13 убирала.
func nextDetail(v detailView) detailView {
	switch v {
	case detailEnv:
		return detailSecrets
	case detailSecrets:
		return detailLog
	default:
		return detailEnv
	}
}

// detailName — имя вида строчными буквами для строки клавиш и заголовка
// колонки; имена видов берутся ровно отсюда, второго перечня имён в файле
// не заводится.
func detailName(v detailView) string {
	switch v {
	case detailEnv:
		return "окружение"
	case detailSecrets:
		return "секреты"
	case detailLog:
		return "лог шага"
	}
	return ""
}

// jobModel — экран джобы: панели шагов, окружения и лога, вход в шелл, петля
// фикса.
type jobModel struct {
	session *Session
	// sessionGen — поколение сессии этого экрана: события чужих поколений
	// (мост предыдущей джобы остаётся подключённым к программе)
	// отбрасываются в update (WR-05 обзора v0.3.0).
	sessionGen int
	theme      Theme
	keys       KeyMap

	stepRows []stepRow
	envRows  []envRow
	// secretRows — строки вида «секреты» правой колонки (Фаза 15, план
	// 15-02): пересобираются там же, где envRows — вторым местом сборки не
	// заводится.
	secretRows []secretRow

	// cursor — номер выбранного шага (считая от нуля), общий для панели
	// шагов; двигают его ↑↓/jk. -1 — шаг ещё не выбирали (встроенный
	// предпросмотр лога пуст).
	cursor int

	// pane — какая из двух колонок экрана джобы сейчас в фокусе ленты (шаги
	// либо окружение·секреты·лог), по умолчанию колонка шагов (Фаза 13,
	// NAV-01…NAV-02): движение вверх/вниз в updateKey смотрит на это поле, а
	// не на то, какая панель отрисована — фокус раздаёт лента, а не сам
	// экран.
	pane columnID
	// detail — какой из трёх видов правой колонки сейчас показан (Фаза 15,
	// план 15-02, idea-0.3.1 §6): по умолчанию окружение. Переключается
	// стрелкой вправо, пока фокус в правой колонке (openDeeper).
	detail detailView
	// envCursor — курсор ОДНОЙ правой колонки (Фаза 13, план 13-01; Фаза 15,
	// план 15-02): в виде окружения ходит по envRows, в виде секретов — по
	// secretRows, в виде лога курсора нет вовсе. Колонка одна, а вид — это
	// то, что в ней сейчас показано; второй курсор в той же колонке был бы
	// вторым правилом навигации там, где Фаза 13 оставила одно. Прижимается
	// к длине списка при переключении вида и при движении.
	envCursor int
	// logv — встроенный предпросмотр лога шага (просмотрщик плана 15-01) в
	// виде «лог шага» правой колонки: строки обновляются только там, где лог
	// мог вырасти (refreshLog), а не на каждом кадре.
	logv logView

	// secret — поле ввода значения переменной секрета (Фаза 15, план 15-02,
	// JOB-02): пока оно активно, клавиши уходят ему, а место строки клавиш
	// занимает его вид (тем же приёмом, что и у cmd выше). filledHere —
	// человек уже вписывал значение на этом экране: готовая сессия с
	// недостающими переменными больше не открывает экран проводника сама,
	// пока этот признак поднят (человек выбрал заполнять на месте).
	secret secretPrompt
	// filledHere объявлено без выравнивания столбцом (gofmt в проекте не
	// запускается, .claude/CLAUDE.md) — человек уже вписывал значение на
	// этом экране.
	filledHere bool

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

	// apply — состояние экрана переноса правок (задача 2, план 09-03,
	// FIXUI-03): показ списка изменённых файлов, перенос трёхсторонним
	// мерджем, фиксация с полем ввода сообщения. Пока apply.stage не равна
	// applyIdle, клавиши уходят в неё же приёмом, что и у cmd выше.
	apply applyState

	// swapping — идёт подмена образа (задача 3, план 09-03, :image):
	// блокирует canRun ровно как и остальные долгие операции, пока тяга
	// нового образа и перезапуск контейнера не закончатся.
	swapping bool
	// swappedImage — заполняется imageSwappedMsg: новая ссылка на образ,
	// показанная HintImageReplaced ровно один раз после подмены, пока фаза
	// не сменится ещё раз.
	swappedImage string

	// execRunning/execCommand — идёт выполнение произвольной команды в
	// контейнере (:!, задача 3): подсказка временно замещается строкой о
	// выполняющейся команде. execView/execCode — панель лога переключена на
	// вывод команды (заголовок «ВЫВОД КОМАНДЫ»), execCode — её код выхода
	// для HintExecDone; execView сбрасывается выбором другого шага (↑↓) или
	// новым прогоном — тогда панель лога возвращается к хвосту шага.
	execRunning bool
	execCommand string
	execView    bool
	execCode    int

	// height — единственное, что хранит поле размера экрана джобы (Фаза 13,
	// план 13-02, пункт 7): ширина панелей больше не поле модели, а аргумент
	// columnView, посчитанный лентой (App.columnWidthFor) — второго расчёта
	// ширины здесь не заводится (panelWidths удалён).
	height int
}

// newJobModel строит модель экрана джобы поверх уже собранной сессии.
func newJobModel(session *Session, gen int, theme Theme, keys KeyMap) jobModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return jobModel{
		session:    session,
		sessionGen: gen,
		theme:      theme,
		keys:       keys,
		cursor:     -1,
		phase:      phasePreparing,
		spin:       sp,
		pane:       colSteps,
	}
}

// init запускает тик спиннера и подготовку сессии.
func (m jobModel) init() tea.Cmd {
	return tea.Batch(m.spin.Tick, m.session.PrepareCmd(context.Background()))
}

// setSize передаёт модели экрана джобы только высоту (Фаза 13, план 13-02,
// пункт 7): ширина панелей больше не хранится полем — она приходит
// аргументом от ленты прямо в columnView на каждый кадр, тем же приёмом,
// что и у остальных моделей-колонок.
func (m jobModel) setSize(height int) jobModel {
	m.height = height
	return m
}

// naturalWidth — часть общего контракта колонки (Фаза 17, FIT-02): желаемая
// ширина колонки id по её реальному содержимому. Шаги — та же формула, что
// stepLine вычитает при обрезке (RowIconGap+номер+1+команда), сложением, а
// не вычитанием. Правая колонка разбирает по m.detail: «окружение» и
// «секреты» — самая широкая строка ключ=значение уже загруженных данных;
// «лог» — готового содержимого для оценки нет до выбора шага (m.cursor < 0
// до первого падения), а лог — самая частая причина, по которой человеку
// нужна ШИРОКАЯ колонка (длинные команды сборки, пути) — фиксированное
// значение вдвое шире минимума, а не сам минимум, иначе FIT-02 «много —
// достаётся тому, кто хочет больше» никогда не сработало бы в пользу лога,
// у которого нет короткого «мало текста» состояния до выбора шага.
func (m jobModel) naturalWidth(id columnID) int {
	if id == colSteps {
		max := 0
		for _, r := range m.stepRows {
			w := 1 + RowIconGap + textwidth.Of(fmt.Sprintf("%d", r.Index)) + 1 + textwidth.Of(r.Command)
			if w > max {
				max = w
			}
		}
		if max < ColumnMin {
			max = ColumnMin
		}
		return max
	}

	switch m.detail {
	case detailSecrets:
		max := 0
		for _, r := range m.secretRows {
			w := textwidth.Of(r.Key) + 3 + textwidth.Of(r.Display)
			if w > max {
				max = w
			}
		}
		if max < ColumnMin {
			max = ColumnMin
		}
		return max
	case detailLog:
		return ColumnMin * 2
	default:
		max := 0
		for _, r := range m.envRows {
			w := textwidth.Of(r.Key) + 1 + textwidth.Of(r.Display)
			if w > max {
				max = w
			}
		}
		if max < ColumnMin {
			max = ColumnMin
		}
		return max
	}
}

// setColumnFocus — часть общего контракта колонки (Фаза 13): записывает,
// какая из двух колонок экрана джобы (шаги либо окружение·секреты·лог)
// сейчас в фокусе ленты — движение вверх/вниз в updateKey читает это поле.
func (m jobModel) setColumnFocus(id columnID) jobModel {
	m.pane = id
	return m
}

// openDeeper — часть общего контракта колонки (Фаза 13; наполнена Фазой 15,
// план 15-02): из колонки шагов возвращается команда, шлющая openDetailMsg —
// ровно как раньше; в правой колонке — переключает вид на следующий по
// кольцу (idea-0.3.1 §6: «по → правая панель переключает вид»). Это и есть
// углубление внутри колонки — тот же случай, что раскрытие ветки дерева,
// названный планом 13-01. Курсор прижимается к длине списка нового вида
// сразу же — второго места прижатия при переключении не заводится.
func (m jobModel) openDeeper() (jobModel, tea.Cmd) {
	if m.pane == colSteps {
		return m, func() tea.Msg { return openDetailMsg{} }
	}
	m.detail = nextDetail(m.detail)
	return m.clampDetailCursor(), nil
}

// clampDetailCursor прижимает курсор правой колонки к длине списка текущего
// вида — единственное место прижатия, зовётся и при переключении вида
// (openDeeper), и при движении (updateKey).
func (m jobModel) clampDetailCursor() jobModel {
	var n int
	switch m.detail {
	case detailEnv:
		n = len(m.envRows)
	case detailSecrets:
		n = len(m.secretRows)
	default:
		return m
	}
	if m.envCursor >= n {
		m.envCursor = n - 1
	}
	if m.envCursor < 0 {
		m.envCursor = 0
	}
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
		// Встроенный предпросмотр лога шага обновляется тиком спиннера,
		// ПОКА идёт долгая операция (прогон, тяга образа, произвольная
		// команда, подмена) — та же точка, где может расти вывод шага без
		// отдельного дискретного события на каждую строку. Копировать буфер
		// на каждый тик вне долгой операции значило бы копировать тысячи
		// строк ради кадра, в котором ничего не изменилось.
		if m.pulling || m.phase == phaseRunning || m.execRunning || m.swapping {
			lines, tail := m.logGrowthSource()
			m.logv = m.logv.setSource(tail, -1, "")
			m.logv = m.logv.setLines(lines)
		}
		return m, cmd

	case sessionReadyMsg:
		m.phase = phaseImageReady
		m.pulling = false
		// SwapImageCmd делегирует PrepareCmd, когда контейнера ещё не было
		// (Фаза 11, GUIDE-05) — сбрасывается и здесь, чтобы флаг не
		// залипал, если подготовка была запущена именно так.
		m.swapping = false
		m.stepRows = stepRowsFromSession(m.session)
		m.envRows = envRowsFromSession(m.session)
		// Строки секретов пересобираются там же, где строки окружения
		// (Фаза 15, план 15-02) — второго места сборки не появляется.
		m.secretRows = secretRowsFromSession(m.session)
		// Готовность сессии — одна из точек, где встроенный предпросмотр
		// лога шага мог вырасти (Фаза 15, план 15-02): маска уже собрана к
		// первой строке тяги образа (internal/ui/session.go).
		readyLines, readyTail := m.logGrowthSource()
		m.logv = m.logv.setSource(readyTail, -1, "")
		m.logv = m.logv.setLines(readyLines)
		if msg.outcome.Failed != nil {
			m.cursor = msg.outcome.FailedIndex - 1
		}
		// Готовая сессия с непустым списком недостающих переменных больше
		// не молчит (Фаза 11, GUIDE-03): человек узнаёт о поломке из
		// экрана сразу, а не из отказа шага через минуту. Исключение —
		// человек уже вписывал значение на этом экране (filledHere, Фаза
		// 15, план 15-02): он выбрал заполнять секреты на месте, и
		// открывать поверх его выбора предложение уйти в редактор после
		// каждого повтора подготовки значило бы спорить с ним раз в
		// тридцать секунд. Команда открытия редактора (:secrets) и
		// действие проводника (actionEditSecrets) при этом не трогаются —
		// путь через редактор остаётся.
		if missing := m.session.environment.Missing; len(missing) > 0 && !m.filledHere {
			g := guideForMissing(missing, m.session.environment.SecretsPath)
			return m, func() tea.Msg { return guideMsg{Guide: g} }
		}
		return m, nil

	case sessionFailedMsg:
		m.canceling = false
		m.swapping = false
		if msg.canceled {
			m.phase = phaseBlocked
			m.blockedCanceled = true
			m.banner = msg.reason
			return m, nil
		}
		// Сорвавшаяся подготовка ищет ситуацию в словаре распознавания с
		// местом «подготовка» (Фаза 11): найдена — экран открывает
		// проводник и остаётся в фазе подготовки, чтобы после ответа было
		// куда вернуться; не найдена — прежнее поведение фазы 9.
		if g, ok := guideFor(msg.err, whereSession, m.session.ref); ok {
			return m, func() tea.Msg { return guideMsg{Guide: g} }
		}
		m.phase = phaseBlocked
		m.blockedCanceled = false
		m.banner = msg.reason
		return m, nil

	case EventMsg:
		// Событие чужого поколения — от сессии предыдущей джобы, чей мост
		// всё ещё подключён к программе: применять его к этому экрану значит
		// мигать «тяну образ» и двигать статусы шагов чужой джобы (WR-05).
		if msg.Gen != m.sessionGen {
			return m, nil
		}
		// Событие фазы 8 — одна из точек, где лог мог вырасти (Фаза 15,
		// план 15-02): приходит реже, чем перерисовка кадра, и как раз
		// тогда, когда в буфере действительно что-то новое.
		m = m.applyEvent(msg.Event)
		eventLines, eventTail := m.logGrowthSource()
		m.logv = m.logv.setSource(eventTail, -1, "")
		m.logv = m.logv.setLines(eventLines)
		return m, nil

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
		// Итог прогона — одна из точек, где лог мог вырасти (Фаза 15, план
		// 15-02).
		m = m.applyRunFinished(msg)
		runLines, runTail := m.logGrowthSource()
		m.logv = m.logv.setSource(runTail, -1, "")
		m.logv = m.logv.setLines(runLines)
		return m, nil

	case captureDoneMsg:
		m.apply = newApplyState(msg.patch, msg.stats, msg.ahead, msg.aheadKnown)
		m.phase = phaseApplyConfirm
		// Список файлов занимает ленту целиком (fullFrame) — устаревший
		// вывод последней команды :! не должен маячить в подсказке позади
		// него.
		m.execView = false
		return m, nil

	case captureEmptyMsg:
		// Пустой патч: подсказка HintNoChanges, панель не открывается —
		// приём тот же, что и у отказа переноса ниже (m.banner), приглушённым
		// стилем, потому что это честное «нечего переносить», а не поломка.
		m.banner = HintNoChanges()
		m.blockedCanceled = true
		return m, nil

	case captureFailedMsg:
		// Сорвавшийся перенос правок идёт в словарь распознавания с местом
		// «перенос правок», а не «подготовка» (Фаза 11): одна и та же
		// ошибка отсутствия рабочей копии означает здесь другое — ровно
		// ради этого различения место whereApply было заведено планом
		// 11-01. Найденное описание дополняется путём сохранённого патча.
		if g, ok := guideFor(msg.err, whereApply, m.session.ref); ok {
			if path := m.session.patchPath(); path != "" {
				g.List = append([]string{fmt.Sprintf("патч сохранён: %s", path)}, g.List...)
				g.Hint = HintApplyOutsideRepo(path)
			}
			return m, func() tea.Msg { return guideMsg{Guide: g} }
		}
		m.banner = msg.reason
		m.blockedCanceled = false
		return m, nil

	case appliedMsg:
		m.apply.stage = applyIdle
		m.phase = phaseApplied
		m.banner = ""
		return m, nil

	case applyFailedMsg:
		m.apply.stage = applyIdle
		m.phase = phaseLeftShell
		m.banner = msg.reason
		m.blockedCanceled = false
		return m, nil

	case committedMsg:
		m.apply.stage = applyIdle
		m.phase = phaseCommitted
		m.banner = ""
		return m, nil

	case commitFailedMsg:
		// Отказ фиксации — однострочный баннер цветом отказа и возврат в
		// стадию покоя; фаза остаётся phaseApplied, потому что перенесённое
		// никуда не делось (задача 2, done-критерий плана).
		m.apply.stage = applyIdle
		m.banner = msg.reason
		m.blockedCanceled = false
		return m, nil

	case execDoneMsg:
		m.execRunning = false
		m.execCode = msg.code
		// Конец произвольной команды — одна из точек, где лог мог вырасти
		// (Фаза 15, план 15-02).
		execLines, execTail := m.logGrowthSource()
		m.logv = m.logv.setSource(execTail, -1, "")
		m.logv = m.logv.setLines(execLines)
		return m, nil

	case imageSwappedMsg:
		// Новый контейнер ничего не исполнял — отметки шагов сбрасываются в
		// ожидание (glyph ○), а не остаются со значениями прошлого прогона.
		m.swapping = false
		m.swappedImage = msg.image
		m.execView = false
		m.banner = ""
		for i := range m.stepRows {
			m.stepRows[i].Status = "created"
		}
		m.cursor = -1
		m.phase = phaseImageReady
		// Подмена образа — одна из точек, где лог мог вырасти (Фаза 15,
		// план 15-02): новый контейнер уже писал в буфер во время подъёма.
		swapLines, swapTail := m.logGrowthSource()
		m.logv = m.logv.setSource(swapTail, -1, "")
		m.logv = m.logv.setLines(swapLines)
		return m, nil

	case imageSwapFailedMsg:
		// Отказ подмены — однострочный баннер цветом отказа, состояние
		// экрана не меняется (задача 3, done-критерий плана).
		m.swapping = false
		m.banner = msg.reason
		m.blockedCanceled = false
		return m, nil

	case secretsEditedMsg:
		// Возврат из редактора обязан сопровождаться полной перерисовкой —
		// частичная оставляет мусор на экране (тот же жёсткий контракт, что
		// и при возврате из шелла).
		if msg.Err != nil {
			m.banner = HintSecretsFailed(msg.Err.Error())
			m.blockedCanceled = false
			return m, tea.ClearScreen
		}
		// Возврат прав файлу секретов к 0600 и повтор подготовки
		// подтверждаются человеку баннером, а не молчанием: контракт фазы 11
		// требует сообщить об этом состоянии (WR-10 обзора v0.3.0).
		m.banner = HintSecretsEdited(msg.Path)
		m.blockedCanceled = true
		m.phase = phasePreparing
		return m, tea.Batch(tea.ClearScreen, m.session.RestartCmd(context.Background()))

	case secretEnteredMsg:
		// Поле уже закрыто и вычищено секундой раньше (secretPrompt.updateKey)
		// — здесь только запуск записи, единственное место вызова
		// saveSecretCmd на экране джобы.
		m.secret = secretPrompt{}
		return m, saveSecretCmd(m.session.ref.Host, m.session.ref.ProjectPath, msg.Key, msg.Value)

	// secretSavedMsg: ошибка — однострочный баннер цветом отказа, состояние
	// экрана не меняется, вписать можно снова; успех — баннер приглушённым
	// стилем (это не поломка), признак «уже вписывал» поднимается, фаза
	// переводится в подготовку и запускается ПОВТОР ПОДГОТОВКИ, а не
	// подмена переменной в памяти: окружение собирается доменом из четырёх
	// слоёв, контейнер уже поднят со своим набором переменных, а
	// маскировщик лога построен по старому окружению — подмена в памяти
	// дала бы экран, на котором переменная есть, и контейнер, в котором её
	// нет. Тот же путь уже проходит возврат из редактора (secretsEditedMsg).
	case secretSavedMsg:
		if msg.Err != nil {
			m.banner = HintSecretFailed(msg.Err.Error())
			m.blockedCanceled = false
			return m, nil
		}
		m.banner = HintSecretSaved(msg.Key, msg.Path)
		m.blockedCanceled = true
		m.filledHere = true
		m.phase = phasePreparing
		return m, m.session.RestartCmd(context.Background())

	case pullFinishedMsg:
		if msg.Err != nil {
			if g, ok := guideFor(msg.Err, whereSession, m.session.ref); ok {
				return m, func() tea.Msg { return guideMsg{Guide: g} }
			}
			m.banner = HintPullFailed(msg.Err.Error())
			m.blockedCanceled = false
			return m, nil
		}
		m.banner = HintPullDone()
		m.blockedCanceled = true
		return m, nil

	case guideDoneMsg:
		return m.applyGuideDone(msg)

	case commandMsg:
		return m.applyCommand(msg)

	case tea.PasteMsg:
		// Вставка приходит отдельным сообщением, а не клавишей (скобочная
		// вставка Bubble Tea v2), поэтому updateKey её не видит вовсе. На
		// этом экране два поля ввода, и вставка уходит тому, которое сейчас
		// открыто: строке команды либо сообщению коммита (одновременно они
		// не активны никогда — applyIdle-проверка в updateKey).
		if m.cmd.active {
			var cmd tea.Cmd
			m.cmd.input, cmd = m.cmd.input.Update(msg)
			return m, cmd
		}
		if m.apply.stage == applyMessage {
			var cmd tea.Cmd
			m.apply.msgInput, cmd = m.apply.msgInput.Update(msg)
			return m, cmd
		}
		if m.secret.active {
			// Вставка секрета из менеджера паролей — обычный способ его
			// ввести (Фаза 15, план 15-02): без этой ветви она молча уходила
			// бы в никуда.
			var cmd tea.Cmd
			m.secret.input, cmd = m.secret.input.Update(msg)
			return m, cmd
		}
		return m, nil

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
		// Условия «упал хоть один шаг» здесь больше нет: после успешного
		// перезапуска упавшего шага нет, а прогнать оставшиеся всё ещё надо —
		// ровно это в этот момент и советует HintStepFixed (WR-07). Есть ли
		// что прогонять, решает сама сессия (RestCmd, errNoStepsAfter).
		if m.canRun() {
			return m.startRun(runRest)
		}
	case "clean":
		if m.canRun() {
			return m.startRun(runClean)
		}
	case "A":
		return m.startCapture()
	case "commit":
		return m.startCommitMessage()
	case "image":
		return m.startSwapImage(msg.Arg)
	case "!":
		return m.startExec(msg.Arg)
	case "secrets":
		// Тот же обработчик, что и подтверждение экрана недостающих
		// значений (guideDoneMsg{Action: actionEditSecrets}, Фаза 11,
		// GUIDE-03) — второго места запуска редактора не появляется.
		// Занятость проверяется как и у соседних веток (WR-12): редактор
		// забирает терминал, а фоновая операция продолжала бы писать в лог.
		if m.idle() {
			return m, m.session.EditSecretsCmd(context.Background())
		}
	case "pull":
		if m.idle() {
			return m, m.session.PullCmd(context.Background())
		}
	case "env":
		return m.startEnvScreen()
	case "q":
		// Выход идёт через корневую модель: только она знает про уборку
		// сессии (internal/ui/app.go, quitCmd) — CR-01.
		return m, func() tea.Msg { return quitMsg{} }
	}
	return m, nil
}

// startEnvScreen открывает полноэкранное окружение джобы (:env, Фаза 11,
// GUIDE-05) — экран проводника формы списка. Строки уже собраны
// envRowsFromSession через единственную точку решения о показе значения
// (render.DisplayValue) — собственной ветки показа здесь нет.
func (m jobModel) startEnvScreen() (jobModel, tea.Cmd) {
	lines := make([]string, 0, len(m.envRows))
	for _, r := range m.envRows {
		lines = append(lines, fmt.Sprintf("%s=%s", r.Key, r.Display))
	}
	g := guide{
		Kind:  guideList,
		Where: whereSession,
		Title: fmt.Sprintf("окружение джобы (%d)", len(m.envRows)),
		List:  lines,
		Hint:  HintEnvScreen(len(m.envRows)),
	}
	return m, func() tea.Msg { return guideMsg{Guide: g} }
}

// applyGuideDone разбирает ответ экрана проводника на экране джобы (Фаза
// 11): каждое действие ведёт к той же функции сессии, что и первый её
// запуск — второго места не появляется ни для одной из них.
func (m jobModel) applyGuideDone(msg guideDoneMsg) (jobModel, tea.Cmd) {
	// Ответ проводника не принимается, пока экран занят операцией, которую
	// повтор подготовки снёс бы из-под ног (WR-12): RestartCmd снимает
	// контейнер и запускает подготовку заново — поверх идущего прогона это
	// два PrepareCmd одновременно и снос контейнера из-под работающего
	// docker exec. Фаза подготовки в список занятости здесь НЕ входит
	// намеренно: проводник и открывается из сорвавшейся подготовки, и его
	// ответ обязан дойти.
	if !m.guideActionable() {
		return m, nil
	}

	switch msg.Action {
	case actionSaveToken:
		host := m.session.ref.Host
		// saveToken (internal/ui/guide.go, Фаза 14) — единственная точка
		// сохранения ключа в пакете интерфейса: прямой вызов token.Save
		// отсюда снят той же правкой, что сняла его из заставки.
		if path, err := saveToken(host, msg.Value); err != nil {
			m.banner = HintTokenNotSaved()
		} else {
			m.banner = HintTokenSaved(path)
		}
		m.blockedCanceled = true
		m.phase = phasePreparing
		return m, m.session.RestartCmd(context.Background())
	case actionSaveDataDir:
		// Сохранённый каталог данных подтверждается человеку баннером, а не
		// молчанием: контракт фазы 11 требует сообщить о смене (WR-10).
		m.banner = HintDataDirSaved(msg.Value)
		m.blockedCanceled = true
		m.phase = phasePreparing
		return m, m.session.RestartCmd(context.Background())
	case actionRetryPrepare:
		m.phase = phasePreparing
		return m, m.session.RestartCmd(context.Background())
	case actionSetImage:
		m.swapping = true
		m.banner = HintImageSet(msg.Value)
		m.blockedCanceled = true
		return m, m.session.SwapImageCmd(context.Background(), msg.Value)
	case actionEditSecrets:
		return m, m.session.EditSecretsCmd(context.Background())
	case actionPull:
		return m, m.session.PullCmd(context.Background())
	case actionRetryApply:
		return m.startCapture()
	}
	return m, nil
}

// startCapture запускает CaptureCmd — единственное место запуска переноса
// правок и для клавиши A, и для команды :A (второго места, как и у
// startRun, не появляется).
func (m jobModel) startCapture() (jobModel, tea.Cmd) {
	if !m.canRun() || m.apply.stage != applyIdle {
		return m, nil
	}
	return m, m.session.CaptureCmd(context.Background())
}

// startCommitMessage открывает поле ввода сообщения коммита — команда
// :commit. Доступно только после переноса (phaseApplied), пока правки не
// зафиксированы: до переноса коммитить нечего, а после фиксации — уже
// нечего коммитить второй раз.
func (m jobModel) startCommitMessage() (jobModel, tea.Cmd) {
	if m.phase != phaseApplied || m.apply.stage != applyIdle {
		return m, nil
	}
	m.apply.stage = applyMessage
	m.apply.msgInput = newMessageInput()
	m.apply.err = ""
	return m, textinput.Blink
}

// startExec запускает произвольную команду command в контейнере джобы —
// команда :!<команда> (задача 3, FIXUI-05). Единственное место запуска
// ExecCmd, ровно как startCapture — единственное место запуска CaptureCmd.
func (m jobModel) startExec(command string) (jobModel, tea.Cmd) {
	if !m.canRun() {
		return m, nil
	}
	m.execRunning = true
	m.execCommand = command
	m.execView = true
	m.banner = ""
	return m, m.session.ExecCmd(context.Background(), command)
}

// startSwapImage запускает подмену образа джобы — команда :image <образ>
// (задача 3, FIXUI-05). Блокирующего вопроса нет: замена обратима повторной
// командой (Copywriting Contract контракта), поэтому startSwapImage
// запускается сразу, без промежуточной стадии согласия.
func (m jobModel) startSwapImage(image string) (jobModel, tea.Cmd) {
	// Проверяется незанятость, а не готовность: подмена образа осмысленна и
	// до первого контейнера — сорвавшуюся на образе подготовку она повторяет
	// с новым образом (SwapImageCmd, Фаза 11, GUIDE-05).
	if !m.idle() {
		return m, nil
	}
	m.swapping = true
	m.banner = ""
	return m, m.session.SwapImageCmd(context.Background(), image)
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

// idle истинно, когда экран не занят долгой операцией (подготовка, передача
// терминала, уже идущий прогон, подмена образа, выполнение произвольной
// команды). Про готовность сессии idle ничего не утверждает — это делает
// canRun ниже.
func (m jobModel) idle() bool {
	switch m.phase {
	case phasePreparing, phaseHandingTerminal, phaseRunning:
		return false
	}
	return !m.swapping && !m.execRunning
}

// cancelable — идёт ли долгая операция, которую Ctrl-C отменяет вместо
// выхода из программы. Единственное место, где этот вопрос решается:
// корневая модель спрашивает его же (internal/ui/app.go, cancelsRun), потому
// что иначе она либо отняла бы отмену у экрана джобы, либо оставила бы
// Ctrl-C без выхода везде, где отменять нечего.
func (m jobModel) cancelable() bool {
	return m.phase == phasePreparing || m.phase == phaseRunning
}

// guideActionable — можно ли принять ответ экрана проводника: занятость без
// фазы подготовки (см. applyGuideDone).
func (m jobModel) guideActionable() bool {
	switch m.phase {
	case phaseHandingTerminal, phaseRunning:
		return false
	}
	return !m.swapping && !m.execRunning
}

// canRun истинно, когда экран не занят другой долгой операцией И сессия
// действительно готова — контейнер поднят, код материализован. Одной
// незанятости мало: после сорвавшейся подготовки экран уходит в
// phaseBlocked, где занятости нет, а контейнера и чекаута ещё нет тоже, и
// команды вроде :clean, :!<команда> и A разыменовывали бы nil прямо в
// горутине команды — то есть вне единственного перехвата проекта (CR-03
// обзора v0.3.0).
func (m jobModel) canRun() bool {
	return m.idle() && m.session.Ready()
}

// startRun запускает один из трёх прогонов задачи 3 по виду kind — единый
// обработчик, который уже сегодня обслуживает клавишу перезапуска шага, а
// после плана 09-03 обслужит и командный режим (:rest, :clean) без второго
// места запуска прогонов.
func (m jobModel) startRun(kind runKind) (jobModel, tea.Cmd) {
	m.phase = phaseRunning
	// Новый прогон — панель лога возвращается к хвосту шага, а не остаётся
	// на выводе последней команды :! (если она была).
	m.execView = false
	switch kind {
	case runRetry:
		m.runLabel = "перезапуск шага"
		m.runOffset = m.session.outcome.FailedIndex - 1
		return m, m.session.RetryStepCmd(context.Background())
	case runRest:
		m.runLabel = RunLabelRest
		// Смещение — номер последнего исполненного шага, то же, что возьмёт
		// сама RestCmd: после успешного перезапуска упавшего шага уже нет
		// (WR-07).
		m.runOffset = m.session.LastStep()
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
	if m.secret.active {
		// Поле ввода значения секрета (Фаза 15, план 15-02, JOB-02) —
		// третье поле ввода экрана, той же ветвью-исключением, что и у
		// строки команды и у экрана переноса правок. Три поля ввода экрана
		// никогда не активны одновременно, и это держится проверками, а не
		// удачей: cmd.active выше и apply.stage != applyIdle ниже не могут
		// стать истинными, пока secret.active истинно (startFill —
		// единственное место открытия поля, и оно не зовётся из веток,
		// проверяющих эти два условия).
		var cmd tea.Cmd
		m.secret, cmd = m.secret.updateKey(msg, m.keys)
		return m, cmd
	}
	if m.cmd.active {
		return m.updateCommandKey(msg)
	}
	if m.apply.stage != applyIdle {
		return m.updateApplyKey(msg)
	}

	switch {
	case key.Matches(msg, m.keys.Cancel):
		// До этой ветви Ctrl-C доходит только тогда, когда отменять есть что:
		// корневая модель пропускает его сюда ровно по этому условию
		// (cancelsRun), а во всех остальных случаях сама завершает программу.
		// Проверка оставлена и здесь — она называет условие, по которому
		// экран забирает клавишу себе, и второго его толкования не заводит.
		if m.cancelable() {
			m.session.Cancel()
			m.canceling = true
		}
		return m, nil

	case key.Matches(msg, m.keys.Apply):
		// Клавиша переноса правок (FIXUI-03) — доступна и работает уже
		// здесь; :A вводится командным режимом, но зовёт тот же startCapture.
		return m.startCapture()

	case key.Matches(msg, m.keys.Command):
		// Двоеточие открывает строку команды и передаёт ей фокус (задача 1,
		// план 09-03) — общим хелпером openCommandBar (internal/ui/command.go),
		// тем же, что и на экране списка джоб: второго места открытия строки
		// команды в проекте нет.
		var cmd tea.Cmd
		m.cmd, cmd = openCommandBar()
		return m, cmd

	// Вход в шелл висит на готовности сессии, а не на одной фазе (WR-08
	// обзора v0.3.0): после первого выхода фаза становится phaseLeftShell,
	// после R — phaseStepFixed/phaseStepStillFails, и подсказки этих самых
	// фаз прямо велят «вернитесь в контейнер (s)».
	case key.Matches(msg, m.keys.Shell) && m.canRun():
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

	case key.Matches(msg, m.keys.Log) && m.pane == colDetail && m.detail == detailLog && m.cursor >= 0:
		// Клавиша открытия полноэкранного просмотрщика (Фаза 15, план
		// 15-02, JOB-04) — работает только в виде «лог шага»; второго места
		// сборки openLogMsg на экране джобы не появляется. m.cursor >= 0
		// (Фаза 15, план 15-03, LOG-05) — без выбранного шага открытие
		// показало бы заголовок «ЛОГ ШАГА 0» с бессмысленным содержимым.
		return m, m.openLogCmd()

	case key.Matches(msg, m.keys.Up):
		// Движение смотрит на m.pane (Фаза 13) и, внутри правой колонки, на
		// m.detail (Фаза 15, план 15-02): курсор один на все виды, в виде
		// окружения он ходит по envRows, в виде секретов — по secretRows, в
		// виде лога курсора нет вовсе.
		if m.pane == colDetail {
			if m.detail == detailLog {
				return m, nil
			}
			if m.envCursor > 0 {
				m.envCursor--
			}
			return m, nil
		}
		if m.cursor > 0 {
			m.cursor--
		}
		// Выбор другого шага — панель лога возвращается к его хвосту, а не
		// остаётся на выводе последней команды :! (задача 3).
		m.execView = false
		// Смена курсора — тоже точка роста в смысле LOG-05, только растёт не
		// содержимое, а то, ЧЕЙ это сегмент (Фаза 15, план 15-03): без
		// обновления здесь предпросмотр показывал бы лог прежде выбранного
		// шага до следующего события сессии, а не шага под курсором прямо
		// сейчас.
		upLines, upTail := m.logGrowthSource()
		m.logv = m.logv.setSource(upTail, -1, "")
		m.logv = m.logv.setLines(upLines)
		return m, nil

	case key.Matches(msg, m.keys.Down):
		if m.pane == colDetail {
			if m.detail == detailLog {
				return m, nil
			}
			n := len(m.envRows)
			if m.detail == detailSecrets {
				n = len(m.secretRows)
			}
			if m.envCursor < n-1 {
				m.envCursor++
			}
			return m, nil
		}
		if m.cursor < len(m.stepRows)-1 {
			m.cursor++
		}
		m.execView = false
		// Та же точка роста, что и у стрелки вверх (LOG-05) — курсор колонки
		// шагов сдвинулся, встроенный предпросмотр обязан показать сегмент
		// шага под ним сразу, не дожидаясь следующего события сессии.
		downLines, downTail := m.logGrowthSource()
		m.logv = m.logv.setSource(downTail, -1, "")
		m.logv = m.logv.setLines(downLines)
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

// updateApplyKey обрабатывает нажатие клавиши, пока экран переноса правок
// открыт (apply.stage != applyIdle) — три разных набора клавиш по стадиям
// (FIXUI-03): согласие на перенос (y/n), ввод сообщения коммита (обычный
// текстовый ввод + ⏎), согласие на фиксацию (y/n). Без явного согласия на
// каждой из двух y/n-стадий не пишется ничего — тот же предохранитель, что
// и у переноса правок обычного режима (cmd/ci/main.go).
func (m jobModel) updateApplyKey(msg tea.KeyPressMsg) (jobModel, tea.Cmd) {
	switch m.apply.stage {
	case applyConfirm:
		switch msg.String() {
		case "y":
			if !m.apply.canApply() {
				// Коммита джобы нет в этом репозитории — базы для
				// трёхстороннего мерджа нет, перенос не предлагается вовсе
				// (задача 2, пункт 9 плана): согласие ничего не запускает.
				return m, nil
			}
			return m, m.session.ApplyCmd(context.Background())
		case "n":
			m.banner = m.apply.discardedNotice()
			m.blockedCanceled = true
			m.apply = applyState{}
			return m, nil
		}
		if key.Matches(msg, m.keys.Back) {
			m.banner = m.apply.discardedNotice()
			m.blockedCanceled = true
			m.apply = applyState{}
		}
		return m, nil

	case applyMessage:
		if msg.String() == "enter" {
			text := strings.TrimSpace(m.apply.msgInput.Value())
			if text == "" {
				m.apply.err = emptyMessageNotice()
				return m, nil
			}
			m.apply.err = ""
			m.apply.msgInput.SetValue(text)
			m.apply.stage = applyCommitConfirm
			return m, nil
		}
		if key.Matches(msg, m.keys.Back) {
			m.apply.stage = applyIdle
			m.apply.msgInput = textinput.Model{}
			m.apply.err = ""
			return m, nil
		}
		var cmd tea.Cmd
		m.apply.msgInput, cmd = m.apply.msgInput.Update(msg)
		return m, cmd

	case applyCommitConfirm:
		switch msg.String() {
		case "y":
			return m, m.session.CommitCmd(context.Background(), m.apply.msgInput.Value())
		case "n":
			m.apply.stage = applyMessage
			return m, nil
		}
		if key.Matches(msg, m.keys.Back) {
			m.apply.stage = applyMessage
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

// stepsPanel рисует тело КОЛОНКИ шагов без заголовка (Фаза 13, план 13-02):
// заголовок «ШАГИ» рисует теперь корневая модель (App.columnView) уточнением
// заголовка колонки — второй заголовок здесь был бы мусором. Курсор
// инверсией — только при focused, иначе на экране был бы курсор сразу в
// двух колонках (шаги и окружение·секреты·лог).
func (m jobModel) stepsPanel(width int, focused bool) string {
	var b strings.Builder
	for i, r := range m.stepRows {
		b.WriteString(m.stepLine(r, i == m.cursor && focused, width))
		b.WriteString("\n")
	}
	return lipgloss.NewStyle().Width(width).Render(strings.TrimRight(b.String(), "\n"))
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

// envPanel рисует тело КОЛОНКИ окружения без собственного заголовка (Фаза
// 13, план 13-02): заголовок «ОКРУЖЕНИЕ (N)» рисовала панель раньше, теперь
// его место заняла лента (App.columnView) — второй заголовок здесь был бы
// мусором. Значение — через уже принятое решение о показе (envRow.Display,
// посчитано render.DisplayValue при сборке envRowsFromSession) — второй
// точки решения о печати значения здесь нет (T-13-06). Курсор строки
// (envCursor, заведён планом 13-01) рисуется инверсией только при focused —
// вне фокуса ленты второго курсора на экране быть не должно; заполнение
// незаполненных скрытых значений прямо на строке — Фаза 15, здесь курсор
// только виден.
func (m jobModel) envPanel(width int, focused bool) string {
	if len(m.envRows) == 0 {
		return fmt.Sprintf("не удалось собрать окружение: %s", "переменных нет")
	}

	var b strings.Builder
	for i, r := range m.envRows {
		line := Truncate(fmt.Sprintf("%s=%s", r.Key, r.Display), width)
		if i == m.envCursor && focused {
			line = m.theme.Selected.Render(line + " " + GlyphCursor)
		}
		b.WriteString(line)
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
	return lipgloss.NewStyle().Width(width).Render(strings.TrimRight(b.String(), "\n"))
}

// secretsPanel рисует тело вида «секреты» правой колонки (Фаза 15, план
// 15-02): строки секретов (secretRow.line), под ними приглушённая строка с
// путём файла секретов и напоминанием, что его по-прежнему можно открыть в
// редакторе (Фаза 11 не отменяется); при пустом списке — одна честная
// строка «у этой джобы нет скрытых переменных».
func (m jobModel) secretsPanel(width int, focused bool) string {
	var b strings.Builder
	if len(m.secretRows) == 0 {
		b.WriteString(HintSecretsNone())
	} else {
		for i, r := range m.secretRows {
			b.WriteString(r.line(m.theme, width, i == m.envCursor && focused))
			b.WriteString("\n")
		}
	}
	b.WriteString(m.theme.Muted.Render(HintSecretsFile(m.session.environment.SecretsPath)))
	return lipgloss.NewStyle().Width(width).Render(strings.TrimRight(b.String(), "\n"))
}

// logDetailPanel рисует тело вида «лог шага» правой колонки: окно строк
// встроенного предпросмотра и под ним его строка положения — тот же
// просмотрщик (internal/ui/logview.go, план 15-01), что и у полноэкранного
// оверлея, второй прокрутки в проекте не заводится (LOG-01). Ширина
// приходит аргументом от колонки на каждый кадр, тем же приёмом, что и у
// остальных видов; setSize здесь безопасен даже когда поле пустое —
// logView простое значение без компонентов Bubbles.
//
// Без выбранного шага (m.cursor < 0 — джоба прошла целиком без единого
// падения, курсор ни разу не сдвигался) тело честно отказывается открывать
// логи вслепую (Фаза 15, план 15-03): показывать лог без выбранного шага
// значило бы показать чужой сегмент или общий поток джобы — ровно то
// поведение, которое убирает LOG-05.
func (m jobModel) logDetailPanel(width int) string {
	if m.cursor < 0 {
		return m.theme.Muted.Render("шаг ещё не выбран — движение курсора в колонке шагов покажет его лог")
	}
	lv := m.logv.setSize(width, m.logDetailHeight())
	return lv.view(m.theme) + "\n" + lv.statusLine(m.theme)
}

// logDetailHeight — высота встроенного предпросмотра лога шага: высота
// колонки минус одна строка его собственной строки положения (LOG-04).
func (m jobModel) logDetailHeight() int {
	h := m.height - 1
	if h < 0 {
		h = 0
	}
	return h
}

// logGrowthSource — сегмент лога ИМЕННО шага под курсором колонки шагов и
// признак его вытеснения, для точки роста встроенного предпросмотра (Фаза
// 15, план 15-03, LOG-05). Она больше не отдаёт «буфер целиком», а режет
// его по шагу под курсором: без выбранного шага (m.cursor < 0, или курсор
// вышел за длину m.stepRows — та же защита от рассинхронизации, что уже
// делает cursorAt) сегмента не существует, и подмена его общим потоком
// была бы ровно тем поведением, которое LOG-05 убирает — тогда возвращаются
// нулевые значения. Иначе берётся m.stepRows[m.cursor].Index (абсолютный
// номер шага — то же поле, что уже читает cursorAt и openLogCmd, один
// источник истины) и передаётся в m.session.StepLog; признак «шаг вообще
// запускался» в возвращаемое значение не входит — вызывающий код
// воспринимает «не запускался» и «запускался без вывода» одинаково честно:
// пустой сегмент. Подпись «чем дотянуть» по-прежнему пуста и никак не
// меняется этой задачей — у сегмента шага, как и у всего локального буфера
// до этой задачи, начала может не быть (это теперь означает признак
// вытеснения ИМЕННО ЕГО, а не всего буфера), а команды дотянуть его нет.
// Формула общая, а сами вызовы m.logv.setSource/setLines инлайнятся в
// каждой точке роста отдельно — там, где лог мог вырасти, а не на каждом
// кадре; шесть уже существующих мест не редактируются этой задачей, они
// получают новый результат бесплатно.
func (m jobModel) logGrowthSource() (lines []string, tail bool) {
	if m.cursor < 0 || m.cursor >= len(m.stepRows) {
		return nil, false
	}
	lines, tail, _ = m.session.StepLog(m.stepRows[m.cursor].Index)
	return lines, tail
}

// currentSecretRow — строка вида «секреты» под курсором, если она есть.
func (m jobModel) currentSecretRow() (secretRow, bool) {
	if m.envCursor < 0 || m.envCursor >= len(m.secretRows) {
		return secretRow{}, false
	}
	return m.secretRows[m.envCursor], true
}

// fillsSecret — можно ли сейчас вписать значение переменной (Фаза 15, план
// 15-02, JOB-02): фокус в правой колонке (focused), вид «секреты», под
// курсором есть строка и экран не занят долгой операцией — та же проверка
// незанятости, что стоит у открытия редактора секретов (applyCommand,
// "secrets"): повтор подготовки снёс бы идущую операцию из-под ног.
// Заполненная переменная тоже принимается: перезапись ошибочно введённого
// значения — законное действие, отказывать в ней значило бы отправлять
// человека в редактор ради опечатки.
func (m jobModel) fillsSecret(focused columnID) bool {
	if focused != colDetail || m.detail != detailSecrets {
		return false
	}
	if _, ok := m.currentSecretRow(); !ok {
		return false
	}
	return m.idle()
}

// startFill — открывает поле ввода для строки под курсором; единственное
// место открытия поля.
func (m jobModel) startFill() (jobModel, tea.Cmd) {
	row, ok := m.currentSecretRow()
	if !ok {
		return m, nil
	}
	var cmd tea.Cmd
	m.secret, cmd = newSecretPrompt(row.Key)
	return m, cmd
}

// openLogCmd собирает команду открытия полноэкранного просмотрщика (план
// 15-01) из вида «лог шага» правой колонки: тот же источник, что и у
// встроенного предпросмотра — второго места сборки openLogMsg на экране
// джобы не появляется.
func (m jobModel) openLogCmd() tea.Cmd {
	// m.stepRows[m.cursor].Index — тот же источник истины, что уже читает
	// logGrowthSource и cursorAt (Фаза 15, план 15-03): значение равно
	// m.cursor+1 по построению (stepRowsFromSession), но теперь оба места
	// читают ОДНО поле, а не пересчитывают его каждое по-своему. Вызов
	// защищён условием m.cursor >= 0 у клавиши в updateKey — сюда эта
	// команда без выбранного шага не попадает.
	title := fmt.Sprintf("ЛОГ ШАГА %d", m.stepRows[m.cursor].Index)
	lines, tail := m.logGrowthSource()
	return func() tea.Msg {
		return openLogMsg{title: title, lines: lines, tail: tail, mark: -1, pull: ""}
	}
}

// detailHeading — заголовок правой колонки ровно как в мокапе (idea-0.3.1
// §6): текущий вид ВЕРХНИМ РЕГИСТРОМ стилем заголовка панели, остальные —
// строчными приглушёнными, через разделитель; имена видов берутся из
// единственной функции имён (detailName).
func (m jobModel) detailHeading(t Theme) string {
	views := []detailView{detailEnv, detailSecrets, detailLog}
	parts := make([]string, 0, len(views))
	for _, v := range views {
		name := detailName(v)
		if v == m.detail {
			parts = append(parts, t.PanelTitle.Render(strings.ToUpper(name)))
		} else {
			parts = append(parts, t.Muted.Render(name))
		}
	}
	return strings.Join(parts, t.Muted.Render(" · "))
}

// preparingView — тело экрана, пока сессия готовится (до sessionReadyMsg):
// кадр продолжает тикать спиннером, а не подвешивает интерфейс (тяга образа
// и прогон шагов идут в горутине команды).
func (m jobModel) preparingView() string {
	label := "готовлю джобу…"
	if m.pulling {
		// Та же формулировка, что и в строке подсказки, — и та же очистка
		// ссылки на образ: она приходит из конфига джобы (CR-06).
		label = HintPullingImage(m.pullImage)
	}
	return m.spin.View() + " " + m.theme.Text.Render(label)
}

// columnView — тело ОДНОЙ колонки экрана джобы без заголовка (Фаза 13, план
// 13-02): id называет, какая из двух колонок сейчас рисуется. Особые
// состояния, занимающие ЛЕНТУ целиком (подготовка, перенос правок), сюда
// не заходят вовсе — их рисует fullFrame ниже, и корневая модель спрашивает
// его ДО сборки ленты, а не веткой внутри тела колонки. Правая колонка
// показывает РОВНО ОДИН вид из трёх (Фаза 15, план 15-02, idea-0.3.1 §6):
// теперь в колонке один вид, а не окружение и лог сразу — высота колонки,
// освободившаяся от второй панели, и есть то, ради чего лог наконец стало
// можно листать.
func (m jobModel) columnView(id columnID, width int, focused bool) string {
	if id == colDetail {
		switch m.detail {
		case detailSecrets:
			return m.secretsPanel(width, focused)
		case detailLog:
			return m.logDetailPanel(width)
		default:
			return m.envPanel(width, focused)
		}
	}
	return m.stepsPanel(width, focused)
}

// cursorAt — часть общего контракта колонки (Фаза 13, план 13-03; Фаза 15,
// план 15-02): экран джобы держит две колонки, и id называет, чью позицию
// читать. Для колонки шагов — номер выбранного шага (индекс в m.stepRows) и
// его номер строкой (stepRow.Index стабилен в пределах джобы — шаги не
// переставляются и не удаляются между перезагрузками). Для правой колонки —
// номер выбранной строки и её ключ ПО ТЕКУЩЕМУ ВИДУ: envRow.Key в виде
// окружения, secretRow.Key в виде секретов (память курсора работает по
// имени переменной — ключ у обоих списков одной природы); в виде лога
// курсора нет, возвращаются нулевые значения. Пустой список любой из
// колонок тоже возвращает нулевые значения.
func (m jobModel) cursorAt(id columnID) (int, string) {
	if id == colDetail {
		switch m.detail {
		case detailSecrets:
			if m.envCursor < 0 || m.envCursor >= len(m.secretRows) {
				return 0, ""
			}
			return m.envCursor, m.secretRows[m.envCursor].Key
		case detailLog:
			return 0, ""
		default:
			if m.envCursor < 0 || m.envCursor >= len(m.envRows) {
				return 0, ""
			}
			return m.envCursor, m.envRows[m.envCursor].Key
		}
	}
	if m.cursor < 0 || m.cursor >= len(m.stepRows) {
		return 0, ""
	}
	return m.cursor, fmt.Sprintf("%d", m.stepRows[m.cursor].Index)
}

// withCursor — часть общего контракта колонки (Фаза 13, план 13-03; Фаза
// 15, план 15-02): тот же порядок «сначала ключ, затем прижатый номер», что
// и у остальных трёх моделей колонок. Правая колонка ищет строку ПО
// ТЕКУЩЕМУ ВИДУ — envRow.Key в виде окружения, secretRow.Key в виде
// секретов, в виде лога курсора нет и правка ничего не делает; не найдена —
// index прижимается к границам списка; список пуст — курсор обнуляется.
// Метод не возвращает команду: постановка позиции — чтение и запись поля, а
// не поход в сеть.
func (m jobModel) withCursor(id columnID, index int, key string) jobModel {
	switch id {
	case colDetail:
		if m.detail == detailLog {
			return m
		}
		rows := len(m.envRows)
		if m.detail == detailSecrets {
			rows = len(m.secretRows)
		}
		if rows == 0 {
			m.envCursor = 0
			return m
		}
		if key != "" {
			if m.detail == detailSecrets {
				for i, r := range m.secretRows {
					if r.Key == key {
						m.envCursor = i
						return m
					}
				}
			} else {
				for i, r := range m.envRows {
					if r.Key == key {
						m.envCursor = i
						return m
					}
				}
			}
		}
		idx := index
		if idx < 0 {
			idx = 0
		} else if idx >= rows {
			idx = rows - 1
		}
		m.envCursor = idx
		return m

	case colSteps:
		if len(m.stepRows) == 0 {
			m.cursor = 0
			return m
		}
		if key != "" {
			for i, r := range m.stepRows {
				if fmt.Sprintf("%d", r.Index) == key {
					m.cursor = i
					return m
				}
			}
		}
		idx := index
		if idx < 0 {
			idx = 0
		} else if idx >= len(m.stepRows) {
			idx = len(m.stepRows) - 1
		}
		m.cursor = idx
		return m
	}
	return m
}

// fullFrame — кадр, занимающий ЛЕНТУ целиком вместо колонок (Фаза 13, план
// 13-02, пункт 6): вид подготовки (спиннер и текст тяги образа) и экран
// переноса правок со списком изменённых файлов. Оба и раньше занимали тело
// целиком — с этого плана они занимают ленту, а не одну колонку: подтверждение
// записи в чужой репозиторий не должно ужиматься до трети экрана (FIXUI-03
// требует, чтобы человек видел ровно то, что будет записано, до любого
// согласия). Корневая модель спрашивает fullFrame перед сборкой ленты и,
// получив истину, рисует его вместо ribbonView().
func (m jobModel) fullFrame() (string, bool) {
	switch {
	case m.phase == phasePreparing:
		return m.preparingView(), true
	case m.apply.stage != applyIdle:
		return m.apply.filesPanel(m.theme), true
	}
	return "", false
}

// bannerLine — однострочный баннер отказа (Фаза 13, план 13-02, пункт 8):
// он больше не часть тела колонки — внутри узкой колонки он уезжал бы за
// край вместе с ней при прокрутке ленты, а баннер обязан оставаться видимым
// независимо от того, какая колонка сейчас в кадре. Корневая модель рисует
// его под ribbonView(), тем же местом вертикального ритма, где он рисовался
// раньше (App.viewInner).
func (m jobModel) bannerLine() (string, bool) {
	if m.banner == "" {
		return "", false
	}
	style := m.theme.Danger
	if m.blockedCanceled {
		// Отменённая человеком операция не помечается цветом отказа — это
		// осознанное действие, а не поломка.
		style = m.theme.Muted
	}
	return style.Render(m.banner), true
}

// keyBar собирает СВОЮ строку клавиш экрана джобы (Фаза 15, план 15-02,
// JOB-04): шелл, перезапуск шага, перенос правок и запись клавиши
// углубления с ЖИВОЙ подписью — в колонке шагов она называет правую
// колонку, в правой колонке — следующий вид; в виде «лог шага» к ним
// добавляется клавиша открытия полноэкранного просмотрщика. Клавиша во
// всех записях читается из раскладки (Hint/HintAs), а не пишется литералом.
// Ширина: "s шелл"(6) + "R повторить шаг"(15) + "A перенести правки"(18) +
// "→ секреты"(9, самая длинная живая подпись среди трёх видов — «секреты»
// длиннее «окружения» и «лог шага» не в счёт, потому что заменяется
// клавишей лога) + хвост закона (NavHints, без Right — уже назван выше) и
// пара помощь/выход (MetaHints); KeyBar сама отбрасывает записи закона по
// приоритету, если сумма не укладывается в 78 ячеек (keys.go, KeyBarOf) —
// собственные записи этой строки короче их суммы и укладываются всегда.
// Пока строка команды активна, на её месте — само поле ввода, а последняя
// ошибка разбора (если есть) показывается строкой НАД ним тем же приёмом,
// что и строка подсказки (RenderHint). Стадия applyMessage экрана переноса
// занимает то же место тем же приёмом — commandBar и applyState.msgInput
// никогда не активны одновременно (applyIdle-проверка в updateKey).
func (m jobModel) keyBar() string {
	if m.secret.active {
		// Поле ввода значения секрета на месте строки клавиш — тем же
		// приёмом, что и у строки команды (Фаза 15, план 15-02).
		return m.secret.view(m.theme)
	}
	if m.cmd.active {
		var b strings.Builder
		if m.cmd.err != "" {
			b.WriteString(RenderHint(m.theme, m.cmd.err))
			b.WriteString("\n")
		}
		b.WriteString(m.cmd.input.View())
		return b.String()
	}
	if m.apply.stage == applyMessage {
		return m.apply.messageView(m.theme)
	}

	extra := []KeyHint{Hint(m.keys.Shell), Hint(m.keys.Retry), Hint(m.keys.Apply)}
	if m.pane == colSteps {
		extra = append(extra, HintAs(m.keys.Right, columnTitle(colDetail)))
	} else {
		extra = append(extra, HintAs(m.keys.Right, detailName(nextDetail(m.detail))))
		if m.detail == detailLog {
			extra = append(extra, Hint(m.keys.Log))
		}
		if m.detail == detailSecrets {
			// Клавиша подтверждения впишет значение переменной под курсором
			// (Фаза 15, план 15-02) — читается из раскладки (Open), подпись
			// живая.
			extra = append(extra, HintAs(m.keys.Open, "вписать значение"))
		}
	}
	return KeyBar(m.theme, m.keys, extra...)
}

// hintText — (jobModel) hintText() string, единственная функция отображения
// фазы петли фикса в текст подсказки: она зовёт именованные формулировки
// (internal/ui/hint.go) и ничего не собирает на месте.
func (m jobModel) hintText() string {
	switch {
	case m.secret.active:
		// Поле открыто — это про то, что происходит прямо сейчас, поэтому
		// ветка стоит первой, впереди даже долгих операций и отказов (Фаза
		// 15, план 15-02).
		return HintSecretPrompt(m.secret.key)
	case m.canceling:
		return HintCanceling()
	case m.pulling:
		// Тяга образа идёт и при первой подготовке (phasePreparing), и при
		// подмене :image (задача 3) — оба случая эмитят одни и те же события
		// доменного слоя (event.ImagePulling/ImageLocal), поэтому проверка не
		// привязана к конкретной фазе.
		return HintPullingImage(m.pullImage)
	case m.swapping:
		// Хвост подмены образа после тяги (возврат владельца, снятие
		// старого контейнера, подъём нового, проба оболочки) — своей
		// именованной формулировки контракт не даёт (только итог,
		// HintImageReplaced), но подсказка обязана присутствовать всегда.
		return "подменяю образ, перезапускаю контейнер…"
	case m.execRunning:
		return "выполняю " + m.execCommand + "…"
	case m.phase == phaseRunning:
		return HintRunning(m.runLabel, m.runStep, len(m.stepRows))
	case m.phase == phaseBlocked && m.blockedCanceled:
		return "прервано пользователем — esc вернёт к списку джоб"
	case m.phase == phaseBlocked:
		return HintBlocked(m.banner)
	case m.execView:
		// Последняя произвольная команда закончилась — HintExecDone
		// перекрывает фазу-результат петли фикса, потому что она устарела
		// относительно только что выполненной команды.
		return HintExecDone(m.execCode)
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
	case m.apply.stage == applyConfirm:
		return HintApplyConfirm(len(m.apply.stats))
	case m.apply.stage == applyMessage:
		return HintCommitMessage()
	case m.apply.stage == applyCommitConfirm:
		return m.apply.commitConfirmPrompt(m.session.patchRoot)
	case m.phase == phaseApplied:
		return HintApplied(len(m.apply.stats))
	case m.phase == phaseCommitted:
		return HintCommitted()
	case m.phase == phaseImageReady && m.swappedImage != "":
		// Сразу после :image — итог замены, а не обычная готовность образа;
		// поле сбрасывается новой подготовкой сессии (её здесь и не бывает
		// без новой сессии), так что второй показ не залипает навсегда.
		return HintImageReplaced(m.swappedImage)
	case m.phase == phaseImageReady:
		return HintImageReady(m.session.img.Ref)
	}
	// Ни одно из состояний петли фикса выше не сработало — фолбэк называет
	// закон ленты по колонке в фокусе (Фаза 13, план 13-03), а не молчит:
	// «подсказка присутствует ВСЕГДА» держится тем, что здесь никогда не
	// возвращается пустая строка, а не тем, что этот фолбэк недостижим
	// сегодня. Порядок веток обязателен: долгие операции и отказы остаются
	// выше в switch — они про то, что происходит прямо сейчас, — состояния
	// петли фикса тоже выше, а правая колонка — самый нижний, самый общий
	// уровень: незаполненная переменная и есть причина, по которой петля не
	// сдвинется (Фаза 15, план 15-02).
	if m.pane == colDetail {
		if m.detail == detailSecrets {
			if row, ok := m.currentSecretRow(); ok {
				if row.Filled {
					return HintSecretFilled(row.Key, m.keys.Open.Help().Key)
				}
				return HintSecretMissing(row.Key, m.keys.Open.Help().Key)
			}
		}
		return HintDetailNext(m.keys.Right.Help().Key, detailName(nextDetail(m.detail)))
	}
	return HintColumnDeeper(columnTitle(colDetail))
}
