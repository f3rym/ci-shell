package ui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
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

	// guide/guideReturn — экран проводника по типичным поломкам (Фаза 11):
	// экран проводника всегда знает, откуда пришёл, и esc возвращает ровно
	// туда. Возврат хранится значением, а не вычисляется, потому что одна и
	// та же ситуация приходит и с заставки, и с экрана джобы.
	guide       guideModel
	guideReturn screen

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

// programSend — функция отправки сообщения в запущенную программу Bubble
// Tea; ставится в Run() сразу после tea.NewProgram, потому что раньше самой
// программы не существует. Единственный потребитель — мост событий
// (Bridge.Attach), подключаемый в Update() при переходе на экран джобы: сама
// модель App не хранит ссылку на программу (Bubble Tea её и не передаёт), а
// без пакетной переменной мосту неоткуда было бы взять функцию отправки.
var programSend func(tea.Msg)

// Init запускает программу пакетом команд: запрос цвета фона терминала
// (цвет фона в Bubble Tea v2 приходит сообщением, а не запрашивается
// синхронно, поэтому первая отрисовка идёт темой по умолчанию и
// перерисовывается по приходу ответа) и старт заставки.
func (a App) Init() tea.Cmd {
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

// Update обрабатывает сообщения программы. Общие для всех экранов случаи
// (размер окна, цвет фона, выход, возврат) разбираются здесь один раз;
// остальное уходит подмодели текущего экрана.
func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		a.splash.width, a.splash.height = msg.Width, msg.Height
		a.jobs = a.jobs.setSize(msg.Width, msg.Height)
		a.job = a.job.setSize(msg.Width, msg.Height)
		if a.browseClient != nil {
			// Дерево держит настоящий компонент списка Bubbles (list.Model)
			// — до первого browseMsg он ещё не собран newTreeModel, и
			// SetSize на нулевом значении небезопасен, в отличие от
			// jobsModel/jobModel, которые обходятся простыми полями.
			a.tree = a.tree.setSize(msg.Width, msg.Height)
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
			if a.current == screenJob && a.job.session != nil {
				// Уборка сессии (контейнер, файловые переменные, чекаут)
				// обязана произойти и при выходе прямо с экрана джобы, а не
				// только после явного возврата к списку — иначе выход из
				// интерфейса клавишей q оставлял бы контейнер и чекаут на
				// диске (T-09-18).
				sess := a.job.session
				return a, func() tea.Msg { sess.Close(); return tea.Quit() }
			}
			return a, tea.Quit
		}
		if key.Matches(msg, a.keys.Back) && a.current != screenSplash {
			switch a.current {
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

	case backMsg:
		a = a.back()
		return a, nil

	case openJobMsg:
		// Мост подключается к программе ровно здесь и один раз: программа
		// Bubble Tea уже существует (programSend поставлен в Run), а
		// экран джобы ещё нет — создание сессии, моста и подготовка идут
		// одним переходом, чтобы второе место подключения не появилось.
		bridge := NewBridge()
		bridge.Attach(programSend)
		sess := NewSession(a.jobs.ref, a.provider, msg.job, event.Emitter{Sink: bridge})
		a.job = newJobModel(sess, a.theme, a.keys)
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
	}
	return a, nil
}

// View собирает кадр экрана. Раскладка кадра общая для всех экранов и
// собирается ровно здесь — единственное место сборки вертикального ритма
// (заголовок, тело, строка клавиш, строка подсказки): разъехавшийся ритм
// между экранами первым бросается в глаза.
func (a App) View() tea.View {
	if (a.width > 0 && a.width < MinWidth) || (a.height > 0 && a.height < MinHeight) {
		return tea.NewView(TooSmall(a.width, a.height))
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
		keybar = KeyBar(a.theme, a.keys.Back)
		hint = RenderHint(a.theme, a.guide.hintText())
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

	return tea.NewView(b.String())
}

// Run — точка входа пакета: создаёт корневую модель, создаёт программу
// Bubble Tea с альтернативным экраном и переданным контекстом, запускает
// её и возвращает ошибку.
//
// Перехват паники Bubble Tea остаётся включённым — отключающая его опция
// программе не передаётся: паника в отрисовке иначе оставила бы терминал в
// сломанном режиме (idea-0.3.0 §7; формализация требования — POL-04,
// Фаза 12).
func Run(ctx context.Context) error {
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
	p := tea.NewProgram(app, tea.WithContext(ctx), tea.WithAltScreen())
	// Функция отправки существует только теперь — раньше программы не
	// было. Мост событий экрана джобы (internal/ui/bridge.go) подключается
	// к ней при первом открытии джобы (App.Update, openJobMsg).
	programSend = p.Send
	_, err := p.Run()
	return err
}
