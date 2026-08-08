// Package ui рисует консольный интерфейс ci-shell и слушает клавиши. Пакет
// ничего не печатает в стандартные потоки и не содержит доменной логики —
// воспроизведение упавшей джобы остаётся в internal/runner, internal/repo и
// internal/env, интерфейс только даёт им лицо (09-UI-SPEC.md).
package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// Сетка в целых ячейках терминала (09-UI-SPEC.md, «Сетка и раскладка»).
// Отступов в px не существует — вся геометрия здесь в ячейках.
const (
	// OuterMargin — отступ от левого/правого края экрана до первой рамки.
	OuterMargin = 1
	// PanelGap — горизонтальный зазор между соседними панелями.
	PanelGap = 2
	// Gutter — отступ между рамкой панели и содержимым внутри неё.
	Gutter = 1
	// RowIconGap — зазор между символом статуса и текстом строки списка.
	RowIconGap = 1
	// TreeIndent — отступ на уровень вложенности дерева групп и проектов.
	// Объявлена здесь, но в этой фазе не задействована: её потребитель —
	// дерево групп Фазы 10, и заводить вторую сетку там было бы ошибкой.
	TreeIndent = 2
)

// Жёсткий пол экрана и минимумы панелей экрана джобы (09-UI-SPEC.md,
// «Минимальные размеры экрана и раскладка по ширине»). Лишняя ширина при
// росте окна уходит в правую панель (окружение) — значения переменных
// длиннее имён ключей.
const (
	// MinWidth — жёсткий пол экрана по ширине.
	MinWidth = 80
	// MinHeight — жёсткий пол экрана по высоте.
	MinHeight = 24

	// StepsPanelMin — минимальная ширина левой панели экрана джобы (шаги).
	StepsPanelMin = 24
	// EnvPanelMin — минимальная ширина правой панели экрана джобы (окружение).
	EnvPanelMin = 30
	// StepsSharePercent — доля левой панели экрана джобы от доступной
	// ширины.
	StepsSharePercent = 40
)

// Замкнутый набор символов состояния (09-UI-SPEC.md, «Символы состояния»).
// Все девять шириной ровно в одну ячейку — эмодзи ломают выравнивание в
// половине терминалов (REQUIREMENTS.md → Out of Scope) и в этот набор не
// допускаются. Символ предупреждения из internal/render/banner.go (⚠) сюда
// не переезжает — вместо него в предупреждающем стиле используется
// текстовое «!».
const (
	GlyphOK     = "✓"
	GlyphFail   = "✗"
	GlyphActive = "●"
	GlyphIdle   = "○"
	GlyphHint   = "▸"
	GlyphCursor = "◀"
	GlyphUp     = "↑"
	GlyphDown   = "↓"
	GlyphEnter  = "⏎"
)

// ManualNote — подпись рядом со статусом ручного запуска, названная
// контрактом.
const ManualNote = "вручную"

// Theme — стили по ролям, ровно по таблице «Цвет» контракта.
type Theme struct {
	// Text — обычный текст: собственного цвета не задаёт, наследуется от
	// терминала.
	Text lipgloss.Style
	// Muted — приглушённые метаданные (время, `<скрыто>`, заголовки панелей).
	Muted lipgloss.Style
	// Accent — курсор/выбор, буква клавиши в подсказке, текущий пайплайн.
	Accent lipgloss.Style
	// Danger — единственное законное место для красного: реально упавшее.
	Danger lipgloss.Style
	// Warning — баннер допущений, предупреждения (не ошибки).
	Warning lipgloss.Style
	// Border — границы панелей Lip Gloss.
	Border lipgloss.Style
	// Selected — текущая строка списка: инверсия видео, а не цвет.
	Selected lipgloss.Style
	// ScreenTitle — заголовок экрана (bold, Sentence case).
	ScreenTitle lipgloss.Style
	// PanelTitle — заголовок панели/колонки (bold + muted, ВЕРХНИЙ РЕГИСТР
	// в вызывающем коде).
	PanelTitle lipgloss.Style
	// KeyCap — клавиша в строке клавиш (bold + accent).
	KeyCap lipgloss.Style
	// KeyDesc — описание клавиши (normal + muted).
	KeyDesc lipgloss.Style
	// HintText — текст строки подсказки (bold).
	HintText lipgloss.Style
}

