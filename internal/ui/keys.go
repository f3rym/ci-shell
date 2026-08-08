package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"
)

// KeyMap — раскладка клавиш одним объявлением: единственное место в
// проекте, где привязки собраны вместе (idea-0.3.0 §2).
//
// В этой фазе сознательно нет привязки фильтра по списку (появляется в
// Фазе 10 вместе с длинными списками) и привязки экрана помощи (Фаза 12,
// POL-03, `?`) — их отсутствие осознанное, а не забытое.
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
	Quit    key.Binding
	Cancel  key.Binding
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

// KeyBar собирает строку клавиш внизу экрана — единственное место сборки
// этой строки в проекте: клавиша жирным акцентным (Theme.KeyCap), описание
// обычным приглушённым (Theme.KeyDesc), разделитель " · " приглушённый.
// Привязки без отображаемой подписи (например, Down — см. DefaultKeys)
// пропускаются, чтобы пара ↑↓ не задваивалась.
func KeyBar(t Theme, bindings ...key.Binding) string {
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
