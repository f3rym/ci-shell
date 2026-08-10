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
	Up   key.Binding
	Down key.Binding
	// Right, Left — закон ленты колонок (Фаза 13, NAV-01…NAV-02): → всегда
	// переносит фокус в колонку справа, ← — в колонку слева, и это
	// единственное значение обеих клавиш во всём интерфейсе. Второе
	// толкование стрелки где-либо в пакете и есть та поломка, которую фаза
	// чинит — закон применяется ровно в одном месте (navFor,
	// internal/ui/ribbon.go).
	Right   key.Binding
	Left    key.Binding
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
		Right: key.NewBinding(
			key.WithKeys("right", "l"),
			key.WithHelp(GlyphRight, "глубже"),
		),
		Left: key.NewBinding(
			key.WithKeys("left", "h"),
			key.WithHelp(GlyphLeft, "назад"),
		),
		Open: key.NewBinding(
			key.WithKeys("enter"),
			// По закону ленты ⏎ — синоним движения вправо, а не отдельное
			// действие (idea-0.3.1 §3, строка таблицы про ⏎): подпись обязана
			// называть его именно так, а не изображать второе действие.
			key.WithHelp(GlyphEnter, "то же, что "+GlyphRight),
		),
		Back: key.NewBinding(
			key.WithKeys("esc"),
			// esc уводит в самую левую колонку ленты, а из неё — выходит:
			// прежняя подпись «назад» после этой фазы врёт (idea-0.3.1 §3).
			key.WithHelp("esc", "в начало · выход"),
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
		// Cancel — ctrl+c. Подпись называет оба смысла, потому что их
		// действительно два и решает между ними одно место (internal/ui/app.go,
		// cancelsRun): идёт долгая операция на экране джобы — отменяет её;
		// во всех остальных случаях, включая заставку, экран проводника и
		// любое открытое поле ввода, завершает программу.
		Cancel: key.NewBinding(
			key.WithKeys("ctrl+c"),
			key.WithHelp("ctrl+c", "отменить операцию или выйти"),
		),
	}
}

// ShortHelp — короткая форма для строки клавиш внизу экрана (KeyBar ниже):
// движение по колонке, вправо, влево, esc, помощь, выход — закон ленты
// колонок (Фаза 13, NAV-01). Open (⏎) в короткой форме не участвует: она
// синоним Right, и задваивать её в строке клавиш незачем — в полной форме
// она остаётся и там названа синонимом. Реализация интерфейса помощи
// компонента Bubbles.
//
// Ширина получившейся строки укладывается в 78 ячеек (пол 80 минус отступы
// от краёв):
//
//	"↑↓ выбор"            → 2+1+5 = 8
//	"→ глубже"             → 1+1+6 = 8
//	"← назад"              → 1+1+5 = 7
//	"esc в начало · выход" → 3+1+16 = 20
//	"? помощь"             → 1+1+6 = 8
//	"q выход"              → 1+1+5 = 7
//	5 разделителей " · "   → 5*3 = 15
//	итого: 8+8+7+20+8+7+15 = 73 <= 78
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Right, k.Left, k.Back, k.Help, k.Quit}
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
		{k.Up, k.Down, k.Right, k.Left, k.Open, k.Back, k.Filter},
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
