package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
)

// KeyMap — раскладка клавиш одним объявлением: единственное место в
// проекте, где привязки собраны вместе (idea-0.3.0 §2). Каждая привязка —
// отдельное поле, отдельная строка структуры, не по несколько имён на
// строку: перечень привязок пересчитывается гейтом (см. FullHelp ниже), и
// объявление в несколько имён на строке сделало бы пересчёт невозможным.
type KeyMap struct {
	Up      key.Binding
	Down    key.Binding
	Open    key.Binding
	Back    key.Binding
	Shell   key.Binding
	Retry   key.Binding
	Apply   key.Binding
	Refresh key.Binding
	Command key.Binding
	Filter  key.Binding
	// Help — привязка экрана помощи (Фаза 12, POL-03, `?`). Единственная
	// новая клавиша этого плана.
	Help   key.Binding
	Quit   key.Binding
	Cancel key.Binding
}

// DefaultKeys задаёт раскладку клавиш ровно по таблице idea-0.3.0 §2.
// Подписи — в нижнем регистре, как требует контракт.
func DefaultKeys() KeyMap {
	return KeyMap{
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			// Отображаемая форма клавиши перемещения — "↑↓"; у привязки
			// "вниз" ниже отображаемой подписи нет, чтобы пара не
			// задваивалась в строке клавиш.
			key.WithHelp(GlyphUp+GlyphDown, "выбор"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
		),
		Open: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp(GlyphEnter, "открыть"),
		),
		Back: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "назад"),
		),
		Shell: key.NewBinding(
			key.WithKeys("s"),
			key.WithHelp("s", "шелл"),
		),
		Retry: key.NewBinding(
			key.WithKeys("R"),
			key.WithHelp("R", "повторить шаг"),
		),
		Apply: key.NewBinding(
			key.WithKeys("A"),
			key.WithHelp("A", "перенести правки"),
		),
		Refresh: key.NewBinding(
			key.WithKeys("g"),
			key.WithHelp("g", "обновить"),
		),
		Command: key.NewBinding(
			key.WithKeys(":"),
			key.WithHelp(":", "команда"),
		),
		// Filter — сам фильтр выполняет компонент списка Bubbles своей
		// внутренней раскладкой (internal/ui/tree.go, Фаза 10); привязка
		// существует затем, чтобы строка клавиш внизу и экран помощи
		// называли клавишу из того же единственного места, что и все
		// остальные.
		Filter: key.NewBinding(
			key.WithKeys("/"),
			key.WithHelp("/", "фильтр"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "помощь"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q"),
			key.WithHelp("q", "выход"),
		),
		Cancel: key.NewBinding(
			key.WithKeys("ctrl+c"),
			key.WithHelp("ctrl+c", "отменить"),
		),
	}
}

// ShortHelp — короткая форма для строки клавиш внизу экрана (KeyBar ниже):
// навигация, открыть, назад, помощь, выход. Реализация интерфейса помощи
// компонента Bubbles.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Open, k.Back, k.Help, k.Quit}
}

// FullHelp — полная форма для экрана помощи (internal/ui/help.go, Фаза 12,
// POL-03), сгруппированная по смыслу колонками: навигация; действия петли
// фикса; обновление и командный режим; помощь и выход.
//
// В этой форме перечислены ВСЕ поля структуры без исключения — это
// инвариант, а не аккуратность: экран помощи читает только полную форму,
// поэтому пропущенная здесь привязка исчезает из помощи целиком. Гейт
// сравнивает число полей структуры выше с числом различных привязок,
// упомянутых здесь, — забыть клавишу в помощи структурно невозможно.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Open, k.Back, k.Filter},
		{k.Shell, k.Retry, k.Apply},
		{k.Refresh, k.Command},
		{k.Help, k.Quit, k.Cancel},
	}
}

// KeyBar собирает строку клавиш внизу экрана — единственное место сборки
// этой строки в проекте: клавиша жирным акцентным (Theme.KeyCap), описание
// обычным приглушённым (Theme.KeyDesc), разделитель " · " приглушённый.
// Источник перечня — короткая форма помощи раскладки (k.ShortHelp), а не
// перечисление привязок аргументами: второго перечня клавиш в проекте не
// остаётся (Фаза 12, POL-03). Привязки без отображаемой подписи (например,
// Down — см. DefaultKeys) пропускаются, чтобы пара ↑↓ не задваивалась.
func KeyBar(t Theme, k KeyMap) string {
	bindings := k.ShortHelp()
	parts := make([]string, 0, len(bindings))
	for _, b := range bindings {
		h := b.Help()
		if h.Key == "" && h.Desc == "" {
			continue
		}
		parts = append(parts, t.KeyCap.Render(h.Key)+" "+t.KeyDesc.Render(h.Desc))
	}
	return strings.Join(parts, t.KeyDesc.Render(" · "))
}
