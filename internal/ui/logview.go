// logview.go — просмотрщик лога (Фаза 15, LOG-01…LOG-04): окно в срез строк,
// и больше ничего. Просмотрщик не ходит в сеть, не знает, откуда взялись
// строки, не маскирует значения и не завершает программу — строки приходят
// уже замаскированными единственным маскировщиком проекта (internal/ui/mask.go),
// а решение о том, что показать (хвост или лог целиком, где упавший шаг),
// принимает вызывающий код.
//
// Потребителей два, и это принципиально: панель лога настоящего прогона
// (internal/ui/joblog.go) и панель лога локального воспроизведения
// (internal/ui/job.go) — второй прокрутки в проекте не заводится, ровно
// поэтому просмотрщик отдельным файлом, а не полем внутри одной из панелей.
package ui

import (
	"fmt"
	"strings"
)

// logView — состояние просмотрщика лога. Поля поиска (задача 2) здесь не
// объявляются — они присоединяются отдельной правкой, чтобы задача 2
// читалась как одно связное изменение.
type logView struct {
	// lines — уже готовые к показу строки: приходят замаскированными и
	// прочищенными от вызывающего кода, просмотрщик их не трогает.
	lines []string
	// offset — номер первой видимой строки.
	offset int
	// width, height — размер окна в ячейках и строках.
	width, height int
	// tail — показан хвост лога, а не лог целиком.
	tail bool
	// pull — подпись команды, которой можно дотянуть лог целиком; пусто,
	// когда тянуть неоткуда (лог и так целиком).
	pull string
	// mark — строка упавшего шага; отрицательное значение — метки нет.
	mark int
	// title — заголовок для полноэкранного вида просмотрщика (задача 3).
	title string
}

// newLogView — пустой просмотрщик: метка отсутствует, смещение нулевое.
func newLogView() logView {
	return logView{mark: -1}
}

// setTitle задаёт заголовок полноэкранного вида.
func (v logView) setTitle(title string) logView {
	v.title = title
	return v
}

// setSize задаёт размеры окна: панель даёт свои, оверлей — весь кадр тела.
// Прижимает смещение к новым границам, чтобы уменьшение окна не оставляло
// просмотрщик за концом лога.
func (v logView) setSize(width, height int) logView {
	v.width = width
	v.height = height
	v.offset = v.clamp(v.offset)
	return v
}

// clamp прижимает произвольное смещение off к границам, в которых окно
// высотой v.height показывает существующие строки, не заезжая за конец
// среза. Единственное место этой арифметики — все переходы (scroll, page,
// top, bottom, setSize) и расчёт видимого окна (window ниже) зовут её, а не
// считают границы заново.
func (v logView) clamp(off int) int {
	if len(v.lines) == 0 {
		return 0
	}
	if off < 0 {
		off = 0
	}
	max := len(v.lines) - v.height
	if max < 0 {
		max = 0
	}
	if off > max {
		off = max
	}
	return off
}

// setLines кладёт новое содержимое лога. Правило прилипания к концу: если ДО
// замены человек стоял в самом низу (atBottom), после замены он остаётся в
// самом низу — иначе живой лог локального прогона уезжал бы из-под глаз на
// каждой новой строке; если он ушёл вверх читать, смещение сохраняется и
// прижимается к новым границам — иначе чтение было бы невозможно, пока шаг
// пишет вывод.
func (v logView) setLines(lines []string) logView {
	wasBottom := v.atBottom()
	v.lines = lines
	if wasBottom {
		return v.bottom()
	}
	v.offset = v.clamp(v.offset)
	return v
}

// setSource задаёт источник: показан ли хвост, где упавший шаг (mark) и чем
// дотянуть лог целиком (pull). Отдельно от setLines, потому что строки
// меняются на каждом обновлении, а источник — только при смене джобы или при
// переходе с хвоста на полный лог.
func (v logView) setSource(tail bool, mark int, pull string) logView {
	v.tail = tail
	v.mark = mark
	v.pull = pull
	return v
}

// empty сообщает, пуст ли лог целиком.
func (v logView) empty() bool {
	return len(v.lines) == 0
}

// atBottom — последняя строка лога видна в окне.
func (v logView) atBottom() bool {
	if v.empty() {
		return true
	}
	first, count := v.window()
	return first+count >= len(v.lines)
}

// atTop — первая строка лога видна в окне.
func (v logView) atTop() bool {
	first, _ := v.window()
	return first == 0
}

// window — единственное место расчёта видимого куска лога: first — смещение,
// прижатое к границам, count — сколько строк помещается в высоту, но не
// больше остатка лога. Отрисовка, строка положения и переходы спрашивают
// его, а не считают заново.
func (v logView) window() (first, count int) {
	if v.empty() {
		return 0, 0
	}
	first = v.clamp(v.offset)
	count = v.height
	if count < 0 {
		count = 0
	}
	remaining := len(v.lines) - first
	if count > remaining {
		count = remaining
	}
	return first, count
}

// scroll двигает окно на delta строк вверх (отрицательное значение) или вниз,
// с прижатием к границам.
func (v logView) scroll(delta int) logView {
	v.offset = v.clamp(v.offset + delta)
	return v
}

// page двигает окно на delta экранов — высота окна минус одна строка
// перекрытия, чтобы взгляд не терял место склейки между двумя страницами.
func (v logView) page(delta int) logView {
	step := v.height - 1
	if step < 1 {
		step = 1
	}
	return v.scroll(delta * step)
}

// top ставит окно в самое начало лога.
func (v logView) top() logView {
	v.offset = 0
	return v
}

// bottom ставит окно в самый конец лога — последняя строка становится
// последней видимой.
func (v logView) bottom() logView {
	v.offset = v.clamp(len(v.lines))
	return v
}

// view рисует окно строк: берётся window, каждая строка обрезается по
// ширине графемной мерой темы (Truncate), пустой лог заменяется одной
// честной строкой приглушённым стилем. Ни одного ручного выравнивания
// форматным глаголом. Подсветку совпадений в эту же функцию добавляет
// задача 2.
func (v logView) view(t Theme) string {
	first, count := v.window()
	if count == 0 {
		return t.Muted.Render("лог пуст — здесь появится вывод шага")
	}
	var b strings.Builder
	for i := 0; i < count; i++ {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(Truncate(v.lines[first+i], v.width))
	}
	return b.String()
}

// statusLine — строка положения, ради которой LOG-04 и заведён: «строки
// {первая}–{последняя} из {всего}», а при показанном хвосте — плюс «показан
// хвост, {pull} вытянет лог целиком» (подпись команды берётся из поля pull и
// очищается Plain, потому что она попадает в кадр мимо меры ширины). Когда
// лог целиком, часть про хвост не печатается вовсе — врать про хвост,
// которого нет, хуже, чем молчать. Нумерация строк человеческая, с единицы:
// индекс в срезе — деталь реализации, и показывать его человеку значит
// просить его считать за нас.
func (v logView) statusLine(t Theme) string {
	if v.empty() {
		return t.Muted.Render("строк 0 из 0")
	}
	first, count := v.window()
	text := fmt.Sprintf("строки %d–%d из %d", first+1, first+count, len(v.lines))
	if v.tail {
		text += fmt.Sprintf(" — показан хвост, %s вытянет лог целиком", Plain(v.pull))
	}
	return t.Muted.Render(text)
}

// hintText — строка подсказки просмотрщика. Наполняется задачей 2
// (состояния поиска); здесь — формулировка обычного просмотра.
func (v logView) hintText() string {
	return "обычный просмотр лога"
}
