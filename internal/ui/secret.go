// secret.go — вид «секреты» правой колонки экрана джобы (Фаза 15, план
// 15-02, idea-0.3.1 §6): отдельный файл, потому что у него своя строка
// (secretRow), своя сборка из окружения сессии (secretRowsFromSession) и
// (задача 3) своё поле ввода значения — модель экрана джобы и без того
// самая большая в пакете.
package ui

import (
	"sort"
	"strings"

	"github.com/f3rym/ci-shell/internal/render"
)

// secretRow — одна строка вида «секреты»: имя переменной, источник (путь
// проекта или группы, приславшей её маскированной) и уже принятое решение о
// показе (для заполненной переменной — то, что вернула render.DisplayValue).
// Поля под сырое значение у типа НЕТ — тот же приём, что уже применён к
// типу недостающей переменной домена (env.Missing): тип, в котором значению
// негде поместиться, не может его случайно вынести наружу. Заполненность —
// отдельное булево поле.
type secretRow struct {
	Key     string
	Origin  string
	Display string
	Filled  bool
}

// secretRowsFromSession собирает строки вида «секреты» из окружения сессии:
// сначала незаполненные (перечень недостающих переменных, env.Missing),
// потом заполненные (переменные с признаком секрета — отображение считает
// та же функция, что и для панели окружения, второй ветки показа не
// появляется), внутри каждой группы по имени. Строка, требующая действия,
// не должна прятаться в середине алфавитного списка — именно ради неё вид
// и открывают.
func secretRowsFromSession(s *Session) []secretRow {
	missing := make(map[string]bool, len(s.environment.Missing))
	rows := make([]secretRow, 0, len(s.environment.Missing))
	for _, m := range s.environment.Missing {
		rows = append(rows, secretRow{Key: m.Key, Origin: m.Origin, Filled: false})
		missing[m.Key] = true
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })

	var filled []secretRow
	for _, v := range s.environment.Vars {
		if !v.Secret || missing[v.Key] {
			continue
		}
		filled = append(filled, secretRow{Key: v.Key, Origin: v.Origin, Display: render.DisplayValue(v), Filled: true})
	}
	sort.Slice(filled, func(i, j int) bool { return filled[i].Key < filled[j].Key })

	return append(rows, filled...)
}

// line отрисовывает одну строку вида «секреты»: имя, затем либо отображение
// приглушённым стилем, либо отметка «не задано» текстовым восклицательным
// знаком в предупреждающем стиле — контракт запрещает символ (⚠) вне
// замкнутого набора, «!» остаётся обычным текстом. Обрезка идёт ДО
// раскраски (тот же приём, что и подсветка совпадений просмотрщика лога,
// internal/ui/logview.go, highlight): управляющие последовательности стиля
// разъехали бы графемную меру ширины. Выбранная строка получает символ
// курсора справа и инверсию на всю строку — ровно как строки шагов и джоб.
// Ручного выравнивания форматным глаголом нет.
func (r secretRow) line(t Theme, width int, selected bool) string {
	statusText := "не задано !"
	style := t.Warning
	if r.Filled {
		statusText = r.Display
		style = t.Muted
	}
	trimmed := Truncate(r.Key+" "+statusText, width)
	name, status, found := strings.Cut(trimmed, " ")
	line := name
	if found {
		line += " " + style.Render(status)
	}
	if selected {
		return t.Selected.Render(line + " " + GlyphCursor)
	}
	return line
}
