// help.go — экран помощи (Фаза 12, POL-03): `?` с любого экрана, кроме
// заставки и открытого поля ввода, показывает перечень всех клавиш и всех
// команд. Перечень собирается из тех же двух структур, которыми клавиши
// обрабатываются (KeyMap.FullHelp, internal/ui/keys.go) и команды
// разбираются (knownCommands, internal/ui/command.go) — второй список в
// проекте не появляется: он разошёлся бы с первым на первой же новой
// клавише, и человек читал бы помощь, описывающую вчерашний интерфейс.
package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"

	"github.com/f3rym/ci-shell/internal/textwidth"
)

// helpModel — модель экрана помощи: тема, раскладка, область просмотра для
// длинного перечня (листается сама, если не помещается по высоте — без
// собственной прокрутки) и экран, с которого пришли.
type helpModel struct {
	theme Theme
	keys  KeyMap
	from  screen

	vp viewport.Model

	width, height int
}

// newHelpModel строит экран помощи: содержимое собирается один раз при
// открытии — клавиши и команды не меняются на лету — и кладётся в область
// просмотра.
func newHelpModel(t Theme, k KeyMap, from screen) helpModel {
	m := helpModel{theme: t, keys: k, from: from, vp: viewport.New()}
	m.vp.SetContent(m.render())
	return m
}

// setSize подгоняет область просмотра под кадр: заголовок экрана, строку
// клавиш и строку подсказки рисует корневая модель (App.View) — здесь
// только то, что останется от высоты кадра под них.
func (m helpModel) setSize(width, height int) helpModel {
	m.width, m.height = width, height
	m.vp.SetWidth(width)
	h := height - 6
	if h < 1 {
		h = 1
	}
	m.vp.SetHeight(h)
	return m
}

// Update отдаёт сообщение области просмотра целиком — прокрутка длинного
// перечня уже реализована компонентом Bubbles, второй у экрана помощи не
// появляется.
func (m helpModel) Update(msg tea.Msg) (helpModel, tea.Cmd) {
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

// View рисует содержимое, собранное newHelpModel.
func (m helpModel) View() string {
	return m.vp.View()
}

// hintText — строка подсказки экрана помощи: `esc вернёт туда, откуда
// пришли`, но клавиша не литерал — она читается из той же привязки Back,
// которой пользуется весь остальной интерфейс, чтобы формулировка не
// разошлась с раскладкой при следующей правке.
func (m helpModel) hintText() string {
	return m.keys.Back.Help().Key + " вернёт туда, откуда пришли"
}

// render собирает содержимое экрана: раздел КЛАВИШИ из полной формы помощи
// раскладки, раздел КОМАНДЫ из реестра команд. Ни одного имени клавиши и ни
// одного имени команды в этом файле не написано — оба перечня производные,
// это инвариант, а не аккуратность: второй список расходится с первым на
// первой же новой клавише.
func (m helpModel) render() string {
	var b strings.Builder
	b.WriteString(m.theme.PanelTitle.Render("КЛАВИШИ"))
	b.WriteString("\n")
	b.WriteString(m.renderKeys())
	b.WriteString("\n\n")
	b.WriteString(m.theme.PanelTitle.Render("КОМАНДЫ"))
	b.WriteString("\n")
	b.WriteString(m.renderCommands())
	return b.String()
}

// renderKeys обходит FullHelp раскладки: колонка на группу, строка на
// привязку, клавиша жирным акцентным (KeyCap), описание обычным
// приглушённым (KeyDesc) — те же две роли, что уже применяет строка клавиш
// внизу экрана (KeyBar). Ширина клавиши внутри колонки — подгонка Fit
// пакета интерфейса (делегат internal/textwidth, план 12-01), а не
// сложение длин строк: имена команд и описания короче ключевых слов не
// всегда, и без графемной меры колонка на кириллице поехала бы.
func (m helpModel) renderKeys() string {
	// Первая строка раздела — закон ленты одним предложением (Фаза 13, план
	// 13-03): человек, открывший помощь, узнаёт правило раньше перечня
	// клавиш, а не только по перечислению одних символов без объяснения.
	// Все четыре клавиши читаются из привязок раскладки (k.Right.Help().Key
	// и т. д.) — тот же приём, что и у hintText() экрана помощи (Back) и у
	// перечня ниже, — второго литерала клавиши в файле не появляется:
	// инвариант «ни одного имени клавиши здесь не написано» остаётся в силе
	// и для этой строки.
	k := m.keys
	law := m.theme.Muted.Render(fmt.Sprintf(
		"%s — глубже, %s — назад, %s — то же, что %s, %s — в начало ленты и выход",
		k.Right.Help().Key, k.Left.Help().Key, k.Open.Help().Key, k.Right.Help().Key, k.Back.Help().Key,
	))

	groups := m.keys.FullHelp()
	cols := make([]string, 0, len(groups))
	for _, g := range groups {
		type row struct{ key, desc string }
		var rows []row
		maxKey := 0
		for _, binding := range g {
			h := binding.Help()
			if h.Key == "" && h.Desc == "" {
				continue
			}
			rows = append(rows, row{h.Key, h.Desc})
			if w := textwidth.Of(h.Key); w > maxKey {
				maxKey = w
			}
		}
		if len(rows) == 0 {
			continue
		}
		var col strings.Builder
		for i, r := range rows {
			if i > 0 {
				col.WriteString("\n")
			}
			col.WriteString(m.theme.KeyCap.Render(Fit(r.key, maxKey)))
			col.WriteString("  ")
			col.WriteString(m.theme.KeyDesc.Render(r.desc))
		}
		cols = append(cols, col.String())
	}
	if len(cols) == 0 {
		return law
	}
	// Склейка колонок помощи построчно — тем же зазором PanelGap, что и у
	// ленты (Фаза 13, план 13-02): собственная функция joinPanels ушла из
	// пакета вместе с отрисовкой экрана дерева по двум панелям, второй
	// склейки строк вручную не заводится — Lip Gloss делает то же самое.
	gap := strings.Repeat(" ", PanelGap)
	parts := make([]string, 0, len(cols)*2-1)
	for i, c := range cols {
		if i > 0 {
			parts = append(parts, gap)
		}
		parts = append(parts, c)
	}
	return law + "\n\n" + lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// renderCommands обходит knownCommands (internal/ui/command.go) целиком —
// тот же реестр, которым пользуется разбор командной строки, второго
// перечня имён команд в проекте нет. Отложенная команда (Deferred) получает
// свою честную формулировку вместо описания — контракт плана требует не
// врать о том, что не готово.
func (m helpModel) renderCommands() string {
	type row struct{ label, desc string }
	rows := make([]row, 0, len(knownCommands))
	maxLabel := 0
	for _, c := range knownCommands {
		label := ":" + c.Name
		switch {
		case c.RequiresArg && c.Name == "!":
			label += "<аргумент>"
		case c.RequiresArg:
			label += " <аргумент>"
		}
		desc := c.Desc
		if c.Deferred {
			desc = "пока не реализовано"
		}
		rows = append(rows, row{label, desc})
		if w := textwidth.Of(label); w > maxLabel {
			maxLabel = w
		}
	}
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(m.theme.KeyCap.Render(Fit(r.label, maxLabel)))
		b.WriteString("  ")
		b.WriteString(m.theme.KeyDesc.Render(r.desc))
	}
	return b.String()
}
