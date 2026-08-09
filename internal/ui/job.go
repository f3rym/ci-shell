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
	"github.com/f3rym/ci-shell/internal/token"
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

// logPanelLines — сколько последних строк ограниченного буфера лога сессии
// показывает панель лога.
const logPanelLines = 14

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

	width, height int
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
		// SwapImageCmd делегирует PrepareCmd, когда контейнера ещё не было
		// (Фаза 11, GUIDE-05) — сбрасывается и здесь, чтобы флаг не
		// залипал, если подготовка была запущена именно так.
		m.swapping = false
		m.stepRows = stepRowsFromSession(m.session)
		m.envRows = envRowsFromSession(m.session)
		if msg.outcome.Failed != nil {
			m.cursor = msg.outcome.FailedIndex - 1
		}
		// Готовая сессия с непустым списком недостающих переменных больше
		// не молчит (Фаза 11, GUIDE-03): человек узнаёт о поломке из
		// экрана сразу, а не из отказа шага через минуту.
		if missing := m.session.environment.Missing; len(missing) > 0 {
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

	case captureDoneMsg:
		m.apply = newApplyState(msg.patch, msg.stats, msg.ahead, msg.aheadKnown)
		m.phase = phaseApplyConfirm
		// Список файлов занимает тело целиком (bodyView) — устаревший вывод
		// последней команды :! не должен маячить в подсказке позади него.
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
		if path, err := token.Save(host, msg.Value); err != nil {
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
	if m.cmd.active {
		return m.updateCommandKey(msg)
	}
	if m.apply.stage != applyIdle {
		return m.updateApplyKey(msg)
	}

	switch {
	case key.Matches(msg, m.keys.Cancel):
		if m.phase == phasePreparing || m.phase == phaseRunning {
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

	case key.Matches(msg, m.keys.Up):
		if m.cursor > 0 {
			m.cursor--
		}
		// Выбор другого шага — панель лога возвращается к его хвосту, а не
		// остаётся на выводе последней команды :! (задача 3).
		m.execView = false
		return m, nil

	case key.Matches(msg, m.keys.Down):
		if m.cursor < len(m.stepRows)-1 {
			m.cursor++
		}
		m.execView = false
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
// честная строка ожидания. Отдельного состояния обрыва чтения у панели нет:
// буфер лога живёт только в памяти и не читает файлов, обрываться нечему —
// поле под него не заводится, пока не появится реальный источник обрыва.
// Пока идёт или только что закончилась произвольная команда :!
// (execView, задача 3), заголовок панели меняется на «ВЫВОД КОМАНДЫ» — тот
// же буфер лога, что и у шагов, только заголовок называет, что сейчас в нём.
func (m jobModel) logPanel() string {
	width := m.width - 2*OuterMargin
	if width < 1 {
		width = 1
	}
	titleText := fmt.Sprintf("ЛОГ ШАГА %d", m.cursor+1)
	if m.execView {
		titleText = "ВЫВОД КОМАНДЫ"
	}
	title := m.theme.PanelTitle.Render(titleText)

	if m.cursor < 0 {
		return title + "\n" + m.theme.Muted.Render("лог появится, когда вы выберете шаг")
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
		// Та же формулировка, что и в строке подсказки, — и та же очистка
		// ссылки на образ: она приходит из конфига джобы (CR-06).
		label = HintPullingImage(m.pullImage)
	}
	return m.spin.View() + " " + m.theme.Text.Render(label)
}

// bodyView собирает тело экрана джобы: две панели рядом (шаги, окружение),
// под ними панель лога на всю ширину минус отступ от краёв, и однострочный
// баннер над строкой подсказки при отказе. Пока экран переноса правок
// открыт (apply.stage != applyIdle) — список изменённых файлов занимает
// тело целиком: человек должен видеть ровно то, что будет записано, до
// любого согласия (FIXUI-03), а не гадать по шагам и окружению позади него.
func (m jobModel) bodyView() string {
	if m.phase == phasePreparing {
		return m.preparingView()
	}

	if m.apply.stage != applyIdle {
		return m.apply.filesPanel(m.theme)
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

// keyBar собирает строку клавиш экрана джобы из короткой формы помощи
// раскладки (Фаза 12, POL-03) — шелл, повторить шаг, перенести правки и
// командный режим перечислены полностью на экране помощи (`?`), а не
// здесь. Пока строка команды активна, на её месте — само поле ввода, а
// последняя ошибка разбора (если есть) показывается строкой НАД ним тем же
// приёмом, что и строка подсказки (RenderHint) — контракт требует, чтобы
// отказ не закрывал поле, и чтобы настоящая строка подсказки внизу экрана
// осталась на месте без исключений (она рисуется отдельно, в App.View).
// Стадия applyMessage экрана переноса занимает то же место тем же приёмом —
// commandBar и applyState.msgInput никогда не активны одновременно
// (applyIdle-проверка в updateKey).
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
	if m.apply.stage == applyMessage {
		return m.apply.messageView(m.theme)
	}
	return KeyBar(m.theme, m.keys)
}

// hintText — (jobModel) hintText() string, единственная функция отображения
// фазы петли фикса в текст подсказки: она зовёт именованные формулировки
// (internal/ui/hint.go) и ничего не собирает на месте.
func (m jobModel) hintText() string {
	switch {
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
	return ""
}
