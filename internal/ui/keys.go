package ui

import (
	"strings"

	"charm.land/bubbles/v2/key"

	"github.com/f3rym/ci-shell/internal/textwidth"
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

	// Просмотрщик лога, полноэкранный оверлей (Фаза 15, план 15-01,
	// LOG-01…LOG-04): девять привязок ниже применимы только пока он открыт —
	// оверлей забирает клавиши целиком, и второе толкование той же клавиши в
	// какой-нибудь колонке (например, g — обновление списка джоб) не спор, а
	// два разных момента времени.
	//
	// PageUp, PageDown — страница вверх/вниз.
	PageUp   key.Binding
	PageDown key.Binding
	// Top, Bottom — края лога.
	Top    key.Binding
	Bottom key.Binding
	// Search — поиск по логу. Та же клавиша, что и у Filter выше: фильтр
	// принадлежит спискам колонок, поиск — просмотрщику лога, и в один
	// момент времени применима ровно одна из двух.
	Search key.Binding
	// NextMatch, PrevMatch — переход по совпадениям поиска.
	NextMatch key.Binding
	PrevMatch key.Binding
	// Failed — переход к упавшему шагу (LOG-03).
	Failed key.Binding
	// Log — открытие просмотрщика из колонки джоб (LOG-01, JOB-04).
	Log key.Binding
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
		PageUp: key.NewBinding(
			key.WithKeys("pgup"),
			// Отображаемая форма называет обе клавиши страницы сразу; у
			// PageDown отображаемой подписи нет — тот же приём, что и у пары
			// Up/Down выше, чтобы пара не задваивалась в строке клавиш.
			key.WithHelp("pgup/pgdn", "экран"),
		),
		PageDown: key.NewBinding(
			key.WithKeys("pgdown"),
		),
		Top: key.NewBinding(
			// Буква редактора и клавиша начала терминала сразу. Та же буква
			// g означает «обновить» в колонке джоб (Refresh выше) — не спор:
			// просмотрщик лога открыт оверлеем и забирает клавиши целиком, а
			// колонка джоб в этот момент клавиш не видит вовсе.
			key.WithKeys("g", "home"),
			key.WithHelp("g/G", "края"),
		),
		Bottom: key.NewBinding(
			key.WithKeys("G", "end"),
		),
		Search: key.NewBinding(
			// Та же клавиша, что и у Filter выше, по той же причине, что
			// названа там: в один момент времени применимо только одно из
			// двух толкований.
			key.WithKeys("/"),
			key.WithHelp("/", "поиск"),
		),
		NextMatch: key.NewBinding(
			key.WithKeys("n"),
			key.WithHelp("n/N", "совпадения"),
		),
		PrevMatch: key.NewBinding(
			key.WithKeys("N"),
		),
		Failed: key.NewBinding(
			key.WithKeys("f"),
			key.WithHelp("f", "падение"),
		),
		Log: key.NewBinding(
			key.WithKeys("L"),
			key.WithHelp("L", "лог"),
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
		// Просмотрщик лога (Фаза 15, план 15-01): девятая группа, а не
		// вставка в одну из существующих — привязки применимы только пока
		// он открыт, и смешивать их с законом ленты или с петлёй фикса было
		// бы враньём про то, когда клавиша вообще что-то делает.
		{k.PageUp, k.PageDown, k.Top, k.Bottom, k.Search, k.NextMatch, k.PrevMatch, k.Failed, k.Log},
	}
}

// KeyHint — одна запись строки клавиш внизу экрана: клавиша и её описание
// (Фаза 15, план 15-01, JOB-04). Обе строки уже готовы к показу — источник
// решает, откуда они взялись (привязка раскладки, своя формулировка), а
// KeyBarOf ниже только укладывает готовые записи в ширину.
type KeyHint struct {
	Key  string
	Desc string
}

// Hint собирает запись из привязки b — и клавиша, и описание читаются из
// раскладки, а не пишутся литералом.
func Hint(b key.Binding) KeyHint {
	h := b.Help()
	return KeyHint{Key: h.Key, Desc: h.Desc}
}

// HintAs — та же клавиша b с другим описанием desc: клавиша по-прежнему
// читается из раскладки, а описание зависит от состояния колонки (что
// откроется вправо, какой вид следующий) — подпись привязки статична, а
// строка клавиш обязана называть то, что произойдёт ЗДЕСЬ И СЕЙЧАС
// (idea-0.3.1 §6, мокап с подписью следующего вида правой колонки).
func HintAs(b key.Binding, desc string) KeyHint {
	return KeyHint{Key: b.Help().Key, Desc: desc}
}

// NavHints — хвост закона ленты для строки клавиш колонки: движение по
// колонке, вправо, влево, esc — короткая форма помощи раскладки (k.ShortHelp)
// БЕЗ двух последних записей (помощь и выход), про которые заботится
// MetaHints ниже. ShortHelp всегда возвращает эти шесть привязок в этом же
// порядке (см. её объявление выше) — второй перечень навигации в проекте не
// заводится, строка клавиш каждой колонки берёт закон отсюда же. Привязки
// без отображаемой подписи (например, Down) пропускаются, чтобы пара ↑↓ не
// задваивалась.
func NavHints(k KeyMap) []KeyHint {
	bindings := k.ShortHelp()
	n := len(bindings) - 2
	if n < 0 {
		n = 0
	}
	var hints []KeyHint
	for _, b := range bindings[:n] {
		h := Hint(b)
		if h.Key == "" && h.Desc == "" {
			continue
		}
		hints = append(hints, h)
	}
	return hints
}

