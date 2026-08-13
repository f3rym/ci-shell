package ui

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"

	"github.com/f3rym/ci-shell/internal/browse"
	"github.com/f3rym/ci-shell/internal/config"
	"github.com/f3rym/ci-shell/internal/event"
	"github.com/f3rym/ci-shell/internal/provider"
	"github.com/f3rym/ci-shell/internal/provider/gitlab"
	"github.com/f3rym/ci-shell/internal/textwidth"
	"github.com/f3rym/ci-shell/internal/token"
)

// screen — экран интерфейса. screenJobs и screenJob объявлены с самого
// начала (план 09-01/09-02); screenTree наполняет Фаза 10 (BROW-01) —
// переключение экранов должно жить в одном месте, а не расползаться правкой
// перечисления. С Фазы 13 перечень выводится из ленты колонок (ribbon.go,
// screenFor) для колонок и из поля оверлея для заставки/проводника/помощи —
// сам перечень экранов при этом не меняется, его по-прежнему читает экран
// помощи.
type screen int

const (
	screenSplash screen = iota
	screenJobs
	screenJob
	screenTree
	// screenGuide — экран-проводник по типичным поломкам (Фаза 11,
	// GUIDE-01…05, internal/ui/guide.go).
	screenGuide
	// screenHelp — экран помощи (Фаза 12, POL-03, internal/ui/help.go):
	// `?` с любого экрана, кроме заставки и открытого поля ввода.
	screenHelp
	// screenMenu — меню входа (Фаза 14, MENU-01, internal/ui/menu.go).
	// Дописано в конец перечисления, а не по месту в цепочке экранов:
	// порядок в этом перечислении ничего не значит (экран выводится из
	// ленты и оверлея, см. screen() ниже), поэтому дописывание в конец
	// безопасно.
	screenMenu
	// screenLog — полноэкранный просмотрщик лога (Фаза 15, план 15-01,
	// LOG-01…LOG-04, internal/ui/logview.go): оверлей поверх ленты, тем же
	// приёмом, что и заставка, проводник, помощь и меню.
	screenLog
)

// overlay — экран, не являющийся колонкой ленты (Фаза 13, NAV-01…NAV-04):
// заставка стоит ПЕРЕД лентой — лента ещё пуста, вопрос «куда идти»
// задаётся раньше, чем лента начинает существовать; проводник и помощь
// перекрывают ленту целиком и закону стрелок не подчиняются — стрелка
// внутри них принадлежит им. Возвращаться после снятия оверлея некуда:
// лента при этом не менялась, и само снятие оверлея возвращает человека
// ровно туда, где он был.
type overlay int

const (
	overlayNone overlay = iota
	overlaySplash
	overlayGuide
	overlayHelp
	// overlayMenu — меню входа (Фаза 14, MENU-01…MENU-05): стоит ПЕРЕД
	// лентой ровно как заставка, но, в отличие от неё, является точкой
	// входа — заставка теперь открывается из меню, а не наоборот.
	overlayMenu
	// overlayLog — полноэкранный просмотрщик лога (Фаза 15, план 15-01):
	// перекрывает ленту целиком, забирает клавиши целиком (движение по
	// логу, поиск, переход к упавшему шагу) и закону стрелок не подчиняется
	// — тот же класс оверлея, что и проводник с помощью. Лента под ним не
	// меняется, и закрытие возвращает человека ровно туда, где он был.
	overlayLog
)

// App — корневая модель Bubble Tea; единственная модель проекта,
// реализующая интерфейс модели Bubble Tea. Подмодели экранов (splashModel,
// jobsModel, treeModel, ...) — обычные структуры с методами обновления и
// отрисовки, возвращающими строку, а не собственные реализации tea.Model.
type App struct {
	theme Theme
	// caps — возможности терминала, определённые ровно один раз при
	// создании модели (Run, ниже); тема пересобирается из них по приходу
	// сообщения о цвете фона (Фаза 12, POL-01).
	caps Caps

	width  int
	height int

	// ribbon — лента колонок (Фаза 13): то, что «глубже», живёт правее, и
	// это единственное, что нужно знать про навигацию. Экран больше не
	// хранится полем — он выводится методом screen() ниже.
	ribbon ribbon
	// overlay — экран, не являющийся колонкой (см. тип overlay выше).
	overlay overlay

	// menu — меню входа (Фаза 14, MENU-01…MENU-05): открывается оверлеем
	// overlayMenu и является стартовым состоянием корневой модели (см.
	// Run ниже) — точка входа теперь одна, и это меню.
	menu menuModel

	splash splashModel
	jobs   jobsModel
	job    jobModel
	tree   treeModel
	// treeReady — экран дерева собран newTreeModel (browseMsg), и его
	// компонент списка Bubbles можно трогать. Признак явный, а не выведенный
	// из непустоты access: доступ к хосту появляется в карте и при входе по
	// прямой ссылке на джобу (jobRefMsg), где newTreeModel не вызывается
	// вовсе, и вывод из одной лишь записи в карте означал бы SetSize на
	// нулевом list.Model при первом же изменении размера окна (CR-04 обзора
	// v0.3.0).
	treeReady bool

	// sessionGen — поколение сессии джобы: растёт на каждое открытие джобы.
	// Мост событий получает его при создании и проставляет каждому EventMsg,
	// а экран джобы отбрасывает чужие — старый мост остаётся подключённым к
	// той же программе, и события прошлой джобы иначе доезжали бы до новой
	// (WR-05 обзора v0.3.0). Тот же приём поколения, что уже применён в
	// панелях лога и пайплайнов.
	sessionGen int

	// guide — экран проводника по типичным поломкам (Фаза 11): открывается
	// оверлеем overlayGuide поверх ленты; возвращаться при закрытии некуда
	// специально хранить не нужно — лента не менялась (см. тип overlay).
	guide guideModel

	// help — экран помощи (Фаза 12, POL-03): тот же приём, что и у экрана
	// проводника выше — оверлей overlayHelp поверх ленты.
	help helpModel

	// logv — полноэкранный просмотрщик лога (Фаза 15, план 15-01): оверлей
	// overlayLog поверх ленты, тем же приёмом, что guide и help выше.
	// Собирается заново на каждое открытие (openLogMsg) и обнуляется на
	// закрытии (logDismissMsg) — вторая копия строк лога не переживает
	// закрытия просмотрщика (T-15-04).
	logv logView

	// access — доступ по хосту (Фаза 14, план 14-02): «текущий хост» как
	// понятие корневой модели больше не существует — в один момент времени
	// в ленте могут быть открыты проект одного инстанса и джоба другого, и
	// единственное поле хоста неизбежно назвало бы не тот. Карта создаётся
	// при сборке модели (Run ниже). browse.New по-прежнему вызывается ровно
	// в двух местах этого файла (browseMsg и jobRefMsg через resolveBrowse)
	// и нигде больше в пакете — единственное место, знающее, как собрать
	// клиент обхода поверх значения интерфейса провайдера.
	access map[string]hostAccess

	keys KeyMap

	// toy — «игрушка» (Фаза 16, план 16-04, TOY-01…04): украшение в правом
	// углу строки заголовка кадра. Что рисовать и когда тикать — знает
	// сама модель (internal/ui/toy.go); видна ли она СЕЙЧАС — решает ровно
	// одна функция корневой модели, toyVisible() ниже.
	toy toyModel
}

// quitMsg — просьба экрана завершить программу. Экраны НЕ зовут tea.Quit
// сами: выход обязан пройти через уборку сессии, а знает про неё только
// корневая модель (quitCmd ниже, CR-01 обзора v0.3.0).
type quitMsg struct{}

// quitCmd — единственная точка выхода из интерфейса: отменяет незавершённую
// подготовку, убирает сессию (контейнер, каталог файловых переменных с
// материализованными секретами, чекаут) и только потом завершает программу.
// Уборка идёт в горутине команды, потому что снятие контейнера ходит к
// демону Docker и подвесило бы отрисовку.
func (a App) quitCmd() tea.Cmd {
	sess := a.job.session
	if sess == nil {
		return tea.Quit
	}
	return func() tea.Msg {
		sess.Close()
		return tea.Quit()
	}
}

// programSend — функция отправки сообщения в запущенную программу Bubble
// Tea; ставится в Run() сразу после tea.NewProgram, потому что раньше самой
// программы не существует. Единственный потребитель — мост событий
// (Bridge.Attach), подключаемый в Update() при переходе на экран джобы: сама
// модель App не хранит ссылку на программу (Bubble Tea её и не передаёт), а
// без пакетной переменной мосту неоткуда было бы взять функцию отправки.
var programSend func(tea.Msg)

// liveSession — сессия, живая ПРЯМО СЕЙЧАС (Фаза 17, чинит CR-02 обзора
// v0.3.1): пакетная переменная тем же приёмом, что и programSend выше и
// renderPanic (internal/ui/recover.go) — уборка в Run() обязана знать
// актуальную сессию НЕЗАВИСИМО от того, что вернул p.Run(). Встроенный
// перехват паники Bubble Tea (включён по умолчанию, опция его
// выключения нигде не передаётся) возвращает (nil, err) при панике,
// пойманной ИМ, а не тремя точками guard этого пакета (например, в
// горутине долгой операции), — final.(App) тогда не срабатывает вовсе, и
// контейнер с материализованными секретами остаётся жить. liveSession —
// единственный источник истины про сессию в Run(), а final.(App) как
// отдельный путь уборки этим планом убирается, а не дублируется.
var liveSession atomic.Pointer[Session]

// Init — тело инициализации обёрнуто единственным перехватом проекта
// (guard, internal/ui/recover.go, Фаза 12, POL-04): паника здесь чаще всего
// означает половинчато собранную заставку, и следующие кадры врали бы
// человеку — при перехваченной панике возвращается команда завершения, а
// не попытка продолжить.
func (a App) Init() tea.Cmd {
	var cmd tea.Cmd
	if err := guard(func() { cmd = a.initInner() }); err != nil {
		return tea.Quit
	}
	return cmd
}

// initInner запускает программу единственной командой: запрос цвета фона
// терминала (цвет фона в Bubble Tea v2 приходит сообщением, а не
// запрашивается синхронно, поэтому первая отрисовка идёт темой по
// умолчанию и перерисовывается по приходу ответа). С Фазы 14 точка входа —
// меню (см. Run ниже), а не тот экран, что раньше запускался отсюда: тик
// его спиннера и мигание курсора поля ввода — бесконечная череда
// сообщений, каждое из которых перерисовывает кадр, а платить за них,
// пока этот экран не открыт, нечем. Команда старта переезжает туда, где
// этот экран открывается меню (см. обработку menuLinkMsg/menuBrowseMsg
// ниже) — модель при этом по-прежнему собирается заранее в Run, чтобы
// принять размер окна до первого открытия.
func (a App) initInner() tea.Cmd {
	return tea.RequestBackgroundColor
}

