package ui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
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

	keys KeyMap
}

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
		return a, nil
	case tea.BackgroundColorMsg:
		a.dark = msg.IsDark()
		a.theme = NewTheme(a.dark)
		return a, nil
	case tea.KeyPressMsg:
		if key.Matches(msg, a.keys.Quit) && a.current != screenSplash {
			return a, tea.Quit
		}
		if key.Matches(msg, a.keys.Back) && a.current != screenSplash {
			a.current = screenSplash
			return a, nil
		}
	}

	if a.current == screenSplash {
		var cmd tea.Cmd
		a.splash, cmd = a.splash.update(msg)
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
	default:
		// screenJobs наполняет задача 3 этого плана, screenJob — план
		// 09-02; здесь только раскладка кадра, чтобы следующие задачи не
		// заводили второе место её сборки.
		keybar = KeyBar(a.theme, a.keys.Back, a.keys.Quit)
		hint = RenderHint(a.theme, "")
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
	_, err := p.Run()
	return err
}