// MetaHints — помощь и выход: ровно эти две привязки, и больше нигде в
// строке клавиш они не называются — при нехватке ширины KeyBar уступает
// сначала законом (NavHints), затем собственными записями колонки, но
// никогда не этой парой (см. KeyBar ниже).
func MetaHints(k KeyMap) []KeyHint {
	return []KeyHint{Hint(k.Help), Hint(k.Quit)}
}

// keyBarAvail — ширина, в которую обязана уложиться строка клавиш: пол
// экрана (MinWidth) за вычетом отступов от обоих краёв (OuterMargin), а не
// текущее окно терминала — иначе перечень клавиш менялся бы от ширины
// терминала, и человек учил бы разные строки на разных машинах.
const keyBarAvail = MinWidth - 2*OuterMargin

// hintsWidth — ширина перечня записей в ячейках графемной мерой проекта:
// клавиша, пробел, описание, разделитель " · " (3 ячейки) между записями.
func hintsWidth(hints []KeyHint) int {
	w := 0
	for i, h := range hints {
		if i > 0 {
			w += 3
		}
		w += textwidth.Of(h.Key) + 1 + textwidth.Of(h.Desc)
	}
	return w
}

// KeyBarOf — ЕДИНСТВЕННОЕ место отрисовки строки клавиш в проекте: клавиша
// жирным акцентным (Theme.KeyCap), описание обычным приглушённым
// (Theme.KeyDesc), разделитель " · " приглушённый. Перечень укладывается в
// ширину пола экрана (keyBarAvail выше — MinWidth и OuterMargin, не число),
// графемной мерой проекта (textwidth.Of). Правила укладки:
//   - запись, которая целиком не помещается, не показывается вовсе —
//     полклавиши хуже, чем её отсутствие;
//   - записи с уже названной клавишей отбрасываются: если клавиша
//     встречается в перечне дважды, остаётся ПЕРВАЯ — колонка знает про
//     свою клавишу больше, чем общий хвост закона, и «→ секреты» не должно
//     соседствовать с «→ глубже» в одной строке;
//   - ширина берётся от ПОЛА, а не от текущего окна.
func KeyBarOf(t Theme, hints []KeyHint) string {
	// Ширина пола (MinWidth) за вычетом отступов от обоих краёв
	// (OuterMargin) — та же формула, что и в keyBarAvail выше, повторена
	// здесь буквально: KeyBarOf обязана мерить ширину от пола сама, не
	// полагаясь на то, что вызывающий её код когда-либо это проверит.
	avail := MinWidth - 2*OuterMargin
	seen := map[string]bool{}
	width := 0
	parts := make([]string, 0, len(hints))
	for _, h := range hints {
		if h.Key == "" && h.Desc == "" {
			continue
		}
		if seen[h.Key] {
			continue
		}
		w := textwidth.Of(h.Key) + 1 + textwidth.Of(h.Desc)
		sep := 0
		if len(parts) > 0 {
			sep = 3
		}
		if width+sep+w > avail {
			continue
		}
		seen[h.Key] = true
		width += sep + w
		parts = append(parts, t.KeyCap.Render(h.Key)+" "+t.KeyDesc.Render(h.Desc))
	}
	return strings.Join(parts, t.KeyDesc.Render(" · "))
}

// combineHints склеивает несколько перечней записей в один — порядок групп
// сохраняется, второй склейки срезов вручную в KeyBar ниже не заводится.
func combineHints(groups ...[]KeyHint) []KeyHint {
	var out []KeyHint
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// KeyBar собирает строку клавиш колонки: собственные записи колонки extra,
// затем хвост закона (NavHints), затем помощь и выход (MetaHints) — тем же
// порядком, каким они перечислены здесь. Сигнатура остаётся совместимой с
// прежними вызовами без своих клавиш (extra пуст) — второй функции сборки
// строки клавиш не появляется (Фаза 12, POL-03).
//
// Если собранное не укладывается в ширину (keyBarAvail), записи
// отбрасываются в строго названном порядке приоритета: сначала — записи
// закона ленты (nav) С КОНЦА (их называет ещё и строка подсказки, и первая
// строка раздела клавиш экрана помощи — план 13-03), затем — собственные
// записи колонки (extra) с конца (последнее средство), и никогда — помощь и
// выход (meta): человек, потерявший их, теряет вход и в перечень остальных
// клавиш, и в выход из утилиты.
func KeyBar(t Theme, k KeyMap, extra ...KeyHint) string {
	nav := NavHints(k)
	meta := MetaHints(k)
	for {
		combined := combineHints(extra, nav, meta)
		if hintsWidth(combined) <= keyBarAvail || (len(nav) == 0 && len(extra) == 0) {
			return KeyBarOf(t, combined)
		}
		if len(nav) > 0 {
			nav = nav[:len(nav)-1]
			continue
		}
		extra = extra[:len(extra)-1]
	}
}
