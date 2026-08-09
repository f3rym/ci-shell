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
// перечисления.
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

	current screen
	// stack — ранее открытые экраны (Фаза 10): путь по интерфейсу перестал
	// быть линейным (заставка → дерево → джобы → джоба, но и заставка →
	// джобы → джоба), и «назад» без стека расходится с тем, откуда человек
	// пришёл.
	stack []screen

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

	// guide/guideReturn — экран проводника по типичным поломкам (Фаза 11):
	// экран проводника всегда знает, откуда пришёл, и esc возвращает ровно
	// туда. Возврат хранится значением, а не вычисляется, потому что одна и
	// та же ситуация приходит и с заставки, и с экрана джобы.
	guide       guideModel
	guideReturn screen

	// help/helpReturn — экран помощи (Фаза 12, POL-03): тот же приём, что и
	// у экрана проводника выше — экран помощи знает, откуда пришёл, и esc
	// (или повторное `?`) возвращает ровно туда.
	help       helpModel
	helpReturn screen

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

// backMsg обрабатывается здесь же (см. Update) — экран сам решает, когда
// его пора послать, объявление типа в internal/ui/tree.go.

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

// push кладёт текущий экран в стек и переключается на next — единственное
// место, меняющее a.current при переходе вперёд по дереву экранов.
func (a App) push(next screen) App {
	a.stack = append(a.stack, a.current)
	a.current = next
	return a
}

// back снимает экран со стека и возвращает его; пустой стек равнозначен
// нынешнему поведению — уходит на заставку.
func (a App) back() App {
	if len(a.stack) == 0 {
		a.current = screenSplash
		return a
	}
	a.current = a.stack[len(a.stack)-1]
	a.stack = a.stack[:len(a.stack)-1]
	return a
}

