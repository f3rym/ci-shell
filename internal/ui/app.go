package ui

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"

	"github.com/f3rym/ci-shell/internal/browse"
	"github.com/f3rym/ci-shell/internal/event"
	"github.com/f3rym/ci-shell/internal/provider"
	"github.com/f3rym/ci-shell/internal/provider/gitlab"
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

	splash splashModel
	jobs   jobsModel
	job    jobModel
	tree   treeModel
	// treeReady — экран дерева собран newTreeModel (browseMsg), и его
	// компонент списка Bubbles можно трогать. Признак явный, а не выведенный
	// из browseClient: клиент обхода выставляется и при входе по прямой
	// ссылке на джобу (jobRefMsg), где newTreeModel не вызывается вовсе, и
	// вывод из него означал бы SetSize на нулевом list.Model при первом же
	// изменении размера окна (CR-04 обзора v0.3.0).
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

	// browseClient, provider, host, user — состояние обхода (Фаза 10):
	// browse.New вызывается ровно в двух местах этого файла (browseMsg и
	// jobRefMsg) и нигде больше в пакете — единственное место, знающее, как
	// собрать клиент обхода поверх значения интерфейса провайдера.
	browseClient *browse.Client
	provider     provider.Provider
	host         string
	user         provider.User

	keys KeyMap
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

// initInner запускает программу пакетом команд: запрос цвета фона
// терминала (цвет фона в Bubble Tea v2 приходит сообщением, а не
// запрашивается синхронно, поэтому первая отрисовка идёт темой по
// умолчанию и перерисовывается по приходу ответа) и старт заставки.
func (a App) initInner() tea.Cmd {
	return tea.Batch(
		tea.RequestBackgroundColor,
		a.splash.init(),
	)
}

// screen — экран, который сейчас видит человек: экран колонки ленты в
// фокусе, а при активном оверлее — экран самого оверлея. Второго места,
// хранящего экран, в пакете больше нет.
func (a App) screen() screen {
	switch a.overlay {
	case overlaySplash:
		return screenSplash
	case overlayGuide:
		return screenGuide
	case overlayHelp:
		return screenHelp
	}
	return a.ribbon.screen()
}