// screen — экран, который сейчас видит человек: экран колонки ленты в
// фокусе, а при активном оверлее — экран самого оверлея. Второго места,
// хранящего экран, в пакете больше нет.
func (a App) screen() screen {
	switch a.overlay {
	case overlayMenu:
		return screenMenu
	case overlaySplash:
		return screenSplash
	case overlayGuide:
		return screenGuide
	case overlayHelp:
		return screenHelp
	case overlayLog:
		return screenLog
	}
	return a.ribbon.screen()
}

// overlayFor — оверлей, который нужно восстановить после закрытия экрана
// проводника (Фаза 14, MENU-03): решает, кто ждал ответа, вместо того чтобы
// снимать оверлей безусловно. Место whereMenu — оверлей меню (человек
// добавлял ключ прямо из меню входа), whereSplash — оверлей заставки
// (человек отвечал на провалившуюся проверку). Фаза 13 сняла поле экрана
// возврата, потому что лента под проводником не меняется и возвращаться
// было некуда, — но проводник открывают и оверлеи, и для них это неверно:
// человек, добавивший ключ из меню, оказывался бы после ответа в пустой
// ленте, а человек, ответивший на поломку заставки, терял бы недопройденные
// проверки. Остальные места (подготовка воспроизведения, перенос правок) —
// под проводником лента, а не оверлей, и восстанавливать нечего.
func overlayFor(w guideWhere) overlay {
	switch w {
	case whereMenu:
		return overlayMenu
	case whereSplash:
		return overlaySplash
	}
	return overlayNone
}

// openDeeper — единственная точка углубления: если правее фокуса колонка
// уже есть, фокус просто переезжает в неё; иначе колонка в фокусе сама
// решает, что открыть, единым вызовом одноимённого метода. «→ открывает
// пайплайны» и «→ открывает панель действий» обязаны быть одним правилом, а
// не двумя.
//
// Запоминание позиции вызывается первой строкой, ДО обоих исходов: и до
// переезда фокуса на уже существующую колонку правее, и до просьбы колонке
// в фокусе открыть что-либо. Открытие (treeModel.openDeeper и подобные) двигает
// собственный курсор модели-владельца (раскрытие узла дерева, выбор
// пайплайна) — запиши позицию позже, и в ленте оказался бы уже сдвинутый
// курсор, а не тот, с которого человек ушёл вглубь (Фаза 13, план 13-03).
// confirm различает «жму» (⏎, клик мышью) и «листаю» (→). Разница ровно
// одна и только при уже открытой колонке справа: листающий переезжает в неё
// как есть, жмущий пересобирает её из того, на чём стоит курсор. Пока
// правее пусто, оба исхода совпадают — обещание «⏎ работает как →» для
// идущего вглубь впервые остаётся верным.
func (a App) openDeeper(confirm bool) (App, tea.Cmd) {
	a = a.rememberCursor()

	if !confirm && a.ribbon.hasDeeper() {
		a.ribbon = a.ribbon.deeper()
		return a.focusColumn(), nil
	}

	switch a.ribbon.focusedID() {
	case colRepos:
		var cmd tea.Cmd
		a.tree, cmd = a.tree.openDeeper()
		return a, cmd
	case colPipelines:
		var cmd tea.Cmd
		a.tree.runs, cmd = a.tree.runs.openDeeper()
		return a, cmd
	case colJobs:
		var cmd tea.Cmd
		a.jobs, cmd = a.jobs.openDeeper()
		return a, cmd
	case colSteps, colDetail:
		var cmd tea.Cmd
		a.job, cmd = a.job.openDeeper()
		return a, cmd
	}
	return a, nil
}

// focusColumn — единственная точка передачи фокуса подмоделям колонок:
// зовётся из всех трёх исходов движения по закону и из открытия новой
// колонки; второго места, раздающего фокус, не заводится. Экран дерева
// трогается только когда он уже собран (treeReady) — тот же приём
// осторожности, что и у SetSize (CR-04 обзора v0.3.0): до первого browseMsg
// его компоненты не готовы принимать вызовы.
//
// a.restoreCursor() зовётся здесь же, последней строкой, а не отдельным
// местом при движении назад (Фаза 13, план 13-03): порядок обязан быть
// «фокус колонке, затем курсор колонке» — восстанавливать позицию раньше,
// чем колонка узнала, что она в фокусе, было бы рано. У свежеоткрытой
// колонки (projectPickedMsg, openPipelineMsg, openJobMsg, openDetailMsg)
// ribbon.recall честно возвращает нулевые значения — восстановление здесь
// не вредит, оно просто ничего не переставляет, а второго места, ставящего
// курсор колонке, в проекте так и не появляется.
func (a App) focusColumn() App {
	id := a.ribbon.focusedID()
	if a.treeReady {
		a.tree = a.tree.setColumnFocus(id)
	}
	a.jobs = a.jobs.setColumnFocus(id)
	a.job = a.job.setColumnFocus(id)
	a = a.syncColumnSizes()
	return a.restoreCursor()
}

// rememberCursor — единственная точка запоминания позиции курсора (Фаза 13,
// план 13-03): спрашивает у колонки в фокусе её текущую позицию разбором по
// идентификатору колонки (по одной ветви на колонку — общего интерфейса у
// моделей-колонок нет, см. комментарий restoreCursor ниже) и записывает её в
// ленту. Зовётся ровно из одного места — первой строкой openDeeper(), ДО
// того, как фокус или колонка в фокусе успеют сдвинуть собственный курсор.
func (a App) rememberCursor() App {
	var cursor int
	var key string
	switch a.ribbon.focusedID() {
	case colRepos:
		if a.treeReady {
			cursor, key = a.tree.cursorAt()
		}
	case colPipelines:
		if a.treeReady {
			cursor, key = a.tree.runs.cursorAt()
		}
	case colJobs:
		cursor, key = a.jobs.cursorAt()
	case colSteps, colDetail:
		cursor, key = a.job.cursorAt(a.ribbon.focusedID())
	}
	a.ribbon = a.ribbon.remember(cursor, key)
	return a
}

// restoreCursor — единственная точка восстановления позиции курсора (Фаза
// 13, план 13-03): читает запомненную позицию колонки в фокусе из ленты
// (ribbon.recall) и ставит её той же колонке тем же разбором по
// идентификатору, каким rememberCursor читал позицию. Разбор по
// идентификатору, а не общий интерфейс колонки: все четыре модели-колонки
// держат методы с получателем-значением, возвращающие саму модель
// (treeModel, pipelinePanel, jobsModel, jobModel) — такой контракт
// («withCursor(...) T» на четырёх разных T) интерфейсом Go не выражается.
// Заводить ради него указательные получатели значило бы сломать стиль всех
// четырёх моделей ради одной функции — разбор в корневой модели остаётся
// осознанным выбором, а не обходом незнания интерфейсов Go.
func (a App) restoreCursor() App {
	cursor, key := a.ribbon.recall(a.ribbon.focusedID())
	switch a.ribbon.focusedID() {
	case colRepos:
		if a.treeReady {
			a.tree = a.tree.withCursor(cursor, key)
		}
	case colPipelines:
		if a.treeReady {
			a.tree.runs = a.tree.runs.withCursor(cursor, key)
		}
	case colJobs:
		a.jobs = a.jobs.withCursor(cursor, key)
	case colSteps, colDetail:
		a.job = a.job.withCursor(a.ribbon.focusedID(), cursor, key)
	}
	return a
}

// visibleLayout — единственное место, где содержимое видимых колонок
// превращается в их фактические ширины (Фаза 17, FIT-02): собирает
// желаемую ширину каждой видимой колонки одним диспетчером
// (naturalWidthFor) и отдаёт её ленте (ribbon.layout) для окончательного
// распределения. Три потребителя — отрисовка (ribbonView), клик мыши
// (hitTest) и пересинхронизация подмоделей (syncColumnSizes) — обязаны
// увидеть ОДНИ И ТЕ ЖЕ ширины в пределах одного кадра, второго места
// этого расчёта в пакете не появляется.
func (a App) visibleLayout() (first, count int, widths []int) {
	first, count = a.ribbon.window()
	if count == 0 {
		return first, count, nil
	}
	desired := make([]int, count)
	for i := 0; i < count; i++ {
		desired[i] = a.naturalWidthFor(a.ribbon.cols[first+i].id)
	}
	return first, count, a.ribbon.layout(desired)
}

// naturalWidthFor — диспетчер желаемой ширины колонки id по её реальному
// содержимому: разбор по идентификатору, тем же приёмом, что уже
// применяют rememberCursor/restoreCursor выше в файле — общего
// интерфейса у четырёх моделей-колонок нет (см. их комментарий).
func (a App) naturalWidthFor(id columnID) int {
	switch id {
	case colRepos, colPipelines:
		if a.treeReady {
			return a.tree.naturalWidth(id)
		}
		return ColumnMin
	case colJobs:
		return a.jobs.naturalWidth()
	case colSteps, colDetail:
		return a.job.naturalWidth(id)
	}
	return ColumnMin
}

// syncColumnSizes — единственная точка пересчёта размеров ВСЕХ подмоделей
// колонок ленты (Фаза 17, чинит CR-01 обзора v0.3.1): вызывается из
// focusColumn() — единственной точки, через которую проходят ВСЕ
// переходы фокуса и состава ленты (открытие новой колонки, переход по
// уже существующей, возврат назад, клик мышью), а не только открытие
// новой колонки и tea.WindowSizeMsg, как было раньше у columnWidthFor.
// Колонка вне текущего окна (свёрнутая либо ещё не открытая) размер не
// получает вовсе: она получит верный размер на следующий переход,
// который вернёт её в окно, — второй, спекулятивной формулы ширины для
// невидимых колонок здесь не заводится (WR-01 обзора v0.3.1 была ровно
// такой формулой, разошедшейся с ribbon.layout).
func (a App) syncColumnSizes() App {
	first, count, widths := a.visibleLayout()
	reposWidth, pipelinesWidth, jobsWidth := 0, 0, 0
	for i := 0; i < count; i++ {
		// columnBodyWidth (Фаза 17, живой обзор после плана 17-02): подмодели
		// получают ширину СОДЕРЖИМОГО рамки, а не полную ширину блока
		// колонки widths[i] — ту же поправку зовёт columnView (тело
		// колонки) для тех же widths[i] этого же кадра; setSize и
		// отрисовка обязаны договориться об одном и том же числе.
		bodyWidth := columnBodyWidth(widths[i])
		switch a.ribbon.cols[first+i].id {
		case colRepos:
			reposWidth = bodyWidth
		case colPipelines:
			pipelinesWidth = bodyWidth
		case colJobs:
			jobsWidth = bodyWidth
		}
	}
	if a.treeReady {
		a.tree = a.tree.setSize(reposWidth, pipelinesWidth, a.height)
	}
	a.jobs = a.jobs.setSize(jobsWidth, a.height)
	a.job = a.job.setSize(a.height)
	return a
}

