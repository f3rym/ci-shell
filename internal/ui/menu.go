// menu.go — меню входа (Фаза 14, MENU-01…MENU-05): точка входа интерфейса.
// Меню не колонка ленты: цепочка idea-0.3.1 §3 начинается с репозиториев, а
// меню стоит ПЕРЕД лентой и отвечает на вопрос «куда идти», пока ленты ещё
// нет вовсе. Закону стрелок (navFor, internal/ui/ribbon.go) меню не
// подчиняется по той же причине, по которой ему не подчиняются заставка,
// проводник и помощь (Фаза 13).
package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"

	"github.com/f3rym/ci-shell/internal/textwidth"
	"github.com/f3rym/ci-shell/internal/token"
)

// gitlabHost — единственный литерал адреса GitLab в файле: пункт
// добавления ключа GitLab запоминает его как хост, для которого
// спрашивается ключ (план 14-01, задача 2).
const gitlabHost = "gitlab.com"

// menuAction — пункт меню, ровно в порядке мокапа idea-0.3.1 §2. Порядок
// объявления и есть порядок на экране — второго перечня порядка не
// заводится.
type menuAction int

const (
	menuRepos menuAction = iota
	menuOpenLink
	menuAddKeyGitLab
	menuAddKeyGitHub
	menuAddKeySelf
)

// menuItem — один пункт меню: действие, подпись, пометка (непустая только
// у GitHub — SoonNote) и номер группы. Групп две: что открыть (1) и ключи
// (2); между ними в кадре — пустая строка вместо разделительной линии
// мокапа, потому что символов псевдографики набор темы не содержит.
type menuItem struct {
	Action menuAction
	Label  string
	Note   string
	Group  int
}

// menuModel — модель меню входа: пункты, курсор, перечень хостов с ключами
// (token.Hosts() — единственный способ узнать «куда мы можем пойти»; второй
// способ, например чтение файла токенов прямо здесь, завёл бы второе
// место, знающее про формат хранения ключей), строка объяснения последней
// попытки, ширина, высота, тема и раскладка.
type menuModel struct {
	items  []menuItem
	cursor int
	hosts  []string
	hint   string
	// pendingHost — хост, для которого сейчас спрашивается ключ (Фаза 14,
	// задача 2). Единственное поле меню, связанное с добавлением ключа —
	// значение самого ключа здесь не оседает ни на миг: оно живёт в поле
	// ввода экрана проводника до подтверждения и уходит в saveToken
	// аргументом, не задерживаясь ни в одной модели.
	pendingHost string

	width  int
	height int

	theme Theme
	keys  KeyMap
}

// menuBrowseMsg — открыть репозитории: перечень хостов едет в сообщении, а
// не читается получателем заново — решение «пункт доступен» уже принято
// enabled() ниже единственным местом, и второе чтение перечня было бы
// вторым решением, способным с ним разойтись.
type menuBrowseMsg struct {
	Hosts []string
}

// menuLinkMsg — спросить ссылку на джобу.
type menuLinkMsg struct{}

// newMenuModel собирает пункты меню ровно в порядке мокапа и читает
// перечень хостов token.Hosts().
func newMenuModel(t Theme, k KeyMap) menuModel {
	items := []menuItem{
		{Action: menuRepos, Label: "репозитории", Group: 1},
		{Action: menuOpenLink, Label: "открыть джобу по ссылке", Group: 1},
		{Action: menuAddKeyGitLab, Label: "добавить ключ GitLab", Group: 2},
		{Action: menuAddKeyGitHub, Label: "добавить ключ GitHub", Note: SoonNote, Group: 2},
		{Action: menuAddKeySelf, Label: "добавить ключ своего инстанса", Group: 2},
	}
	return menuModel{
		items: items,
		hosts: token.Hosts(),
		theme: t,
		keys:  k,
	}
}

// refreshHosts перечитывает перечень хостов: ключ могли сохранить на
// заставке через экран проводника, и меню обязано заметить это сразу, а не
// со следующего запуска.
func (m menuModel) refreshHosts() menuModel {
	m.hosts = token.Hosts()
	return m
}

// setSize запоминает размер тем же приёмом, что остальные экраны.
func (m menuModel) setSize(width, height int) menuModel {
	m.width = width
	m.height = height
	return m
}