// NewTheme собирает Theme для тёмного (dark=true) или светлого фона
// терминала.
//
// Lip Gloss v2 заменил тип AdaptiveColor v1 функцией-селектором LightDark:
// контракт «каждая роль задаётся парой значений для светлого и тёмного
// фона, деградацию профиля цвета библиотека берёт на себя» соблюдается
// буквально — меняется только механизм выбора (вызов вместо поля
// структуры), не дизайнерское решение.
func NewTheme(dark bool) Theme {
	pick := lipgloss.LightDark(dark)

	accent := pick(lipgloss.Color("#0087AF"), lipgloss.Color("#5FD7FF"))
	muted := pick(lipgloss.Color("#6C6C6C"), lipgloss.Color("#808080"))
	danger := pick(lipgloss.Color("#AF0000"), lipgloss.Color("#FF5F5F"))
	warning := pick(lipgloss.Color("#875F00"), lipgloss.Color("#D7AF00"))
	border := pick(lipgloss.Color("#BCBCBC"), lipgloss.Color("#444444"))

	return Theme{
		Text:  lipgloss.NewStyle(),
		Muted: lipgloss.NewStyle().Foreground(muted).Faint(true),
		// Роль «успех» собственного цвета не имеет намеренно: GlyphOK
		// рисуется обычным текстовым стилем (Text) — осознанное отличие от
		// типичных TUI, где успех зелёный (09-UI-SPEC.md, правило 3
		// палитры).
		Accent:  lipgloss.NewStyle().Foreground(accent),
		Danger:  lipgloss.NewStyle().Bold(true).Foreground(danger),
		Warning: lipgloss.NewStyle().Underline(true).Foreground(warning),
		Border:  lipgloss.NewStyle().Foreground(border),
		// Selected — инверсия видео, не конкретный цвет: инверсия
		// использует собственные цвета терминала и потому читается на
		// любом фоне без нашего выбора конкретного hex.
		Selected:    lipgloss.NewStyle().Reverse(true),
		ScreenTitle: lipgloss.NewStyle().Bold(true),
		PanelTitle:  lipgloss.NewStyle().Bold(true).Foreground(muted).Faint(true),
		KeyCap:      lipgloss.NewStyle().Bold(true).Foreground(accent),
		KeyDesc:     lipgloss.NewStyle().Foreground(muted).Faint(true),
		HintText:    lipgloss.NewStyle().Bold(true),
	}
	// Начертание «курсив» не применяется ни к одной роли выше — контракт
	// прямо запрещает его как непредсказуемое в Windows-эмуляторах терминала
	// (09-UI-SPEC.md, «Типографика»).
}

// StatusGlyph возвращает символ статуса status ровно по таблице «Символы
// состояния» контракта. running возвращает пустую строку — его место
// занимает спиннер, единственное анимированное состояние; отрисовку кадра
// делает вызывающий код экрана.
func StatusGlyph(status string) string {
	switch status {
	case "created", "pending", "manual", "skipped":
		return GlyphIdle
	case "running":
		return ""
	case "success":
		return GlyphOK
	case "failed":
		return GlyphFail
	case "canceled":
		return GlyphFail
	default:
		return GlyphIdle
	}
}

// StatusStyle возвращает стиль символа статуса status. Правило
// доступности: символ первичен, цвет — усиление, состояние читается без
// цвета.
func (t Theme) StatusStyle(status string) lipgloss.Style {
	switch status {
	case "created", "pending", "manual":
		return t.Muted
	case "running":
		return t.Accent
	case "success":
		return t.Text
	case "failed":
		return t.Danger
	case "canceled":
		// Отменено человеком, а не поломка: приглушённый стиль, НЕ danger.
		return t.Muted
	case "skipped":
		// Самый тусклый вариант — тот же приглушённый стиль контракта.
		return t.Muted
	default:
		return t.Muted
	}
}

// Truncate обрезает s до заданной ширины width в ячейках терминала,
// добавляя многоточие «…» при обрезке. Ширина считается функцией измерения
// Lip Gloss (lipgloss.Width) на каждом шаге сборки результата — не длиной
// байтов и не количеством рун: кириллица и «…», посчитанные не в
// графемах, уже один раз сломали `%-8s` в internal/render (Фаза 1), и
// здесь тот же класс дефекта недопустим (09-UI-SPEC.md, «Правило измерения
// ширины»). Приём обрезки по накоплению переносит идею
// render.Progress.truncate — только измеритель здесь Lip Gloss, а не
// []rune.
func Truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	var b strings.Builder
	for _, r := range s {
		candidate := b.String() + string(r)
		if lipgloss.Width(candidate)+1 > width {
			break
		}
		b.WriteRune(r)
	}
	return b.String() + "…"
}

// Fit приводит строку s ровно к ширине width: сначала Truncate, затем
// дополнение до нужной ширины стилем Lip Gloss. Ручной
// fmt.Sprintf("%-Ns", ...) на пользовательских строках (имена джоб, ветки,
// сообщения) в этом пакете запрещён — на кириллице он считает байты, а не
// ячейки; выравнивание только через Lip Gloss.
func Fit(s string, width int) string {
	t := Truncate(s, width)
	return lipgloss.NewStyle().Width(width).Render(t)
}

// TooSmall возвращает единственную строку, которая рисуется вместо
// панелей, когда окно меньше пола 80×24 — отцентрированную по доступной
// ширине средствами Lip Gloss.
func TooSmall(width, height int) string {
	msg := fmt.Sprintf("терминал слишком мал: нужно минимум 80×24, сейчас %d×%d — растяните окно", width, height)
	if width <= 0 {
		return msg
	}
	return lipgloss.NewStyle().Width(width).Align(lipgloss.Center).Render(msg)
}

// Ago форматирует относительное время t для метаданных списков и шапок.
// Живёт здесь, потому что её зовут и список джоб, и шапка экрана джобы, а
// вторая копия разъехалась бы с первой.
func Ago(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d сек назад", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d мин назад", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d ч назад", int(d.Hours()))
	default:
		return fmt.Sprintf("%d дн назад", int(d.Hours()/24))
	}
}
