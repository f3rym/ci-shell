package ui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"

	"github.com/f3rym/ci-shell/internal/event"
)

// screen — экран интерфейса. screenJobs и screenJob объявлены здесь же,
// хотя наполняют их задача 3 этого плана и план 09-02 соответственно:
// переключение экранов должно жить в одном месте, а не расползаться
// правкой перечисления.
type screen int

const (
	screenSplash screen = iota
	screenJobs
	screenJob
)

// App — корневая модель Bubble Tea; единственная модель проекта,
// реализующая интерфейс модели Bubble Tea. Подмодели экранов (splashModel,
// далее jobsModel) — обычные структуры с методами обновления и отрисовки,
// возвращающими строку, а не собственные реализации tea.Model.
type App struct {
	theme Theme
	dark  bool

	width  int
	height int

	current screen
	splash  splashModel
	jobs    jobsModel
	job     jobModel

	keys KeyMap
}

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
		return a, nil
	case tea.BackgroundColorMsg:
		a.dark = msg.IsDark()
		a.theme = NewTheme(a.dark)
		return a, nil
	case tea.KeyPressMsg:
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
			if a.current == screenJob && a.job.session != nil {
				sess := a.job.session
				a.current = screenJobs
				return a, func() tea.Msg { sess.Close(); return nil }
			}
			a.current = screenSplash
			return a, nil
		}
	case jobRefMsg:
		// Переключение на список джоб и передача выбранной джобы
		// объявляются здесь: экран джобы ещё пуст (наполняет план 09-02),
		// но перечисление экранов и место переключения не должны
		// расползаться правкой второго плана.
		a.jobs = newJobsModel(msg.ref, msg.job, a.theme, a.keys)
		a.current = screenJobs
		return a, a.jobs.load()
	case openJobMsg:
		// Мост подключается к программе ровно здесь и один раз: программа
		// Bubble Tea уже существует (programSend поставлен в Run), а
		// экран джобы ещё нет — создание сессии, моста и подготовка идут
		// одним переходом, чтобы второе место подключения не появилось.
		bridge := NewBridge()
		bridge.Attach(programSend)
		sess := NewSession(a.jobs.ref, a.jobs.p, msg.job, event.Emitter{Sink: bridge})
		a.job = newJobModel(sess, a.theme, a.keys)
		a.job = a.job.setSize(a.width, a.height)
		a.current = screenJob
		return a, a.job.init()
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
	app := App{
		theme:  NewTheme(true),
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