// inputActive сообщает, захватывает ли текущий экран сырой ввод текста:
// строка команды экрана джоб или джобы, поле сообщения коммита, фильтр
// дерева. Единственный потребитель — сопоставление клавиши помощи в Update
// ниже (Фаза 12, POL-03): пока открыто одно из этих полей, вопросительный
// знак — обычный символ ввода, а не запрос помощи, иначе набрать «?» в
// сообщении коммита было бы нельзя. Экран проводника сюда не входит: для
// него вопрос уже решён выше (ранний break при screenGuide) — сопоставление
// клавиши помощи до него не доходит вовсе, каким бы ни был его собственный
// ввод.
func (a App) inputActive() bool {
	switch a.current {
	case screenJobs:
		return a.jobs.cmd.active
	case screenJob:
		return a.job.cmd.active || a.job.apply.stage == applyMessage
	case screenTree:
		return a.tree.list.FilterState() == list.Filtering
	}
	return false
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
// случаи (размер окна, цвет фона, выход, возврат) разбираются здесь один
// раз; остальное уходит подмодели текущего экрана.
func (a App) updateInner(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		a.splash.width, a.splash.height = msg.Width, msg.Height
		a.jobs = a.jobs.setSize(msg.Width, msg.Height)
		a.job = a.job.setSize(msg.Width, msg.Height)
		if a.treeReady {
			// Дерево держит настоящий компонент списка Bubbles (list.Model)
			// — до первого browseMsg он ещё не собран newTreeModel, и
			// SetSize на нулевом значении небезопасен, в отличие от
			// jobsModel/jobModel, которые обходятся простыми полями. Условие
			// проверяет ровно тот факт, который декларирует: собран ли экран
			// дерева, а не собран ли клиент обхода (CR-04).
			a.tree = a.tree.setSize(msg.Width, msg.Height)
		}
		if a.current == screenHelp {
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
		// Пока открыт экран проводника, нажатия клавиш уходят подмодели
		// целиком, включая привязки выхода и возврата (T-11-10): буква
		// выхода, набранная в поле ввода токена, не должна завершать
		// программу, а esc обязан вернуться на нужный экран через
		// guideDismissMsg проводника, а не через общий стек.
		if a.current == screenGuide {
			break
		}
		if key.Matches(msg, a.keys.Quit) && a.current != screenSplash {
			// Выход идёт единственной точкой на ЛЮБОМ экране, а не только
			// когда текущий экран — джоба: `q` с экрана помощи, открытого с
			// экрана джобы, тоже обязан убрать сессию (CR-01).
			return a, a.quitCmd()
		}
		if key.Matches(msg, a.keys.Back) && a.current != screenSplash {
			switch a.current {
			case screenHelp:
				a.current = a.helpReturn
				return a, nil
			case screenJob:
				if a.job.session != nil {
					sess := a.job.session
					a = a.back()
					return a, func() tea.Msg { sess.Close(); return nil }
				}
				a = a.back()
				return a, nil
			case screenTree, screenJobs:
				// Экран сам решает: снять внутренний фокус/строку команды
				// или попросить о переходе назад сообщением backMsg (case
				// ниже) — Back здесь не перехватывается, а доходит до
				// подмодели обычным путём (диспетчер в конце функции).
			default:
				a = a.back()
				return a, nil
			}
		}

		// Сопоставление клавиши помощи — ровно здесь и один раз в проекте
		// (Фаза 12, POL-03): помощь работает с любого экрана, кроме
		// заставки. Исключение — пока открыта строка команды или любое
		// поле ввода: там вопросительный знак обычный символ ввода, иначе
		// набрать «?» в сообщении коммита было бы нельзя (a.inputActive
		// ниже). Повторное нажатие с экрана помощи возвращает на
		// запомненный экран — тот же жест, что и Back.
		if key.Matches(msg, a.keys.Help) && a.current != screenSplash && !a.inputActive() {
			if a.current == screenHelp {
				a.current = a.helpReturn
				return a, nil
			}
			a.helpReturn = a.current
			a.help = newHelpModel(a.theme, a.keys, a.current)
			a.help = a.help.setSize(a.width, a.height)
			a.current = screenHelp
			return a, nil
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
		a = a.push(screenJobs)
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
		a = a.push(screenTree)
		return a, a.tree.loadRoot(false)

	case openPipelineMsg:
		// Открытие пайплайна с правой панели экрана дерева (BROW-03):
		// экрана джоб второго в проекте не появляется — тот же jobsModel,
		// второй конструктор.
		a.jobs = newJobsModelFromPipeline(a.browseClient, a.host, msg.project, msg.pipeline, a.theme, a.keys)
		a.jobs = a.jobs.setSize(a.width, a.height)
		a = a.push(screenJobs)
		return a, a.jobs.load()

	case quitMsg:
		// Команда `:q` любого экрана приходит сюда, а не зовёт tea.Quit сама:
		// экран не знает про уборку сессии, а точка выхода в проекте одна
		// (CR-01).
		return a, a.quitCmd()

	case backMsg:
		a = a.back()
		return a, nil

	case openJobMsg:
		// Мост подключается к программе ровно здесь и один раз: программа
		// Bubble Tea уже существует (programSend поставлен в Run), а
		// экран джобы ещё нет — создание сессии, моста и подготовка идут
		// одним переходом, чтобы второе место подключения не появилось.
		// Сессия предыдущей джобы (если человек уже открывал одну и вернулся)
		// убирается здесь же: Close идемпотентен, поэтому повторный вызов
		// после esc ничего не делает, а забытая сессия перестаёт существовать.
		if a.job.session != nil {
			a.job.session.Close()
		}
		a.sessionGen++
		bridge := NewBridge(a.sessionGen)
		bridge.Attach(programSend)
		sess := NewSession(a.jobs.ref, a.provider, msg.job, event.Emitter{Sink: bridge})
		a.job = newJobModel(sess, a.sessionGen, a.theme, a.keys)
		a.job = a.job.setSize(a.width, a.height)
		a = a.push(screenJob)
		return a, a.job.init()

	case guideMsg:
		// Экран проводника (Фаза 11) запоминает текущий экран как экран
		// возврата и переключается на себя.
		a.guideReturn = a.current
		a.guide = newGuideModel(msg.Guide, a.theme, a.keys)
		a.current = screenGuide
		if msg.Guide.Kind == guideInput {
			return a, textinput.Blink
		}
		return a, nil

	case guideDismissMsg:
		a.current = a.guideReturn
		return a, nil

	case guideDoneMsg:
		// Обработка guideDoneMsg не выполняется здесь: сообщение уходит
		// тому экрану, который открыл проводник, — корневая модель только
		// возвращает экран, а смысл ответа решает уже он (диспетчер ниже).
		a.current = a.guideReturn
	}

	switch a.current {
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
// между экранами первым бросается в глаза.
func (a App) viewInner() string {
	if (a.width > 0 && a.width < MinWidth) || (a.height > 0 && a.height < MinHeight) {
		return TooSmall(a.width, a.height)
	}

	title := a.theme.ScreenTitle.Render("ci-shell")
	body := ""
	keybar := ""
	hint := ""

	switch a.current {
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
	// повторение уже сделанной уборки (quitCmd, esc с экрана джобы) ничего не
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