// ribbonView — единственное место сборки кадра из ВИДИМЫХ колонок ленты
// (Фаза 13, план 13-02, NAV-03): перебирается ОКНО видимых колонок
// (a.ribbon.window()), а не весь перечень колонок ленты (a.ribbon.cols) —
// именно это и означает «остальные уезжают за край» (idea-0.3.1 §3).
// Инвариант «фокус всегда в кадре» держит сама лента (ribbon.reflow,
// internal/ui/ribbon.go) — здесь он не проверяется повторно, только
// читается через window()/layout().
func (a App) ribbonView() string {
	first, count, widths := a.visibleLayout()
	if count == 0 {
		return ""
	}
	gap := strings.Repeat(" ", PanelGap)
	collapsed := a.ribbon.collapsed()
	parts := make([]string, 0, (len(collapsed)+count)*2-1)
	// Свёрнутые колонки (Фаза 16, LOOK-03) рисуются ПЕРЕД видимым окном
	// полных колонок, слева направо, тем же зазором PanelGap, что уже
	// разделяет полные колонки между собой — второго зазора не изобретается.
	// Они остаются на экране узкой полосой вместо того, чтобы полностью
	// исчезнуть за краем, как до этого плана (ribbon.window() отдавал только
	// видимый срез).
	for i := range collapsed {
		if i > 0 {
			parts = append(parts, gap)
		}
		parts = append(parts, a.collapsedColumnView(CollapsedColumnWidth, a.ribbon.columnBoxHeight()))
	}
	if len(collapsed) > 0 {
		parts = append(parts, gap)
	}
	for i := 0; i < count; i++ {
		if i > 0 {
			parts = append(parts, gap)
		}
		col := a.ribbon.cols[first+i]
		focused := first+i == a.ribbon.focus
		parts = append(parts, a.columnView(col, widths[i], focused))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// columnHeading — единственное место, где колонка получает заголовок в
// кадре (Фаза 15, план 15-02): для правой колонки экрана джобы заголовок
// даёт сама модель экрана (jobModel.detailHeading) — он переключатель,
// зависящий от состояния m.job.detail (Фаза 15, idea-0.3.1 §6: текущий вид
// ВЕРХНИМ РЕГИСТРОМ, остальные строчными приглушёнными); для всех остальных
// колонок — прежняя формула (columnTitle плюс уточнение col.label),
// обрезанная по ширине графемной мерой и стилем заголовка панели.
// Исключение ровно одно и живёт здесь, а не в columnTitle: заголовок правой
// колонки — переключатель, зависящий от состояния модели, а columnTitle
// обязана остаться чистой функцией от идентификатора колонки.
func (a App) columnHeading(col column, width int) string {
	if col.id == colDetail {
		return a.job.detailHeading(a.theme)
	}
	title := columnTitle(col.id)
	if col.label != "" {
		title = title + " " + col.label
	}
	return a.theme.PanelTitle.Render(Truncate(title, width))
}

// columnView — одна колонка ленты: заголовок (columnHeading выше) и тело,
// взятое у модели-владельца по идентификатору колонки, обёрнутое рамкой Lip
// Gloss. Признак фокуса передаётся моделям — колонка вне фокуса не рисует
// курсор инверсией, иначе на экране было бы два курсора сразу и ни один не
// был бы настоящим. Вся колонка укладывается в ширину width стилем Lip
// Gloss, чтобы соседняя колонка начиналась ровно там, где должна, чем бы ни
// было содержимое.
//
// Репозитории и пайплайны рисует модель дерева, джобы — модель списка джоб,
// шаги и окружение·секреты·лог — модель экрана джобы: каждая колонка своим
// методом columnView, принимающим ширину и признак фокуса (план 13-02,
// задача 2) — второй отрисовки той же самой модели в пакете не заводится.
//
// Фаза 16 (план 16-01) добавляет здесь и только здесь рамку тела колонки:
// LOOK-01 — рамка отделяет колонки друг от друга, и колонка в фокусе
// отличается ЦВЕТОМ рамки (Theme.Accent) от остальных (Theme.Border), а не
// только заголовком (заголовок фазы 13 остаётся вторым, независимым
// признаком); LOOK-02 — высота рамки берётся из a.ribbon.columnBoxHeight(),
// одной на все видимые колонки кадра, а Style.Height() Lip Gloss ДОПОЛНЯЕТ
// короткое содержимое пустыми строками до пола кадра, но не укорачивает
// длинное — известное ограничение, названное в <constraints> плана 16-01
// (T-16-03), второго места отрисовки рамки колонки в пакете не появляется.
func (a App) columnView(col column, width int, focused bool) string {
	header := a.columnHeading(col, width)

	// bodyWidth — ширина, которую реально увидит содержимое ВНУТРИ рамки
	// (Фаза 17, живой обзор после плана 17-02, FIT-01/FIT-03/FIT-05): width
	// здесь — полная ширина блока колонки С РАМКОЙ (та же величина, что уже
	// идёт в a.boxed(width, ...) и во внешний Style.Width(width) ниже) — её
	// сумму лента (ribbon.layout) держит равной доступной ширине терминала.
	// Тело колонки рисуется ВНУТРИ рамки, которая по 1 ячейке с каждой
	// стороны меньше заявленной Width (columnBodyWidth, реализация и
	// пояснение — рядом с boxed ниже) — до этой правки сюда шла полная
	// width без вычета рамки, и модели-колонки (список джоб/пайплайнов,
	// панели джобы) готовили содержимое на 2 ячейки шире, чем реально
	// помещается в рамку.
	bodyWidth := columnBodyWidth(width)

	var body string
	switch col.id {
	case colRepos, colPipelines:
		if a.treeReady {
			body = a.tree.columnView(col.id, bodyWidth, focused)
		}
	case colJobs:
		body = a.jobs.columnView(bodyWidth, focused)
	case colSteps, colDetail:
		body = a.job.columnView(col.id, bodyWidth, focused)
	}

	// Высота содержимого рамки: columnBoxHeight() — высота заголовок+рамка
	// целиком, минус 1 строка заголовка колонки (она остаётся НАД рамкой, а
	// не внутри неё) и минус 2 строки самой рамки (верх+низ), прижатая к
	// минимуму 1.
	boxHeight := a.ribbon.columnBoxHeight() - 1 - 2
	if boxHeight < 1 {
		boxHeight = 1
	}

	return lipgloss.NewStyle().Width(width).Render(header + "\n" + a.boxed(width, boxHeight, focused, body))
}

// columnBodyWidth — ширина СОДЕРЖИМОГО внутри рамки колонки шириной width
// (Фаза 17, живой обзор после плана 17-02): рамка Lip Gloss забирает по 1
// ячейке слева и справа ИЗ заявленной width (см. boxed ниже, тот же расчёт,
// проверенный отдельным прогоном charm.land/lipgloss/v2), поэтому
// содержимому остаётся width-2, прижатое к минимуму 1. Единственная точка
// этого вычитания на файл — columnView (тело колонки) и syncColumnSizes
// (предварительный setSize подмоделей) обязаны видеть ОДНО И ТО ЖЕ число,
// иначе setSize подготовит содержимое одной ширины, а рамка примет другую.
func columnBodyWidth(width int) int {
	w := width - 2
	if w < 1 {
		w = 1
	}
	return w
}

// boxed — общий приём рамки колонки (Фаза 16, план 16-01), вынесенный из
// columnView выше: прижимает ширину/высоту к минимуму, выбирает цвет рамки
// по focused и оборачивает content ровно одним
// lipgloss.NewStyle().Width().Height().Border(lipgloss.RoundedBorder()).
// BorderForeground(...).Render(content). Свёрнутая колонка (LOOK-03,
// collapsedColumnView ниже) рисуется ТЕМ ЖЕ приёмом — второй формулы рамки
// колонки в файле не появляется, LOOK-01 и LOOK-03 обязаны выглядеть одним и
// тем же языком, а не двумя похожими.
func (a App) boxed(width, height int, focused bool, content string) string {
	// Ширина блока рамки: вопреки прежнему комментарию здесь (и вопреки
	// прежнему коду, вычитавшему рамку ЕЩЁ РАЗ), Lip Gloss v2 считает рамку
	// ЧАСТЬЮ заявленной Width — Style{}.Width(N).Border(...).Render(...)
	// отдаёт блок шириной РОВНО N (граница вписана в N, а не добавлена
	// сверх неё), проверено отдельным прогоном на charm.land/lipgloss/v2 —
	// поэтому box-width передаётся В width КАК ЕСТЬ, без вычитания.
	// Прежний код вычитал 2 ячейки здесь ВТОРОЙ раз (columnBodyWidth ниже
	// уже вычитает их один раз для содержимого) — блок рамки оказывался на
	// 2 ячейки уже, чем отвела лента, а модели-колонки готовили содержимое
	// НЕ под эту урезанную ширину, а под полную width (до правки этого же
	// коммита) — двойная ошибка суммарно давала минус 4 ячейки настоящей
	// внутренней ширины рамки, из-за чего таблица пайплайнов переносила
	// «когда» на вторую строку (FIT-05), а строки заворачивались, раздувая
	// высоту колонки за нижний край кадра (FIT-01, FIT-03).
	boxWidth := width
	if boxWidth < 1 {
		boxWidth = 1
	}
	if height < 1 {
		height = 1
	}

	borderColor := a.theme.Border.GetForeground()
	if focused {
		borderColor = a.theme.Accent.GetForeground()
	}
	return lipgloss.NewStyle().
		Width(boxWidth).
		Height(height).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Render(content)
}

// collapsedColumnView — тело свёрнутой колонки (Фаза 16, LOOK-03): пустая
// строка содержимого, обёрнутая тем же boxed(width, height, focused=false,
// "") — свёрнутая колонка НИКОГДА не в фокусе по построению (фокус всегда на
// одной из полных колонок окна, ribbon.window()), поэтому focused здесь
// буквальный false, а не переменная. Высота — та же
// a.ribbon.columnBoxHeight() - 1 - 2 (минус строка заголовка НАД рамкой,
// минус верх/низ рамки), что и у полных колонок: свёрнутая колонка обязана
// заканчиваться на той же нижней границе, что и соседние (LOOK-02 не
// отменяется свёрнутой колонкой). Заголовка НАД полосой нет — заголовок
// несёт СМЫСЛ («РЕПОЗИТОРИИ», номер пайплайна), а полоса шириной в 3 ячейки
// не может показать ни одной графемы этого смысла без обрезки до
// бессмысленного символа; пустая строка вместо заголовка честнее подписи из
// одной обрубленной буквы.
func (a App) collapsedColumnView(width, height int) string {
	// height приходит от вызывающего (ribbonView, ниже) равным
	// a.ribbon.columnBoxHeight() — той же величине, что columnView() выше
	// уже вычитает на 1 (заголовок) и 2 (верх+низ рамки) для полных колонок;
	// свёрнутая колонка обязана дотянуться до того же пола кадра.
	boxHeight := height - 1 - 2
	if boxHeight < 1 {
		boxHeight = 1
	}
	return a.boxed(width, boxHeight, false, "")
}

// contentTop — номер строки (считая с нуля от верха кадра), с которой
// начинается первое содержимое внутри рамки первой видимой колонки (Фаза 16,
// LOOK-05): 2 (заголовок кадра `ci-shell` + пустая строка перед телом — та
// же арифметика, что уже вывела RibbonChromeHeight, план 16-01) плюс 1
// (заголовок САМОЙ колонки, строка над рамкой) плюс 1 (верхняя граница
// рамки) — четыре строки, ПОСЧИТАННЫЕ, а не угаданные: те же четыре строки,
// что уже используют columnHeading+boxed (columnView выше) для любой
// колонки.
func (a App) contentTop() int {
	return 4
}

// clickRow — единственная точка обработки клика мыши по строке (Фаза 16,
// LOOK-05): переиспользует ровно тот же приём, что и ручная навигация
// (rememberCursor/focusOn/focusColumn/openDeeper) — второй реализации
// «эквивалент ⏎» в проекте не появляется, клик готовит ленту и курсор к
// нужному состоянию и передаёт исполнение туда же, куда его передаёт
// клавиатура.
func (a App) clickRow(id columnID, row int) (App, tea.Cmd) {
	if !a.ribbon.has(id) {
		// Клик по колонке, которой уже нет в ленте (например, гонка с
		// перерисовкой) — ничего не делает.
		return a, nil
	}

	// Позиция ПРЕЖНЕЙ колонки в фокусе запоминается ДО того, как фокус
	// переедет — тем же порядком, что и openDeeper() (см. её комментарий
	// выше).
	a = a.rememberCursor()
	// Фокус переезжает на кликнутую колонку (может быть та же, что уже в
	// фокусе — тогда focusOn ничего не меняет, что и требуется), затем
	// раздаётся подмоделям тем же методом, что и ручная навигация.
	a.ribbon = a.ribbon.focusOn(id)
	a = a.focusColumn()

	if row < 0 {
		// Клик по свёрнутой колонке (задачи 1 и 2 — там строки не
		// существует), либо по рамке/заголовку полной колонки: фокус
		// переставлен, курсор не трогается, углубления не происходит. Клик
		// по свёрнутой колонке этим и ограничивается — «разверни меня», не
		// «открой что-то внутри».
		return a, nil
	}

	// Курсор кликнутой колонки ставится на row тем же разбором по
	// идентификатору, каким App.restoreCursor() уже ставит запомненную
	// позицию — та же форма, только число берётся из клика, а не из памяти
	// ленты.
	switch id {
	case colRepos:
		if a.treeReady {
			a.tree = a.tree.withCursor(row, "")
		}
	case colPipelines:
		if a.treeReady {
			a.tree.runs = a.tree.runs.withCursor(row, "")
		}
	case colJobs:
		a.jobs = a.jobs.withCursor(row, "")
	case colSteps, colDetail:
		a.job = a.job.withCursor(id, row, "")
	}
	// Клик мышью — эквивалент ⏎ (LOOK-05), а не →: человек ткнул в строку,
	// то есть выбрал именно её, и колонка справа обязана пересобраться под
	// этот выбор, а не принять фокус от прошлого.
	return a.openDeeper(true)
}

// dropped разбирает колонки, отброшенные лентой при открытии другого пути
// (ribbon.open) — единая уборка на все обработчики. Колонка, выпавшая из
// ленты, недостижима, а её контейнер, чекаут и каталог файловых переменных
// с материализованными секретами остались бы на машине до конца процесса,
// если их не убрать здесь (T-13-02).
func (a App) dropped(ids []columnID) (App, tea.Cmd) {
	for _, id := range ids {
		if id == colSteps && a.job.session != nil {
			sess := a.job.session
			a.job.session = nil
			liveSession.CompareAndSwap(sess, nil)
			return a, func() tea.Msg { sess.Close(); return nil }
		}
	}
	return a, nil
}

// inputActive сообщает, захватывает ли текущий экран сырой ввод текста:
// строка команды экрана джоб или джобы, поле сообщения коммита, фильтр
// дерева. Потребителей два, и оба здесь же: сопоставление клавиши помощи в
// Update ниже (Фаза 12, POL-03) — пока открыто одно из этих полей,
// вопросительный знак обычный символ ввода, а не запрос помощи, иначе
// набрать «?» в сообщении коммита было бы нельзя, — и cancelsRun ниже, а
// также ветвь закона ленты (Фаза 13): пока поле ввода открыто, стрелки
// двигают курсор в тексте, а не фокус по ленте.
// Ctrl-C в этот перечень не заглядывает никогда: он выходит и из открытого
// поля ввода тоже (см. Update). Экран проводника сюда не входит: для
// него вопрос уже решён выше (ранний break при overlayGuide) — сопоставление
// клавиши помощи до него не доходит вовсе, каким бы ни был его собственный
// ввод.
func (a App) inputActive() bool {
	switch a.screen() {
	case screenJobs:
		return a.jobs.cmd.active
	case screenJob:
		// a.job.secret.active — поле ввода значения секрета (Фаза 15, план
		// 15-02): пока оно открыто, ввод захвачен — иначе буква выхода,
		// набранная в значении секрета, завершила бы программу (тот же
		// класс дефекта, что план 15-01 уже починил для клавиши выхода).
		return a.job.cmd.active || a.job.apply.stage == applyMessage || a.job.secret.active
	case screenTree:
		return a.tree.list.FilterState() == list.Filtering
	case screenLog:
		// Пока открыто поле ввода запроса поиска просмотрщика лога, ввод
		// захвачен им же — буква выхода или помощи, набранная в запрос, не
		// должна завершать программу или открывать помощь поверх (Фаза 15,
		// план 15-01, T-15-07).
		return a.logv.searching
	}
	return false
}

// cancelsRun — перехватывает ли текущий экран Ctrl-C под отмену идущей
// долгой операции ВМЕСТО выхода. Такой экран в проекте ровно один — экран
// джобы во время подготовки или прогона (internal/ui/job.go, updateKey), и
// только пока у него не открыто поле ввода: с открытой строкой команды
// нажатие до отмены всё равно не дошло бы (updateKey отдаёт клавиши полю), и
// Ctrl-C не делал бы вообще ничего. Во всех остальных случаях Ctrl-C
// означает выход.
func (a App) cancelsRun() bool {
	return a.screen() == screenJob && !a.inputActive() && a.job.cancelable()
}

// toyVisible — ЕДИНСТВЕННАЯ точка решения «видна ли игрушка прямо сейчас»
// (Фаза 16, план 16-04). Первая проверка — a.toy.off, настройка "off"
// (TOY-01, не список мест TOY-02: выключенная игрушка не показывается нигде
// по определению). Дальше — ровно шесть условий TOY-02/idea-0.3.1 §1.1 и
// ничего сверх них — второго места, решающего этот вопрос, в пакете нет и
// не появится: список мест, где игрушки нет, обязан быть узнаваемым
// правилом, а не разбросанными условиями (idea-0.3.1 §8, «игрушка против
// читаемости»).
//
//  1. !a.caps.Colored() — отключён цвет (internal/ui/color.go): игрушка не
//     несёт состояния, которое обязано пережить монохром, — только
//     украшает, и в монохроме не нужна.
//  2. a.width > 0 && a.width < 100 — узкий терминал (порог дословно из
//     idea-0.3.1 §1.1); проверка > 0 не даёт скрыть игрушку до первого
//     tea.WindowSizeMsg, когда a.width ещё нулевой.
//  3. a.height > 0 && a.height < 30 — низкий терминал, тем же порогом и той
//     же защитой от преждевременного нуля.
//  4. a.overlay == overlayLog — полноэкранный просмотрщик лога.
//  5. a.overlay == overlayHelp — полноэкранный экран помощи.
//  6. a.overlay == overlayGuide && a.guide.Guide.Kind == guideInput —
//     экран-проводник С ПОЛЕМ ВВОДА (не любой проводник вообще).
//  7. (a.ribbon.has(colSteps) || a.ribbon.has(colDetail)) && !a.job.idle() —
//     идёт долгая операция экрана джобы (тяга образа, прогон шагов).
func (a App) toyVisible() bool {
	if a.toy.off {
		return false
	}
	if !a.caps.Colored() {
		return false
	}
	if a.width > 0 && a.width < 100 {
		return false
	}
	if a.height > 0 && a.height < 30 {
		return false
	}
	if a.overlay == overlayLog || a.overlay == overlayHelp {
		return false
	}
	if a.overlay == overlayGuide && a.guide.Guide.Kind == guideInput {
		return false
	}
	if (a.ribbon.has(colSteps) || a.ribbon.has(colDetail)) && !a.job.idle() {
		return false
	}
	return true
}

// resolveBrowse резолвит токен хоста и собирает и значение интерфейса
// провайдера, и клиент обхода поверх него — browse.New вызывается ровно
// здесь и в обработке browseMsg ниже, больше нигде в пакете.
func resolveBrowse(host string) (provider.Provider, *browse.Client, error) {
	tok, err := token.Resolve(host)
	if err != nil {
		return nil, nil, err
	}
	p := gitlab.New(host, tok)
	return p, browse.New(p, host), nil
}

// openHostCmd — единственная точка открытия хоста (Фаза 14, план 14-02):
// резолвит доступ поверх единственного сборщика клиента обхода
// (resolveBrowse выше), спрашивает у API, кто мы (CurrentUser — нужно для
// строки личного пространства в дереве), и возвращает либо готовый доступ,
// либо отказ узла с ключом строки-хоста той же формулой, что и у дерева
// (hostKey, internal/ui/tree.go) — второй формулы ключа здесь не появляется.
// Запрос «кто мы» идёт именно здесь, а не в дереве: дерево клиента не
// собирает, а без клиента спросить некого.
func (a App) openHostCmd(host string) tea.Cmd {
	return func() tea.Msg {
		p, b, err := resolveBrowse(host)
		if err != nil {
			return treeFailedMsg{key: hostKey(host), reason: provider.SafeText(err.Error())}
		}
		user, err := p.CurrentUser(context.Background())
		if err != nil {
			return treeFailedMsg{key: hostKey(host), reason: provider.SafeText(err.Error())}
		}
		return hostReadyMsg{access: hostAccess{Host: host, Provider: p, Client: b, User: user}}
	}
}

// Update — тело обёрнуто единственным перехватом проекта (guard,
// internal/ui/recover.go, Фаза 12, POL-04). При перехваченной панике
// возвращается ПРЕЖНЯЯ модель (та, что была до вызова, — половинчатое
// обновление в model из updateInner не используется) и команда завершения:
// паника здесь означает, что состояние модели могло остаться половинчатым,
// и следующие кадры врали бы человеку.
func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var model tea.Model
	var cmd tea.Cmd
	if err := guard(func() { model, cmd = a.updateInner(msg) }); err != nil {
		return a, tea.Quit
	}
	return model, cmd
}

// updateInner — тонкая обёртка над updateInnerScreens (Фаза 16, план 16-04,
// TOY-03): продвигает кадр игрушки по её собственному тику и решает заново
// на КАЖДОЕ сообщение программы, тикать ли игрушке дальше, — прежде чем
// отдать сообщение остальной обработке.
//
// toyTickMsg разбирается ЗДЕСЬ, а не внутри updateInnerScreens: тот отдаёт
// сообщение подмодели текущего экрана диспетчером в самом низу функции —
// toyTickMsg там не опознала бы ни одна подмодель, и сообщение просто
// исчезло бы, ничего не сломав, но и не продвинув кадр.
//
// a.toy.ensure(a.toyVisible()) зовётся на КАЖДОЕ сообщение — это дёшево
// (несколько сравнений, без обращения к сети или диску) и гарантирует, что
// цепочка тика возобновится на СЛЕДУЮЩЕМ же сообщении программы после того,
// как видимость игрушки перестала быть ложной (открылось окно шире 100
// колонок, закрылся просмотрщик лога, кончилась долгая операция) — без
// явного перечисления всех переходов, которые могли бы это вызвать. Пока
// игрушка скрыта и никаких других сообщений не приходит (пустой терминал,
// ничего не нажато), новый тик НЕ планируется — TOY-03 держится ровно на
// этом: toyModel.ensure не возвращает команду, когда видимость ложна.
func (a App) updateInner(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(toyTickMsg); ok {
		a.toy = a.toy.advance()
	}
	var toyCmd tea.Cmd
	a.toy, toyCmd = a.toy.ensure(a.toyVisible())
	model, cmd := a.updateInnerScreens(msg)
	return model, tea.Batch(toyCmd, cmd)
}

// updateInnerScreens обрабатывает сообщения программы. Общие для всех
// экранов случаи (размер окна, цвет фона, выход, закон ленты) разбираются
// здесь один раз; остальное уходит подмодели текущего экрана. Тело функции
// — прежний updateInner (Фаза 16, план 16-04 переименовал функцию, не
// тронув ни одной строки внутри веток): игрушка в этот разбор не
// вмешивается, её тик и решение о видимости обёрнуты снаружи, в updateInner
// выше.
func (a App) updateInnerScreens(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		a.splash.width, a.splash.height = msg.Width, msg.Height
		// Меню занимает кадр целиком, как заставка (Фаза 14) — своей доли
		// ленты у него нет, поэтому размер приходит за вычетом отступов от
		// обоих краёв, тем же расчётом, каким считается доступная ширина
		// колонки ниже.
		a.menu = a.menu.setSize(msg.Width-2*OuterMargin, msg.Height)
		// Окно видимых колонок ленты и их ширины пересчитываются на том же
		// сообщении, что и всё остальное (Фаза 13) — ДО передачи размера
		// моделям колонок ниже: они читают ровно этот расчёт
		// (App.visibleLayout, Фаза 17), а не делят ширину кадра сами.
		a.ribbon = a.ribbon.setSize(msg.Width, msg.Height)
		// Единая пересинхронизация размеров ВСЕХ подмоделей колонок (Фаза
		// 17, focusColumn/syncColumnSizes) заменяет три отдельных вызова
		// setSize, что были здесь до этой фазы — второго места, задающего
		// размер подмоделям на изменение размера окна, не остаётся. treeReady
		// охраняет дерево тем же способом, что и раньше (CR-04 обзора
		// v0.3.0): syncColumnSizes сама проверяет это поле.
		a = a.focusColumn()
		if a.overlay == overlayHelp {
			// Тот же приём, что и у дерева выше: область просмотра экрана
			// помощи (viewport.Model) собрана newHelpModel только при
			// первом открытии `?`, и SetSize до этого небезопасна.
			a.help = a.help.setSize(msg.Width, msg.Height)
		}
		// Просмотрщик лога (Фаза 15, план 15-01, задача 3, пункт 15): высота
		// — тело кадра, тем же расчётом, что и у openLogMsg выше (title,
		// пустые строки, строка клавиш и строка подсказки, плюс пустая
		// строка и строка положения внутри тела — восемь строк накладных
		// расходов), ширина — за вычетом отступов от краёв. logView —
		// простое значение без компонентов Bubbles, вызов безопасен даже
		// когда просмотрщик сейчас не открыт.
		logHeight := msg.Height - 8
		if logHeight < 0 {
			logHeight = 0
		}
		a.logv = a.logv.setSize(msg.Width-2*OuterMargin, logHeight)
		return a, nil
	case tea.BackgroundColorMsg:
		// Фон уточняется этим сообщением, а не запрашивается синхронно
		// (Init ниже) — тема пересобирается по тем же возможностям с
		// обновлённым признаком фона; профиль цвета сообщение не трогает.
		a.caps.Dark = msg.IsDark()
		a.theme = NewTheme(a.caps)
		return a, nil
	case tea.KeyPressMsg:
		// Ctrl-C завершает программу с ЛЮБОГО экрана — включая заставку,
		// экран проводника и любое открытое поле ввода. Это универсальное
		// соглашение терминала, и перехватывать его под ввод текста нельзя:
		// в сыром режиме сигнала не возникает вовсе (Bubble Tea v2 сама
		// ctrl+c не обрабатывает — обработчика в библиотеке нет), поэтому
		// единственный, кто может здесь выйти, — это мы. Сопоставление
		// стоит ПЕРЕД ранним break экрана проводника и перед всеми
		// исключениями заставки, иначе оба снова окажутся без выхода.
		//
		// Единственное исключение — идущая долгая операция экрана джобы:
		// там первое нажатие отменяет её (контракт, «Долгие операции»), а
		// когда отменять нечего, Ctrl-C снова означает выход.
		//
		// Выход идёт через единственную точку выхода quitCmd, а не напрямую
		// командой библиотеки: иначе сессия не убирается, и на машине
		// остаются живой контейнер, чекаут и каталог файловых переменных с
		// материализованными секретами (CR-01).
		if key.Matches(msg, a.keys.Cancel) && !a.cancelsRun() {
			return a, a.quitCmd()
		}

		// Пока открыт экран проводника, нажатия клавиш уходят подмодели
		// целиком, включая привязки выхода и возврата (T-11-10): буква
		// выхода, набранная в поле ввода токена, не должна завершать
		// программу, а esc обязан вернуться на нужный экран через
		// guideDismissMsg проводника, а не через закон ленты.
		if a.overlay == overlayGuide {
			break
		}

		// q перестал быть выходом (Фаза 18, KEYS-02) — он синоним ← и на
		// колонках ленты разбирается законом ниже (navFor, ribbon.go), а на
		// заставке, в меню и в просмотрщике лога — их собственным разбором
		// клавиш (splash.go/menu.go/logview.go). Выход остался на Cancel
		// (проверка выше по коду) и на команде :q (case quitMsg ниже) —
		// второй точки выхода не появилось.

		// Сопоставление клавиши помощи — ровно здесь и один раз в проекте
		// (Фаза 12, POL-03): помощь работает с любого экрана, кроме
		// заставки. Исключение — пока открыта строка команды или любое
		// поле ввода: там вопросительный знак обычный символ ввода, иначе
		// набрать «?» в сообщении коммита было бы нельзя (a.inputActive
		// ниже). Повторное нажатие снимает оверлей — тот же жест, что и esc
		// ниже, потому что помощь оверлей, а не колонка ленты, и закону
		// стрелок не подчиняется. Меню исключено тем же способом, что и
		// заставка (Фаза 14): снятие помощи ниже возвращает overlayNone
		// безусловно, а на пустой ленте это была бы пустая лента, а не
		// меню, откуда помощь открыли; своей строки клавиш меню не
		// заводит, и пропуск здесь не отнимает у него видимость перечня
		// клавиш — он доступен с любой колонки ленты.
		// a.overlay != overlayLog — помощь не открывается поверх просмотрщика
		// лога (Фаза 15, план 15-01): колода оверлеев одна, второго уровня
		// вложенности не заводится — помощь доступна сразу после закрытия
		// просмотрщика, а оверлей поверх оверлея пришлось бы разбирать в
		// обратном порядке, и первая же ошибка в этом порядке оставила бы
		// человека в экране без выхода.
		if key.Matches(msg, a.keys.Help) && a.overlay != overlaySplash && a.overlay != overlayMenu && a.overlay != overlayLog && !a.inputActive() {
			if a.overlay == overlayHelp {
				a.overlay = overlayNone
				return a, nil
			}
			a.help = newHelpModel(a.theme, a.keys, a.screen())
			a.help = a.help.setSize(a.width, a.height)
			a.overlay = overlayHelp
			return a, nil
		}
		// esc снимает оверлей помощи тем же жестом, что и повторное `?` —
		// возвращаться специально некуда: лента не менялась, пока оверлей
		// был открыт. q закрывает помощь тем же жестом, что и esc (Фаза 18,
		// KEYS-02) — второго места закрытия помощи не появляется.
		if a.overlay == overlayHelp && (key.Matches(msg, a.keys.Back) || key.Matches(msg, a.keys.Quit)) {
			a.overlay = overlayNone
			return a, nil
		}

		// Клавиша подтверждения на строке скрытой переменной открывает поле
		// ввода значения ДО применения закона стрелок (Фаза 15, план
		// 15-02) — ЕДИНСТВЕННОЕ отступление от синонима «клавиша
		// подтверждения — то же, что стрелка вправо» (план 13-01):
		// idea-0.3.1 §6 называет оба правила буквально и в одном разделе —
		// стрелка вправо переключает вид, а клавиша подтверждения вписывает
		// значение. Отступление узкое по построению: оно действует ровно в
		// одном виде одной колонки и только когда под курсором есть строка
		// (jobModel.fillsSecret); во всех остальных местах обе клавиши
		// по-прежнему ведут в одну функцию углубления (navFor ниже).
		// Фаза 20 (план 20-02, VAR-01) расширяет условие дизъюнкцией с
		// jobModel.fillsOverride — та же ветка ЕДИНСТВЕННОГО отступления, а
		// не второе: оба предиката сужены по своему виду одной и той же
		// колонки (detailSecrets у fillsSecret, detailEnv у fillsOverride),
		// startFill уже разбирает оба случая (задача 1).
		if a.overlay == overlayNone && !a.inputActive() && key.Matches(msg, a.keys.Open) && (a.job.fillsSecret(a.ribbon.focusedID()) || a.job.fillsOverride(a.ribbon.focusedID())) {
			var cmd tea.Cmd
			a.job, cmd = a.job.startFill()
			return a, cmd
		}

		// Единственное место применения закона ленты колонок (Фаза 13,
		// NAV-01…NAV-04): срабатывает только когда нет оверлея и ни одно
		// поле ввода не захватывает ввод — иначе стрелка внутри строки
		// команды или фильтра списка перестала бы двигать курсор в тексте.
		// Заставка исключена тем же условием (a.overlay != overlayNone,
		// пока идёт overlaySplash): её esc/⏎ разбирает она сама, решение
		// того же рода, что и у Quit/Help выше.
		if a.overlay == overlayNone && !a.inputActive() {
			switch navFor(msg, a.keys) {
			case navDeeper:
				return a.openDeeper(false)
			case navConfirm:
				return a.openDeeper(true)
			case navShallower:
				a.ribbon = a.ribbon.shallower()
				return a.focusColumn(), nil
			case navLeftmost:
				// На самой левой колонке (и на пустой ленте) esc/q больше не
				// выходят из утилиты (Фаза 18, KEYS-02): leftmost()/
				// focusColumn() на уже самой левой колонке не делают ничего
				// лишнего (тот же приём, что уже применяет shallower() для
				// ←) — выход остался на Cancel и на :q.
				a.ribbon = a.ribbon.leftmost()
				return a.focusColumn(), nil
			}
			// navNone — клавиша пропускается дальше, к подмодели, обычным
			// путём (финальный диспетчер внизу функции).
		}

	case tea.MouseClickMsg:
		// Клик мышью — ДОПОЛНИТЕЛЬНЫЙ путь к тому же действию, что уже
		// делает ⏎/→ (закон ленты, Фаза 13): второго пути открытия джобы,
		// отличного от уже существующего openDeeper, план не заводит (Фаза
		// 16, LOOK-05). Отдельная ветвь сообщения, а не внутри
		// tea.KeyPressMsg выше — разный тип сообщения.
		m := msg.Mouse()
		if m.Button != tea.MouseLeft {
			// Только левая кнопка: колесо, правая и средняя кнопки этот
			// план не разбирает.
			return a, nil
		}
		// Та же охрана, что уже стоит перед законом стрелок (navFor) выше:
		// клик поверх заставки, меню, проводника, помощи, просмотрщика лога
		// или открытого поля ввода не должен ничего делать — у этих экранов
		// свой разбор ввода, а не лента.
		if a.overlay != overlayNone || a.inputActive() {
			return a, nil
		}
		_, _, widths := a.visibleLayout()
		id, row, ok := a.ribbon.hitTest(widths, m.X, m.Y, a.contentTop())
		if !ok {
			// Промах мимо всех колонок (зазор между ними, поля кадра) —
			// ничего не делает.
			return a, nil
		}
		return a.clickRow(id, row)

	case menuLinkMsg:
		// Пункт «открыть джобу по ссылке» (Фаза 14, MENU-01): заставка
		// пересобирается конструктором вопроса — второй заставки в проекте
		// не появляется, эта же модель отвечает и на прямую ссылку.
		a.splash = newSplashModel()
		a.splash.width, a.splash.height = a.width, a.height
		a.overlay = overlaySplash
		return a, a.splash.init()

	case menuBrowseMsg:
		// Пункт репозиториев (Фаза 14, MENU-01/MENU-03): перечень хостов
		// уже решён меню (menuModel.enabled) — второе чтение здесь стало бы
		// вторым местом того же решения.
		var cmd tea.Cmd
		a.splash, cmd = newSplashBrowse(msg.Hosts)
		a.splash.width, a.splash.height = a.width, a.height
		a.overlay = overlaySplash
		return a, cmd

	case splashBackMsg:
		// Возврат с заставки в меню (Фаза 14): за время на заставке человек
		// мог сохранить ключ через экран проводника (план 14-01, задача 2)
		// — меню обязано это заметить, а не показывать список хостов, каким
		// он был до захода на заставку.
		a.menu = a.menu.refreshHosts()
		a.overlay = overlayMenu
		return a, nil

	case jobRefMsg:
		// Переключение на список джоб и передача выбранной джобы —
		// splashModel уже разобрал ссылку и получил метаданные джобы; клиент
		// обхода собирается здесь же (входа с прямой ссылки без обхода не
		// было в Фазе 9, и второго места сборки клиента заводить незачем).
		// Доступ кладётся в карту по хосту ссылки (Фаза 14, план 14-02)
		// вместо прежних четырёх полей; поведение при отказе (строка ошибки
		// на экране списка джоб) не меняется.
		loadErr := ""
		var b *browse.Client
		if p, bc, err := resolveBrowse(msg.ref.Host); err != nil {
			loadErr = err.Error()
		} else {
			a.access[msg.ref.Host] = hostAccess{Host: msg.ref.Host, Provider: p, Client: bc}
			b = bc
		}
		a.jobs = newJobsModel(msg.ref, msg.job, b, loadErr, a.theme, a.keys)
		// По прямой ссылке репозиториев и пайплайнов слева нет — самая
		// левая колонка ленты становится колонка джоб, и esc из неё выходит.
		// Уточнение заголовка — идентификатор пайплайна, ветка и
		// сокращённый коммит (Фаза 13, план 13-02, задача 2): раньше это
		// печатала собственная шапка панели, теперь её ставит корневая
		// модель при открытии колонки.
		a.ribbon = a.ribbon.reset(colJobs, a.jobs.columnLabel())
		a = a.focusColumn()
		a.overlay = overlayNone
		return a, a.jobs.load()

	case browseMsg:
		// Вход в браузер без ссылки (Фаза 10, BROW-01; Фаза 14, план 14-02):
		// значение интерфейса провайдера первого хоста уже создано заставкой
		// (CurrentUser), клиент обхода собирается здесь же поверх него и
		// кладётся в карту доступа; дерево собирается ВСЕМ перечнем хостов
		// сообщения (заставка проверяла только первый — опрашивать все хосты
		// на входе нельзя, это отказ в обслуживании самому себе и чужим
		// серверам), остальные корни раскрываются по требованию.
		access := hostAccess{Host: msg.Host, Provider: msg.Provider, Client: browse.New(msg.Provider, msg.Host), User: msg.User}
		a.access[msg.Host] = access
		var cmd tea.Cmd
		a.tree, cmd = newTreeModel(msg.Hosts, access, a.theme, a.keys)
		a.treeReady = true
		a.ribbon = a.ribbon.reset(colRepos, "")
		// Размер дерева берётся ПОСЛЕ сброса ленты и treeReady=true, тем же
		// единым переходом, что и любой другой (Фаза 17, focusColumn):
		// до сброса лента ещё держит колонки прошлой ветки, и ширины
		// пришли бы от них.
		a = a.focusColumn()
		a.overlay = overlayNone
		return a, cmd

	case needHostMsg:
		// Дерево просит открыть хост (Фаза 14, план 14-02): собирать
		// клиента обхода умеет только эта команда (openHostCmd выше).
		return a, a.openHostCmd(msg.host)

	case hostReadyMsg:
		// Готовый доступ кладётся в карту корневой модели и НЕ перехватывает
		// сообщение (нет return): оно обязано дойти и до treeModel.update
		// тем же диспетчером, каким доезжает ответ проводника до открывшего
		// его экрана. Доступ хранится в двух местах намеренно — корневой
		// модели он нужен, чтобы открывать пайплайны, джобы и сессию,
		// дереву — чтобы спрашивать узлы, а передавать его сообщением
		// дешевле, чем заводить общую изменяемую карту на две модели.
		a.access[msg.access.Host] = msg.access

	case projectPickedMsg:
		// Курсор дерева встал на проект: колонка пайплайнов открывается в
		// ленте, если её ещё нет; если она уже есть, обновляется только её
		// уточнение заголовка — правая панель и так следит за курсором
		// дребезгом (setProject ниже, по дispatcher'у), второй загрузки
		// заводить не нужно. Сообщение НЕ перехватывается (нет return) —
		// оно обязано дойти и до treeModel.update, который передаст проект
		// правой панели.
		label := Plain(msg.project.FullPath)
		if a.ribbon.has(colPipelines) {
			for i := range a.ribbon.cols {
				if a.ribbon.cols[i].id == colPipelines {
					a.ribbon.cols[i].label = label
				}
			}
			break
		}
		var dropped []columnID
		a.ribbon, dropped = a.ribbon.open(colPipelines, label)
		a, _ = a.dropped(dropped)
		a = a.focusColumn()

	case openPipelineMsg:
		// Открытие пайплайна с правой панели экрана дерева (BROW-03; Фаза
		// 14, план 14-02): клиент берётся из карты доступа по ХОСТУ
		// СООБЩЕНИЯ, а не из состояния корневой модели — «текущего хоста»
		// после этого плана не существует. Экрана джоб второго в проекте не
		// появляется — тот же jobsModel, второй конструктор.
		a.jobs = newJobsModelFromPipeline(a.access[msg.host].Client, msg.host, msg.project, msg.pipeline, a.theme, a.keys)
		var dropped []columnID
		// Уточнение заголовка — тем же приёмом, что и у входа по прямой
		// ссылке выше (jobRefMsg): идентификатор пайплайна, ветка и
		// сокращённый коммит, а не только номер и ветка, как раньше.
		a.ribbon, dropped = a.ribbon.open(colJobs, a.jobs.columnLabel())
		var cleanup tea.Cmd
		a, cleanup = a.dropped(dropped)
		a = a.focusColumn()
		return a, tea.Batch(cleanup, a.jobs.load())

	case quitMsg:
		// Команда `:q` любого экрана приходит сюда, а не завершает программу
		// сама: экран не знает про уборку сессии, а точка выхода в проекте
		// одна (CR-01).
		return a, a.quitCmd()

	case openJobMsg:
		// Мост подключается к программе ровно здесь и один раз: программа
		// Bubble Tea уже существует (programSend поставлен в Run), а
		// экран джобы ещё нет — создание сессии, моста и подготовка идут
		// одним переходом, чтобы второе место подключения не появилось.
		// Колонка шагов кладётся в ленту ДО пересборки a.job: если справа от
		// колонки джоб уже была колонка шагов (человек уже открывал одну
		// джобу и вернулся выбрать другую), она попадёт в перечень
		// отброшенных, и dropped уберёт её сессию — ту самую, которую
		// раньше здесь закрывали вручную (Close идемпотентен, поэтому
		// повторное закрытие в редком случае гонки ничего не стоит).
		var dropped []columnID
		a.ribbon, dropped = a.ribbon.open(colSteps, Plain(msg.job.Name))
		var cleanup tea.Cmd
		a, cleanup = a.dropped(dropped)

		a.sessionGen++
		bridge := NewBridge(a.sessionGen)
		bridge.Attach(programSend)
		// Значение интерфейса провайдера берётся из карты доступа по хосту
		// ссылки экрана списка джоб (Фаза 14, план 14-02) — состояние
		// «текущий хост» корневой модели не существует.
		sess := NewSession(a.jobs.ref, a.access[a.jobs.ref.Host].Provider, msg.job, event.Emitter{Sink: bridge})
		liveSession.Store(sess)
		a.job = newJobModel(sess, a.sessionGen, a.theme, a.keys)
		a = a.focusColumn()
		return a, tea.Batch(cleanup, a.job.init())

	case openDetailMsg:
		// Колонка окружения·секретов·лога (Фаза 13, задача 3,
		// internal/ui/job.go): единственная колонка фазы, у которой нет
		// своих данных для загрузки — сообщение существует ради того же
		// единого правила, по которому открываются все остальные колонки.
		var dropped []columnID
		a.ribbon, dropped = a.ribbon.open(colDetail, "")
		var cleanup tea.Cmd
		a, cleanup = a.dropped(dropped)
		a = a.focusColumn()
		return a, cleanup

	case openLogMsg:
		// Полноэкранный просмотрщик лога (Фаза 15, план 15-01): собирается
		// заново на каждое открытие из готовых строк сообщения (комментарий
		// типа openLogMsg — internal/ui/logview.go), а не из ссылки на
		// источник. Размер — тот же расчёт, каким тело кадра считается на
		// tea.WindowSizeMsg ниже (title+пустая+тело+пустая+клавиши+пустая+
		// подсказка сверху, плюс пустая строка и строка положения внутри
		// самого тела — восемь строк накладных расходов).
		v := newLogView().setTitle(msg.title)
		v = v.setSize(a.width-2*OuterMargin, a.height-8)
		v = v.setSource(msg.tail, msg.mark, msg.pull)
		v = v.setLines(msg.lines)
		a.logv = v
		a.overlay = overlayLog
		return a, nil

	case logDismissMsg: // сброс a.logv = logView{} — вторая копия лога не переживает закрытия просмотрщика (T-15-04)
		// Оверлей снимается, и поле просмотрщика обнуляется целиком: строки
		// лога — вторая копия текста, в котором может оказаться значение
		// секрета, случайно напечатанное самим шагом; источник (память
		// панели, буфер сессии) свою копию и так держит, а вторая переживала
		// бы закрытие просмотрщика без всякой пользы.
		a.overlay = overlayNone
		a.logv = logView{}
		return a, nil

	case guideMsg:
		// Экран проводника (Фаза 11) перекрывает ленту оверлеем — лента при
		// этом не меняется, и снятие оверлея само возвращает человека туда,
		// где он был.
		a.guide = newGuideModel(msg.Guide, a.theme, a.keys)
		a.overlay = overlayGuide
		if msg.Guide.Kind == guideInput {
			return a, textinput.Blink
		}
		return a, nil

	case guideDismissMsg:
		a.overlay = overlayFor(a.guide.Guide.Where)
		return a, nil

	case guideDoneMsg:
		// Обработка guideDoneMsg не выполняется здесь: сообщение уходит
		// тому экрану, который открыл проводника, — корневая модель только
		// восстанавливает оверлей по месту поломки (overlayFor), а смысл
		// ответа решает уже он (диспетчер ниже, теперь читающий a.screen(),
		// который снова указывает на нужный оверлей либо на ленту).
		a.overlay = overlayFor(a.guide.Guide.Where)
	}

	switch a.screen() {
	case screenMenu:
		var cmd tea.Cmd
		a.menu, cmd = a.menu.update(msg)
		return a, cmd
	case screenSplash:
		var cmd tea.Cmd
		a.splash, cmd = a.splash.update(msg)
		return a, cmd
	case screenJobs:
		var cmd tea.Cmd
		a.jobs, cmd = a.jobs.update(msg)
		return a, cmd
	case screenJob:
		var cmd tea.Cmd
		a.job, cmd = a.job.update(msg)
		return a, cmd
	case screenTree:
		var cmd tea.Cmd
		a.tree, cmd = a.tree.update(msg)
		return a, cmd
	case screenGuide:
		var cmd tea.Cmd
		a.guide, cmd = a.guide.Update(msg)
		return a, cmd
	case screenHelp:
		var cmd tea.Cmd
		a.help, cmd = a.help.Update(msg)
		return a, cmd
	case screenLog:
		// Разбор клавиш просмотрщика живёт целиком внутри logView.update
		// (Фаза 15, план 15-01, задача 2) — второго места разбора здесь не
		// заводится; возврат из него шлёт logDismissMsg{}, который приходит
		// сюда же новым сообщением и обрабатывается веткой выше.
		var cmd tea.Cmd
		a.logv, cmd = a.logv.update(msg, a.keys)
		return a, cmd
	}
	return a, nil
}