// enabled — единственное место решения о доступности пункта: пункт
// репозиториев доступен, только если перечень хостов непуст; пункт GitHub
// недоступен всегда — соблазн «принять ключ и сохранить на будущее» надо
// давить: сохранённый ключ, который ничего не открывает, выглядит как
// поломка утилиты, хуже честного «ещё не сделано» (idea-0.3.1 §8). Второго
// места, решающего про доступность, в файле нет: и отрисовка, и открытие
// спрашивают эту функцию.
func (m menuModel) enabled(it menuItem) bool {
	switch it.Action {
	case menuRepos:
		return len(m.hosts) > 0
	case menuAddKeyGitHub:
		return false
	default:
		return true
	}
}

// disabledHint — какая формулировка объясняет недоступность пункта. Одна
// функция на обе причины, потому что причина недоступности — свойство
// пункта, а не место нажатия.
func (m menuModel) disabledHint(it menuItem) string {
	switch it.Action {
	case menuRepos:
		return HintMenuNeedsKey()
	case menuAddKeyGitHub:
		return HintMenuSoon()
	}
	return ""
}

// open — единственная точка открытия пункта. Недоступный пункт не
// возвращает ни одной команды: он только кладёт в строку объяснения ответ
// disabledHint — попытка открыть даёт деликатную строку внизу, а не отказ
// (idea-0.3.1 §2, MENU-02).
func (m menuModel) open() (menuModel, tea.Cmd) {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return m, nil
	}
	it := m.items[m.cursor]
	if !m.enabled(it) {
		m.hint = m.disabledHint(it)
		return m, nil
	}
	m.hint = ""
	switch it.Action {
	case menuRepos:
		hosts := m.hosts
		return m, func() tea.Msg { return menuBrowseMsg{Hosts: hosts} }
	case menuOpenLink:
		return m, func() tea.Msg { return menuLinkMsg{} }
	case menuAddKeyGitLab:
		m.pendingHost = gitlabHost
		g := guideForNewToken(gitlabHost)
		return m, func() tea.Msg { return guideMsg{Guide: g} }
	case menuAddKeySelf:
		g := guideForNewHost()
		return m, func() tea.Msg { return guideMsg{Guide: g} }
	}
	// menuAddKeyGitHub сюда не доходит — enabled() выше отсекает его раньше:
	// отсутствие ветви и есть реализация требования MENU-04, а не пропуск.
	return m, nil
}

// update разбирает клавиши меню: движение вверх/вниз по привязкам
// раскладки (курсор прижимается к границам перечня, по кругу не ходит) и
// открытие по привязке открытия ИЛИ по привязке движения вправо — после
// Фазы 13 ⏎ синоним стрелки вправо, и меню обязано вести себя так же.
// Движение курсора снимает строку объяснения — иначе объяснение, вызванное
// недоступным пунктом, висело бы поверх подсказки следующего. Меню — вход в
// интерфейс, дальше назад идти некуда (Фаза 18, KEYS-02): раньше esc отсюда
// завершал программу — единственный вход был единственным выходом, теперь
// выход остался только на Cancel, которую перехватывает корневая модель
// раньше, чем сообщение доходит сюда, — esc и q на меню не делают ничего, и
// явная ветка называет это решение, а не прячет его за отсутствием case.
func (m menuModel) update(msg tea.Msg) (menuModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.keys.Up):
			if m.cursor > 0 {
				m.cursor--
			}
			m.hint = ""
			return m, nil
		case key.Matches(msg, m.keys.Down):
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
			m.hint = ""
			return m, nil
		case key.Matches(msg, m.keys.Open), key.Matches(msg, m.keys.Right):
			return m.open()
		case key.Matches(msg, m.keys.Back), key.Matches(msg, m.keys.Quit):
			return m, nil
		}
		return m, nil
	case guideDoneMsg:
		return m.applyGuideDone(msg)
	}
	return m, nil
}