// openDeeper — единственная точка углубления: если правее фокуса колонка
// уже есть, фокус просто переезжает в неё; иначе колонка в фокусе сама
// решает, что открыть, единым вызовом одноимённого метода. «→ открывает
// пайплайны» и «→ открывает панель действий» обязаны быть одним правилом, а
// не двумя.
func (a App) openDeeper() (App, tea.Cmd) {
	if a.ribbon.hasDeeper() {
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
func (a App) focusColumn() App {
	id := a.ribbon.focusedID()
	if a.treeReady {
		a.tree = a.tree.setColumnFocus(id)
	}
	a.jobs = a.jobs.setColumnFocus(id)
	a.job = a.job.setColumnFocus(id)
	return a
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
		return a.job.cmd.active || a.job.apply.stage == applyMessage
	case screenTree:
		return a.tree.list.FilterState() == list.Filtering
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

// updateInner обрабатывает сообщения программы. Общие для всех экранов
// случаи (размер окна, цвет фона, выход, закон ленты) разбираются здесь один
// раз; остальное уходит подмодели текущего экрана.
func (a App) updateInner(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		a.splash.width, a.splash.height = msg.Width, msg.Height
		a.jobs = a.jobs.setSize(msg.Width, msg.Height)
		a.job = a.job.setSize(msg.Width, msg.Height)
		// Окно видимых колонок ленты пересчитывается на том же сообщении,
		// что и всё остальное (Фаза 13).
		a.ribbon = a.ribbon.setSize(msg.Width, msg.Height)
		if a.treeReady {
			// Дерево держит настоящий компонент списка Bubbles (list.Model)
			// — до первого browseMsg он ещё не собран newTreeModel, и
			// SetSize на нулевом значении небезопасен, в отличие от
			// jobsModel/jobModel, которые обходятся простыми полями. Условие
			// проверяет ровно тот факт, который декларирует: собран ли экран
			// дерева, а не собран ли клиент обхода (CR-04).
			a.tree = a.tree.setSize(msg.Width, msg.Height)
		}
		if a.overlay == overlayHelp {
			// Тот же приём, что и у дерева выше: область просмотра экрана
			// помощи (viewport.Model) собрана newHelpModel только при
			// первом открытии `?`, и SetSize до этого небезопасна.
			a.help = a.help.setSize(msg.Width, msg.Height)
		}
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

		if key.Matches(msg, a.keys.Quit) && a.overlay != overlaySplash {
			// Выход идёт единственной точкой на ЛЮБОМ экране, а не только
			// когда текущий экран — джоба: `q` с экрана помощи, открытого с
			// экрана джобы, тоже обязан убрать сессию (CR-01).
			return a, a.quitCmd()
		}
		// Заставка исключена не потому, что с неё нельзя выйти, а потому,
		// что решение принимает она сама (internal/ui/splash.go): пока её
		// поле ввода открыто, `q` — обычный символ ссылки, и завершать
		// программу им нельзя. Закрытое поле ввода она выпускает сама, тем
		// же quitMsg, что и все остальные экраны.

		// Сопоставление клавиши помощи — ровно здесь и один раз в проекте
		// (Фаза 12, POL-03): помощь работает с любого экрана, кроме
		// заставки. Исключение — пока открыта строка команды или любое
		// поле ввода: там вопросительный знак обычный символ ввода, иначе
		// набрать «?» в сообщении коммита было бы нельзя (a.inputActive
		// ниже). Повторное нажатие снимает оверлей — тот же жест, что и esc
		// ниже, потому что помощь оверлей, а не колонка ленты, и закону
		// стрелок не подчиняется.
		if key.Matches(msg, a.keys.Help) && a.overlay != overlaySplash && !a.inputActive() {
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
		// был открыт.
		if a.overlay == overlayHelp && key.Matches(msg, a.keys.Back) {
			a.overlay = overlayNone
			return a, nil
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
				return a.openDeeper()
			case navShallower:
				a.ribbon = a.ribbon.shallower()
				return a.focusColumn(), nil
			case navLeftmost:
				if a.ribbon.atLeftEdge() || a.ribbon.empty() {
					// Завершение здесь идёт исключительно через единственную
					// точку выхода: иначе сессия не уберётся, и на машине
					// останутся живой контейнер, чекаут и каталог файловых
					// переменных с материализованными секретами — ровно тот
					// дефект, который обзор v0.3.0 уже находил (CR-01).
					return a, a.quitCmd()
				}
				a.ribbon = a.ribbon.leftmost()
				return a.focusColumn(), nil
			}
			// navNone — клавиша пропускается дальше, к подмодели, обычным
			// путём (финальный диспетчер внизу функции).
		}

	case jobRefMsg:
		// Переключение на список джоб и передача выбранной джобы —
		// splashModel уже разобрал ссылку и получил метаданные джобы; клиент
		// обхода собирается здесь же (входа с прямой ссылки без обхода не
		// было в Фазе 9, и второго места сборки клиента заводить незачем).
		loadErr := ""
		var b *browse.Client
		if p, bc, err := resolveBrowse(msg.ref.Host); err != nil {
			loadErr = err.Error()
		} else {
			a.provider, a.browseClient, a.host = p, bc, msg.ref.Host
			b = bc
		}
		a.jobs = newJobsModel(msg.ref, msg.job, b, loadErr, a.theme, a.keys)
		a.jobs = a.jobs.setSize(a.width, a.height)
		// По прямой ссылке репозиториев и пайплайнов слева нет — самая
		// левая колонка ленты становится колонка джоб, и esc из неё выходит.
		a.ribbon = a.ribbon.reset(colJobs, "")
		a.overlay = overlayNone
		return a, a.jobs.load()

	case browseMsg:
		// Вход в браузер без ссылки (Фаза 10, BROW-01): значение интерфейса
		// провайдера уже создано заставкой (CurrentUser), клиент обхода
		// собирается здесь же поверх него.
		a.provider = msg.Provider
		a.host = msg.Host
		a.user = msg.User
		a.browseClient = browse.New(msg.Provider, msg.Host)
		a.tree = newTreeModel(a.browseClient, a.host, a.user, a.theme, a.keys)
		a.treeReady = true
		a.tree = a.tree.setSize(a.width, a.height)
		a.ribbon = a.ribbon.reset(colRepos, "")
		a.overlay = overlayNone
		return a, a.tree.loadRoot(false)

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
		// Открытие пайплайна с правой панели экрана дерева (BROW-03):
		// экрана джоб второго в проекте не появляется — тот же jobsModel,
		// второй конструктор.
		a.jobs = newJobsModelFromPipeline(a.browseClient, a.host, msg.project, msg.pipeline, a.theme, a.keys)
		a.jobs = a.jobs.setSize(a.width, a.height)
		var dropped []columnID
		a.ribbon, dropped = a.ribbon.open(colJobs, Plain(fmt.Sprintf("#%d · %s", msg.pipeline.IID, msg.pipeline.Ref)))
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
		sess := NewSession(a.jobs.ref, a.provider, msg.job, event.Emitter{Sink: bridge})
		a.job = newJobModel(sess, a.sessionGen, a.theme, a.keys)
		a.job = a.job.setSize(a.width, a.height)
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
		a.overlay = overlayNone
		return a, nil

	case guideDoneMsg:
		// Обработка guideDoneMsg не выполняется здесь: сообщение уходит
		// тому экрану, который открыл проводника, — корневая модель только
		// снимает оверлей, а смысл ответа решает уже он (диспетчер ниже,
		// теперь читающий a.screen(), который снова указывает на ленту).
		a.overlay = overlayNone
	}

	switch a.screen() {
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
		return v
	}
	v := tea.NewView(out)
	v.AltScreen = true
	return v
}

// viewInner собирает кадр экрана. Раскладка кадра общая для всех экранов и
// собирается ровно здесь — единственное место сборки вертикального ритма
// (заголовок, тело, строка клавиш, строка подсказки): разъехавшийся ритм
// между экранами первым бросается в глаза. Отрисовка ленты (несколько
// колонок рядом вместо одного экрана за раз) — план 13-02, здесь кадр
// по-прежнему собирается по экрану в фокусе, который называет a.screen().
func (a App) viewInner() string {
	if (a.width > 0 && a.width < MinWidth) || (a.height > 0 && a.height < MinHeight) {
		return TooSmall(a.width, a.height)
	}

	title := a.theme.ScreenTitle.Render("ci-shell")
	body := ""
	keybar := ""
	hint := ""

	switch a.screen() {
	case screenSplash:
		body = a.splash.view(a.theme)
	case screenJobs:
		title = title + "  " + a.theme.Muted.Render(a.jobs.ref.Host)
		body = a.jobs.bodyView()
		keybar = a.jobs.keyBar()
		hint = RenderHint(a.theme, a.jobs.hintText())
	case screenJob:
		body = a.job.bodyView()
		keybar = a.job.keyBar()
		hint = RenderHint(a.theme, a.job.hintText())
	case screenTree:
		title = title + "  " + a.theme.Muted.Render(a.host+" · "+a.user.Name)
		body = a.tree.view()
		keybar = a.tree.keyBar()
		hint = RenderHint(a.theme, a.tree.hintText())
	case screenGuide:
		body = a.guide.View(a.width-2*OuterMargin, a.height)
		// Строка клавиш здесь та же короткая форма, что и у остальных
		// экранов (Фаза 12, POL-03) — единственное место сборки этой
		// строки в проекте одно на все экраны, второго перечня нет; какая
		// именно клавиша сработает на конкретной форме проводника,
		// называет строка подсказки (a.guide.hintText()) под ней.
		keybar = KeyBar(a.theme, a.keys)
		hint = RenderHint(a.theme, a.guide.hintText())
	case screenHelp:
		body = a.help.View()
		keybar = KeyBar(a.theme, a.keys)
		hint = RenderHint(a.theme, a.help.hintText())
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
	app := App{
		caps:   caps,
		theme:  NewTheme(caps),
		keys:   DefaultKeys(),
		splash: newSplashModel(),
		// Стартовый оверлей — заставка: она стоит ПЕРЕД лентой, лента ещё
		// пуста (Фаза 13).
		overlay: overlaySplash,
	}
	p := tea.NewProgram(app, tea.WithContext(ctx))
	// Функция отправки существует только теперь — раньше программы не
	// было. Мост событий экрана джобы (internal/ui/bridge.go) подключается
	// к ней при первом открытии джобы (App.Update, openJobMsg).
	programSend = p.Send

	var runErr error
	var final tea.Model
	_ = guard(func() { final, runErr = p.Run() })

	// Последняя уборка: сюда программа приходит и штатным выходом, и по
	// отменённому контексту (сигнал, отмена снаружи), и после паники,
	// пойманной встроенной сетью Bubble Tea. Close идемпотентен, поэтому
	// повторение уже сделанной уборки (quitCmd, dropped) ничего не
	// стоит, а пропущенная — стоила бы живого контейнера и каталога с
	// секретами на диске.
	if last, ok := final.(App); ok && last.job.session != nil {
		last.job.session.Close()
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