// View — тело отрисовки обёрнуто единственным перехватом проекта (guard,
// internal/ui/recover.go, Фаза 12, POL-04). При перехваченной панике
// возвращается полный кадр отказа (PanicFrame), а не прежний кадр поверх
// которого что-то дорисовалось — контракт «Вход и выход» (09-UI-SPEC.md)
// требует полной перерисовки везде, где кадр мог остаться недорисованным,
// и паника внутри viewInner — ровно такой случай.
// Полноэкранный режим в Bubble Tea v2 объявляется кадром, а не опцией
// программы: AltScreen — поле возвращаемого View. Выход из режима
// библиотека делает сама при завершении программы.
func (a App) View() tea.View {
	var out string
	if err := guard(func() { out = a.viewInner() }); err != nil {
		v := tea.NewView(PanicFrame(a.theme, a.width, a.height))
		v.AltScreen = true
		// Мышь включена и на кадре паники тем же полем, что и ниже (см.
		// комментарий у второго v.MouseMode) — второй формулы включения в
		// файле не появляется.
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}
	v := tea.NewView(out)
	v.AltScreen = true
	// Мышь включена (Фаза 16, LOOK-05): в установленной версии
	// charm.land/bubbletea/v2 (v2.0.8) мышь включается ПОЛЕМ возвращаемого
	// tea.View, а не опцией tea.NewProgram (такой опции в этой версии нет
	// вовсе) — тем же приёмом, каким уже включён альт-экран выше (см. commit
	// cc32953, .claude/CLAUDE.md история проекта). MouseModeCellMotion даёт
	// клики, отпускания и колесо — этому плану нужны только клики левой
	// кнопкой (updateInner, case tea.MouseClickMsg ниже).
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// viewInner собирает кадр экрана. Раскладка кадра общая для всех экранов и
// собирается ровно здесь — единственное место сборки вертикального ритма
// (заголовок, тело, строка клавиш, строка подсказки): разъехавшийся ритм
// между экранами первым бросается в глаза. С плана 13-02 тело кадра, когда
// оверлея нет, берётся у ribbonView() (несколько колонок рядом), а не у
// одной подмодели целиком; заставка, проводник и помощь по-прежнему
// занимают кадр целиком — они оверлеи, а не колонки (план 13-01), и рисуют
// себя сами, тем же способом, каким рисовали себя до этой фазы.
func (a App) viewInner() string {
	if (a.width > 0 && a.width < MinWidth) || (a.height > 0 && a.height < MinHeight) {
		return TooSmall(a.width, a.height)
	}

	// Стиль заголовка проходит через toy.titleStyle ДО рендера (Фаза 16,
	// план 16-04, TOY-01): дыхание — единственный вариант игрушки, меняющий
	// яркость самого слова «ci-shell» вместо отдельной полосы (см. вставку
	// полосы в самом конце сборки заголовка, ниже). showToy решается один
	// раз здесь же и переиспользуется там — второго вызова toyVisible() в
	// этой функции не заводится.
	titleStyle := a.theme.ScreenTitle
	showToy := a.toyVisible()
	if showToy {
		titleStyle = a.toy.titleStyle(titleStyle)
	}
	title := titleStyle.Render("ci-shell")
	body := ""
	keybar := ""
	hint := ""

	// Подпись автора (MENU-05) собирается ровно в одном месте кадра: до
	// разбора экранов, условием «экран меню или экран заставки» — это два
	// лица одного входа, меню спрашивает, куда идти, заставка показывает
	// проверки перед входом. Внутри ленты подписи нет: это след автора, а
	// не баннер (idea-0.3.1 §1).
	if s := a.screen(); s == screenMenu || s == screenSplash {
		title = signatureTitle(a.theme, title, a.width-2*OuterMargin)
	}

	switch a.overlay {
	case overlayMenu:
		// Стартовый экран — весь кадр, не текст в углу (Фаза 18, START-01):
		// рамка растянута на всю доступную ширину и на тот же бюджет высоты,
		// которым меряется ОДНА полноэкранная коробка кадра
		// (ribbon.columnBoxHeight(), Фаза 16/17). Содержимое меню (логотип,
		// подпись, пункты — menuModel.view, задача 1) центрируется внутри
		// рамки лип-глоссом, а не подгоняется в верхний левый угол.
		//
		// boxWidth — полная ширина блока рамки С РАМКОЙ (Фаза 17, живой
		// обзор после плана 17-02: Lip Gloss v2 считает рамку ЧАСТЬЮ Width,
		// та же величина, что уже идёт в a.boxed() ниже) — той же формулой,
		// что уже считает доступную ширину экранов проводника/заставки
		// (a.width - 2*OuterMargin).
		//
		// boxHeight — высота СОДЕРЖИМОГО рамки: columnBoxHeight() — бюджет
		// высоты одной полноэкранной коробки кадра целиком; у меню, в
		// отличие от колонки ленты (columnView), нет отдельной строки
		// заголовка НАД рамкой — заголовок кадра `ci-shell` уже стоит НАД
		// телом всего экрана, поэтому вычитаются только 2 строки самой
		// рамки (верх+низ), а не 3, как у columnView.
		//
		// innerWidth — ширина содержимого ВНУТРИ рамки (boxWidth минус 2
		// ячейки рамки), нужна lipgloss.Place ДО вызова a.boxed(), а не
		// после — та же арифметика, что columnBodyWidth уже применяет к
		// ширине колонки ленты.
		boxWidth := a.width - 2*OuterMargin
		boxHeight := a.ribbon.columnBoxHeight() - 2
		if boxHeight < 1 {
			boxHeight = 1
		}
		innerWidth := boxWidth - 2
		if innerWidth < 1 {
			innerWidth = 1
		}
		centered := lipgloss.Place(innerWidth, boxHeight, lipgloss.Center, lipgloss.Center, a.menu.view(a.theme))
		body = a.boxed(boxWidth, boxHeight, true, centered)
		keybar = KeyBar(a.theme, a.keys)
		hint = RenderHint(a.theme, a.menu.hintText())
	case overlaySplash:
		body = a.splash.view(a.theme)
	case overlayGuide:
		body = a.guide.View(a.width-2*OuterMargin, a.height)
		// Строка клавиш здесь та же короткая форма, что и у остальных
		// экранов (Фаза 12, POL-03) — единственное место сборки этой
		// строки в проекте одно на все экраны, второго перечня нет; какая
		// именно клавиша сработает на конкретной форме проводника,
		// называет строка подсказки (a.guide.hintText()) под ней.
		keybar = KeyBar(a.theme, a.keys)
		hint = RenderHint(a.theme, a.guide.hintText())
	case overlayHelp:
		body = a.help.View()
		keybar = KeyBar(a.theme, a.keys)
		hint = RenderHint(a.theme, a.help.hintText())
	case overlayLog:
		// Тело — окно строк просмотрщика и под ним строка положения
		// (LOG-04); строка клавиш — перечень, который даёт сам просмотрщик
		// (a.logv.keyHints), через единственную отрисовку KeyBarOf, а не
		// через KeyBar с законом ленты — просмотрщик оверлей, закону стрелок
		// не подчиняется, и его собственные esc/движение уже не то же самое,
		// что у ленты.
		body = a.logv.view(a.theme) + "\n\n" + a.logv.statusLine(a.theme)
		keybar = KeyBarOf(a.theme, a.logv.keyHints(a.keys))
		hint = RenderHint(a.theme, a.logv.hintText())
	default:
		// Оверлея нет — тело кадра рисует лента (NAV-03 держится её
		// инвариантом, отрисовка только читает его), кроме особых кадров
		// экрана джобы (подготовка, перенос правок), которые занимают ЛЕНТУ
		// целиком через явный вызов fullFrame (Фаза 13, план 13-02, пункт
		// 6) — корневая модель спрашивает его ДО сборки ленты. Спрашивается
		// он только когда джоба вообще есть в ленте (колонка шагов либо
		// окружения·секретов·лога): на нулевом значении a.job (до первого
		// openJobMsg) фаза по умолчанию равна phasePreparing, и без этой
		// защиты кадр захватывался бы им же на заставке, дереве и списке
		// джоб, которых джоба ещё не касалась.
		hasJobColumn := a.ribbon.has(colSteps) || a.ribbon.has(colDetail)
		full, showFull := "", false
		if hasJobColumn {
			full, showFull = a.job.fullFrame()
		}
		switch {
		case showFull:
			body = full
		case hasJobColumn:
			body = a.ribbonView()
			// Баннер отказа — под лентой, а не внутри колонки (пункт 8):
			// иначе он уезжал бы вместе с колонкой за край при узком окне,
			// а баннер обязан оставаться на виду независимо от того, какая
			// колонка сейчас в кадре.
			if line, ok := a.job.bannerLine(); ok {
				body += "\n\n" + line
			}
		default:
			body = a.ribbonView()
		}
		// Заголовок, строка клавиш и строка подсказки по-прежнему
		// выбираются по ЭКРАНУ КОЛОНКИ В ФОКУСЕ (a.screen()) — ровно тем же
		// способом, каким выбирались по текущему экрану до этой фазы.
		var hintText string
		switch a.screen() {
		case screenJobs:
			title = title + "  " + a.theme.Muted.Render(a.jobs.ref.Host)
			keybar = a.jobs.keyBar()
			hintText = a.jobs.hintText()
		case screenJob:
			keybar = a.job.keyBar()
			hintText = a.job.hintText()
		case screenTree:
			// Уточнение заголовка именем хоста и пользователя снято (Фаза
			// 14, план 14-02): их место теперь в корневой строке своего
			// хоста (internal/ui/tree.go, treeRow.Meta) — заголовок один на
			// кадр, а хостов несколько, и любой выбранный им хост врал бы
			// про остальные корни.
			keybar = a.tree.keyBar()
			hintText = a.tree.hintText()
		}
		// Страховка закона ленты (Фаза 13, план 13-03): формулировка
		// собственной колонки могла оказаться пустой — «подсказка
		// присутствует ВСЕГДА» (09-UI-SPEC.md, «Строка подсказки») не
		// обязана зависеть от того, обо всех ли своих состояниях уже
		// подумала каждая из моделей колонок. На самой левой колонке ленты
		// пустая формулировка заменяется законом: с Фазы 18 (KEYS-02) esc/q
		// отсюда больше не выходят из утилиты — HintColumnLeftmost называет
		// настоящий путь выхода (Cancel, :q), а не эту клавишу.
		if hintText == "" && a.ribbon.atLeftEdge() {
			hintText = HintColumnLeftmost()
		}
		if hintText != "" {
			hint = RenderHint(a.theme, hintText)
		}
	}

	// Полоса игрушки (Фаза 16, план 16-04, TOY-01/TOY-04) — ПОСЛЕДНЯЯ правка
	// title, той же формулой отступа, что уже применяет signatureTitle
	// (internal/ui/menu.go) для подписи «by F3»: измерение УЖЕ отрисованной
	// (ANSI-стилизованной) строки через textwidth.Of безопасно и уже
	// применяется этим же файлом ровно так же. Полоса встаёт ПОСЛЕ всего,
	// что уже дописано в title (подпись автора, имя хоста) — самым правым
	// элементом строки заголовка — и не встаёт вовсе, если для неё не
	// осталось ни одной ячейки (gap < 1): узкий кадр молча остаётся без
	// полосы, а не наезжает на уже написанный текст.
	if showToy {
		if strip := a.toy.strip(a.theme); strip != "" {
			avail := a.width - 2*OuterMargin - textwidth.Of(title)
			if gap := avail - textwidth.Of(strip); gap >= 1 {
				title = title + strings.Repeat(" ", gap) + strip
			}
		}
	}

	margin := strings.Repeat(" ", OuterMargin)
	var b strings.Builder
	b.WriteString(margin)
	b.WriteString(title)
	b.WriteString("\n\n")
	b.WriteString(body)
	b.WriteString("\n\n")
	if keybar != "" {
		b.WriteString(margin)
		b.WriteString(keybar)
		b.WriteString("\n\n")
	}
	if hint != "" {
		b.WriteString(margin)
		b.WriteString(hint)
	}

	return b.String()
}

// Run — точка входа пакета: создаёт корневую модель, создаёт программу
// Bubble Tea с альтернативным экраном и переданным контекстом, запускает
// её и возвращает ошибку.
//
// Встроенный в Bubble Tea перехват паники остаётся включённым — опция,
// которая его выключает, программе не передаётся ни в одном месте: это
// последняя сеть для паники, случившейся вне трёх точек, обёрнутых guard
// (Init, Update, View выше) — например, в горутине долгой операции.
// Сломанный терминал хуже, чем напечатанное значение паники, поэтому
// размен сделан в пользу восстановления (T-12-09).
//
// Сам вызов p.Run() тоже обёрнут guard — на случай паники, вышедшей из
// внутренностей библиотеки, а не из наших трёх точек. После завершения
// программы (штатного или panic) Run проверяет запись о панике: если она
// есть, терминал восстанавливается, честный отчёт (тип паники и
// трассировка, без значения) уходит в поток ошибок ПОСЛЕ восстановления —
// иначе он ушёл бы в альтернативный экран и исчез вместе с ним, — и
// наружу возвращается ошибка вокруг часового ErrRenderPanic. Сама паника
// наружу не пробрасывается ни одним способом: рантайм напечатал бы её
// значение целиком, и вся работа по сокрытию значения (internal/ui/recover.go,
// panicRecord) обнулилась бы.
func Run(ctx context.Context) error {
	// Ctrl-C и SIGTERM отменяют контекст программы — тот же приём и по той
	// же причине, что и в обычном режиме (cmd/ci/main.go, reproduce): без
	// него сигнал завершал бы программу Bubble Tea мимо любой уборки, и
	// контейнер, чекаут и каталог файловых переменных с материализованными
	// секретами оставались бы на машине (CR-01 обзора v0.3.0). Уборка после
	// возврата из p.Run() ниже — вторая половина того же решения.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Возможности терминала определяются ровно один раз здесь, при
	// создании модели (Фаза 12, POL-01) — второе место сборки Caps в
	// проекте не появляется; уточнение фона идёт через отдельное
	// сообщение (см. Update, tea.BackgroundColorMsg).
	caps := DetectCaps()
	theme := NewTheme(caps)
	keys := DefaultKeys()
	// Настройка игрушки читается ровно здесь, тем же приёмом, что уже читает
	// internal/ui/session.go (Фаза 16, план 16-04): ошибка чтения
	// намеренно отбрасывается — сломанный или недоступный файл настроек не
	// должен мешать запуску интерфейса, игрушка просто получит случайный
	// вариант тем же путём, что и пустая настройка (parseToyKind,
	// internal/ui/toy.go).
	settings, _, _ := config.Load()
	app := App{
		caps:   caps,
		theme:  theme,
		keys:   keys,
		splash: newSplashModel(),
		menu:   newMenuModel(theme, keys),
		// access — доступ по хосту (Фаза 14, план 14-02): карта заводится
		// пустой здесь, единственной точкой сборки корневой модели.
		access: map[string]hostAccess{},
		// Стартовый оверлей — меню (Фаза 14, MENU-01): единственная точка
		// входа интерфейса. Меню стоит ПЕРЕД лентой ровно как заставка
		// (Фаза 13, лента ещё пуста), но заставка теперь открывается из
		// меню, а не наоборот.
		overlay: overlayMenu,
		toy:     newToyModel(settings.Animation),
	}
	p := tea.NewProgram(app, tea.WithContext(ctx))
	// Функция отправки существует только теперь — раньше программы не
	// было. Мост событий экрана джобы (internal/ui/bridge.go) подключается
	// к ней при первом открытии джобы (App.Update, openJobMsg).
	programSend = p.Send

	var runErr error
	_ = guard(func() { _, runErr = p.Run() })

	// Последняя уборка: сюда программа приходит и штатным выходом, и по
	// отменённому контексту (сигнал, отмена снаружи), и после паники,
	// пойманной встроенной сетью Bubble Tea. Close идемпотентен, поэтому
	// повторение уже сделанной уборки (quitCmd, dropped) ничего не
	// стоит, а пропущенная — стоила бы живого контейнера и каталога с
	// секретами на диске.
	//
	// Уборка держится на liveSession, а не на разборе типа возвращённой
	// p.Run() модели (CR-02): та модель — nil, если паника поймана
	// встроенным перехватом Bubble Tea вне трёх точек guard, и разбор её
	// типа тогда не срабатывает вовсе, а liveSession
	// обновляется независимо от того, что вернул p.Run(). Close идемпотентен
	// (комментарий выше уже это утверждает), поэтому повторное закрытие уже
	// убранной задачами quitCmd/dropped сессии (liveSession к этому моменту
	// уже nil) ничего не стоит.
	if sess := liveSession.Load(); sess != nil {
		sess.Close()
	}

	if rec := renderPanic.Load(); rec != nil {
		// Последовательности восстановления идут туда же, куда bubbletea
		// рисовало кадры (стандартный вывод); честный отчёт — в поток
		// ошибок, тем же местом, куда уходят все остальные диагностические
		// сообщения проекта (cmd/ci/main.go).
		RestoreTerminal(os.Stdout)
		ReportPanic(os.Stderr, rec)
		return fmt.Errorf("%w", ErrRenderPanic)
	}
	return runErr
}