// applyGuideDone разбирает ответ экрана добавления ключа/адреса, открытого
// меню (Фаза 14, задача 2): действие сохранения токена вызывает
// единственную точку сохранения (saveToken, internal/ui/guide.go) с
// запомненным хостом и пришедшим значением — при успехе перечень хостов
// перечитывается сразу же, а не со следующего запуска. Действие «задан
// адрес инстанса» приводит адрес к голому виду единственной формулой
// пакета токенов (token.NormalizeHost) и открывает экран ввода ключа для
// него. Значение ответа ни в одной из двух ветвей не присваивается полю
// модели: ключ уходит аргументом в saveToken, адрес — через приведение.
func (m menuModel) applyGuideDone(msg guideDoneMsg) (menuModel, tea.Cmd) {
	switch msg.Action {
	case actionSaveToken:
		host := m.pendingHost
		if _, err := saveToken(host, msg.Value); err != nil {
			m.hint = HintMenuKeyNotSaved(err.Error())
			return m, nil
		}
		m = m.refreshHosts()
		m.hint = HintMenuKeySaved(host)
		return m, nil
	case actionSetHost:
		host := token.NormalizeHost(msg.Value)
		m.pendingHost = host
		g := guideForNewToken(host)
		return m, func() tea.Msg { return guideMsg{Guide: g} }
	}
	return m, nil
}

// hintText — строка подсказки меню: строка объяснения последней попытки,
// если она непуста; иначе HintMenuNoKeys() при пустом перечне хостов; иначе
// формулировка пункта под курсором. Подсказка присутствует всегда
// (09-UI-SPEC.md, «Строка подсказки»).
func (m menuModel) hintText() string {
	if m.hint != "" {
		return m.hint
	}
	if len(m.hosts) == 0 {
		return HintMenuNoKeys()
	}
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return ""
	}
	switch m.items[m.cursor].Action {
	case menuRepos:
		return HintMenuRepos(len(m.hosts))
	case menuOpenLink:
		return HintMenuOpenLink()
	case menuAddKeyGitLab:
		return HintMenuAddKey(gitlabHost)
	case menuAddKeyGitHub:
		return HintMenuSoon()
	case menuAddKeySelf:
		return HintMenuAddHost()
	}
	return ""
}

// view отрисовывает пункты: строка под курсором получает символ подсказки
// в роли курсора (мокап idea-0.3.1 §2), остальные — отступ той же ширины.
// Доступная строка под курсором получает инверсию; недоступная строка
// приглушена всегда и инверсию НЕ получает — приглушённость обязана
// остаться видимой под курсором, иначе неактивный пункт под курсором
// перестаёт отличаться от активного. Пометка «скоро» рисуется приглушённым
// стилем справа от подписи — она входит в приглушённую строку GitHub
// целиком, второго стиля для неё не заводится. Каждая строка обрезается по
// доступной ширине графемной мерой проекта; между группами — одна пустая
// строка.
func (m menuModel) view(t Theme) string {
	width := m.width
	if width <= 0 {
		width = MinWidth - 2*OuterMargin
	}
	var b strings.Builder
	prevGroup := 0
	for i, it := range m.items {
		if i > 0 && it.Group != prevGroup {
			b.WriteString("\n")
		}
		prevGroup = it.Group

		cursorGlyph := "  "
		if i == m.cursor {
			cursorGlyph = GlyphHint + " "
		}
		label := it.Label
		if it.Note != "" {
			label = label + " " + it.Note
		}
		avail := width - textwidth.Of(cursorGlyph)
		line := cursorGlyph + Truncate(label, avail)

		switch {
		case !m.enabled(it):
			line = t.Muted.Render(line)
		case i == m.cursor:
			line = t.Selected.Render(line)
		default:
			line = t.Text.Render(line)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// signatureTitle — единственное место сборки подписи автора (MENU-05):
// возвращает заголовок title с подписью Signature, прижатой к правому краю
// доступной ширины width (обе части меряются графемной мерой проекта).
// Если места на подпись не остаётся, возвращается голый заголовок —
// подпись не переносится и не толкает название. Функция живёт здесь, а не
// в теме: тема хранит НАДПИСЬ константой, а решение, где она стоит в
// кадре, принадлежит входному экрану.
func signatureTitle(t Theme, title string, width int) string {
	sig := Signature
	gap := width - textwidth.Of(title) - textwidth.Of(sig)
	if gap < 1 {
		return title
	}
	return title + strings.Repeat(" ", gap) + t.Muted.Render(sig)
}
