// color.go — возможности терминала и единственная точка применения цвета
// (Фаза 12, POL-01). Источники монохрома по 09-UI-SPEC.md: переменная
// запрета цвета окружения (см. term.ColorDisabled — единственное место, где
// она названа), немой терминал (TERM пуст или "dumb"), отсутствие профиля
// цвета у самого терминала. Правило доступности оттуда же: символ первичен,
// цвет — усиление, а не замена — ни одна роль темы не выражается цветом в
// одиночку, у каждой есть символ или начертание, различимые и без цвета
// (internal/ui/theme.go, StatusStyle).
//
// DetectCaps — не то же самое решение, что term.UIEnabled: та отвечает
// «рисовать ли интерфейс вообще» и остаётся единственной точкой запуска
// (internal/term/term.go), эта — «каким цветом», и обе живут раздельно,
// потому что джоба может воспроизводиться и без интерфейса вовсе (пайп,
// CI), а цвет тогда решать уже не для кого.
package ui

import (
	"charm.land/lipgloss/v2"

	"github.com/f3rym/ci-shell/internal/term"
)

// Profile — профиль цвета терминала. Понижение с полного цвета до
// шестнадцати оттенков делает сама библиотека отрисовки; здесь важна только
// граница «цвет есть» / «цвета нет», потому что именно на ней интерфейс
// обязан деградировать, а не сломаться.
type Profile int

const (
	ProfileTrueColor Profile = iota
	ProfileANSI256
	ProfileANSI
	ProfileMono
)

// Caps — возможности терминала, с которыми собирается тема (NewTheme,
// internal/ui/theme.go).
type Caps struct {
	Profile Profile
	// Dark — фон тёмный.
	Dark bool
}

// Colored сообщает, применяется ли цвет вообще: профиль не монохромный.
func (c Caps) Colored() bool {
	return c.Profile != ProfileMono
}

// DetectCaps определяет возможности терминала — вызывается один раз при
// запуске (internal/ui/app.go, Run). Если решение о выключенном цвете
// (term.ColorDisabled) истинно — профиль монохромный, и профиль у
// библиотеки отрисовки не спрашивается вовсе: запрет цвета из окружения —
// это соглашение, которое утилита обязана уважать безусловно, а не
// предложение библиотеке (POL-01).
//
// Иначе профиль берётся у библиотеки отрисовки, а фон по умолчанию
// считается тёмным: это самое частое умолчание, и его уточняет сообщение
// Bubble Tea о цвете фона терминала сразу после старта (internal/ui/app.go,
// Update).
func DetectCaps() Caps {
	if term.ColorDisabled() {
		return Caps{Profile: ProfileMono, Dark: true}
	}
	switch lipgloss.ColorProfile() {
	case lipgloss.TrueColor:
		return Caps{Profile: ProfileTrueColor, Dark: true}
	case lipgloss.ANSI256:
		return Caps{Profile: ProfileANSI256, Dark: true}
	case lipgloss.ANSI:
		return Caps{Profile: ProfileANSI, Dark: true}
	default:
		return Caps{Profile: ProfileMono, Dark: true}
	}
}

// style — единственная функция проекта, применяющая цвет к стилю (T-12-05,
// T-12-06): монохром выключает цвет разом для всех ролей темы, потому что
// забыть роль здесь невозможно — вызвать конструктор цвета в обход этой
// функции значило бы завести вторую точку решения.
//
// При монохромном профиле возвращается стиль без цветовой составляющей
// вовсе — не серый, не приглушённый, а именно без Foreground: начертание
// (жирный/тусклый/подчёркнутый — internal/ui/theme.go) остаётся
// единственным различием ролей, и различимость статусов без цвета
// (T-12-06) не ломается. Иначе вариант выбирается по фону селектором
// светлого и тёмного Lip Gloss v2 и ставится цветом переднего плана.
func (c Caps) style(light, dark string) lipgloss.Style {
	if c.Profile == ProfileMono {
		return lipgloss.NewStyle()
	}
	pick := lipgloss.LightDark(c.Dark)
	return lipgloss.NewStyle().Foreground(pick(lipgloss.Color(light), lipgloss.Color(dark)))
}
