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

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
)

// logView — состояние просмотрщика лога. Поля поиска добавлены задачей 2.
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

	// query — что ищем; пусто — поиска нет. Эхоится обычным образом, в
	// отличие от поля токена и поля значения секрета: запрос — не секрет, а
	// вот совпадение в логе им быть может, и именно поэтому подсветка ниже
	// не меняет текста строки, а только её начертание.
	query string
	// matches — номера строк с совпадением, по возрастанию.
	matches []int
	// match — индекс текущего совпадения в matches.
	match int
	// input — поле ввода запроса поиска.
	input textinput.Model
	// searching — поле ввода запроса открыто.
	searching bool
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
// форматным глаголом.
//
// Подсветка совпадений накладывается ПОСЛЕ обрезки по ширине, одним
// выражением: highlight принимает уже обрезанную строку. Если бы порядок
// был обратным, управляющие последовательности стиля попали бы в меру
// ширины и разъехали бы кадр — тот же класс дефекта, что байтовое
// выравнивание кириллицы в v0.1.0.
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
		lineIdx := first + i
		current := v.query != "" && len(v.matches) > 0 && v.matches[v.match] == lineIdx
		b.WriteString(highlight(Truncate(v.lines[lineIdx], v.width), v.query, t, current))
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
	if v.query != "" {
		// Запрос приходит с клавиатуры (набран или вставлен), но попадает в
		// кадр мимо меры ширины — очистка Plain здесь так же обязательна,
		// как у подписи дотягивания выше.
		if len(v.matches) == 0 {
			text += fmt.Sprintf(" — поиск «%s»: совпадений нет", Plain(v.query))
		} else {
			text += fmt.Sprintf(" — поиск «%s»: %d из %d", Plain(v.query), v.match+1, len(v.matches))
		}
	}
	return t.Muted.Render(text)
}

// hintText — строка подсказки просмотрщика: четыре состояния ровно четырьмя
// именованными формулировками (internal/ui/hint.go) — поле поиска открыто;
// поиск задан и совпадения есть; поиск задан и совпадений нет; обычный
// просмотр (в том числе пустой лог). Клавиши в формулировках не пишутся
// литералами — они читаются из раскладки здесь же.
func (v logView) hintText() string {
	keys := DefaultKeys()
	switch {
	case v.searching:
		return HintLogSearching()
	case v.query != "" && len(v.matches) > 0:
		return HintLogFound(len(v.matches), keys.NextMatch.Help().Key)
	case v.query != "" && len(v.matches) == 0:
		return HintLogNothingFound()
	case v.empty():
		return HintLogEmpty()
	default:
		return HintLogViewing(keys.Search.Help().Key, keys.Failed.Help().Key)
	}
}

// openSearch открывает поле ввода запроса: собирает поле, ставит фокус,
// поднимает признак и возвращает команду мигания курсора — тот же приём,
// что у строки команды (internal/ui/command.go, openCommandBar).
func (v logView) openSearch() (logView, tea.Cmd) {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.Focus()
	v.input = ti
	v.searching = true
	return v, textinput.Blink
}

// closeSearch закрывает поле ввода, НЕ трогая запрос и совпадения: человек
// закрыл ввод, а не отменил поиск.
func (v logView) closeSearch() logView {
	v.searching = false
	return v
}

// clearSearch снимает поиск целиком: запрос, совпадения и поле ввода.
func (v logView) clearSearch() logView {
	v.query = ""
	v.matches = nil
	v.match = 0
	v.input = textinput.Model{}
	v.searching = false
	return v
}

// applyQuery — единственное место, где считаются совпадения: построчно, а
// не по всему тексту разом — лог это строки, и «совпадение на границе
// строк» человеку показать всё равно нечем. По каждой строке лога решается,
// содержит ли она query без учёта регистра (обе стороны приводятся к
// нижнему регистру), номера подходящих строк складываются по возрастанию.
// Текущим становится первое совпадение НЕ ВЫШЕ текущего смещения, а если
// такого нет — первое вообще (нулевой индекс, к которому match уже
// прижат по умолчанию). Пустой запрос очищает совпадения.
func (v logView) applyQuery(query string) logView {
	v.query = query
	v.matches = nil
	if query != "" {
		q := strings.ToLower(query)
		for i, line := range v.lines {
			if strings.Contains(strings.ToLower(line), q) {
				v.matches = append(v.matches, i)
			}
		}
	}
	first, _ := v.window()
	v.match = 0
	for i, m := range v.matches {
		if m >= first {
			v.match = i
			break
		}
	}
	return v
}

// reveal прижимает смещение так, чтобы текущее совпадение попало в окно:
// если оно уже видно, смещение не трогается — иначе кадр прыгал бы на
// каждом переходе внутри одного экрана.
func (v logView) reveal() logView {
	if len(v.matches) == 0 {
		return v
	}
	line := v.matches[v.match]
	first, count := v.window()
	if line >= first && line < first+count {
		return v
	}
	v.offset = v.clamp(line)
	return v
}

// nextMatch / prevMatch — переход по кольцу совпадений.
func (v logView) nextMatch() logView {
	if len(v.matches) == 0 {
		return v
	}
	v.match = (v.match + 1) % len(v.matches)
	return v.reveal()
}

func (v logView) prevMatch() logView {
	if len(v.matches) == 0 {
		return v
	}
	v.match = (v.match - 1 + len(v.matches)) % len(v.matches)
	return v.reveal()
}

// toFailed — LOG-03, единственная точка перехода к упавшему шагу: если
// метка есть (неотрицательна), смещение ставится так, чтобы строка метки
// стала первой видимой; если метки нет — просмотрщик уходит в конец.
//
// Две ветви — не два поведения, а одно правило на двух источниках: в логе
// настоящего прогона метка приходит от провайдера (номер строки последней
// открытой секции, provider.Log.SectionLine); в логе локального
// воспроизведения меток нет и быть не может — маркеры шагов вырезаются
// раньше, чем строка попадает в буфер (internal/runner/steps.go), зато
// set -e обрывает скрипт ровно на упавшем шаге, поэтому конец локального
// лога И ЕСТЬ упавший шаг.
func (v logView) toFailed() logView {
	if v.mark < 0 {
		return v.bottom()
	}
	v.offset = v.clamp(v.mark)
	return v
}

// update — единственное место разбора клавиш просмотрщика. Пока поле поиска
// открыто, клавиши уходят в него (тот же приём, что у строки команды):
// подтверждение применяет набранное и переходит к текущему совпадению,
// привязка возврата закрывает поле, снимая поиск целиком, остальное —
// обычный ввод текста. Вне поля ввода разбираются: движение, страница, края,
// поиск, переход по совпадениям, переход к упавшему шагу, привязка
// возврата — закрытие просмотрщика (logDismissMsg, задача 3). Все клавиши
// берутся из раскладки k, ни одного строкового литерала клавиши в файле.
func (v logView) update(msg tea.Msg, k KeyMap) (logView, tea.Cmd) {
	if v.searching {
		if paste, ok := msg.(tea.PasteMsg); ok {
			var cmd tea.Cmd
			v.input, cmd = v.input.Update(paste)
			return v, cmd
		}
		keyMsg, ok := msg.(tea.KeyPressMsg)
		if !ok {
			var cmd tea.Cmd
			v.input, cmd = v.input.Update(msg)
			return v, cmd
		}
		switch {
		case key.Matches(keyMsg, k.Open):
			v = v.applyQuery(v.input.Value())
			v = v.closeSearch()
			return v.reveal(), nil
		case key.Matches(keyMsg, k.Back):
			return v.clearSearch(), nil
		}
		var cmd tea.Cmd
		v.input, cmd = v.input.Update(keyMsg)
		return v, cmd
	}

	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return v, nil
	}
	switch {
	case key.Matches(keyMsg, k.Up):
		return v.scroll(-1), nil
	case key.Matches(keyMsg, k.Down):
		return v.scroll(1), nil
	case key.Matches(keyMsg, k.PageUp):
		return v.page(-1), nil
	case key.Matches(keyMsg, k.PageDown):
		return v.page(1), nil
	case key.Matches(keyMsg, k.Top):
		return v.top(), nil
	case key.Matches(keyMsg, k.Bottom):
		return v.bottom(), nil
	case key.Matches(keyMsg, k.Search):
		return v.openSearch()
	case key.Matches(keyMsg, k.NextMatch):
		return v.nextMatch(), nil
	case key.Matches(keyMsg, k.PrevMatch):
		return v.prevMatch(), nil
	case key.Matches(keyMsg, k.Failed):
		return v.toFailed(), nil
	// k.Quit («q») с Фазы 18 (KEYS-02) синоним esc и здесь — оба закрывают
	// просмотрщик; второго способа выйти из утилиты отсюда не появляется
	// (просмотрщик и раньше не завершал программу сам).
	case key.Matches(keyMsg, k.Back), key.Matches(keyMsg, k.Quit):
		return v, func() tea.Msg { return logDismissMsg{} }
	}
	return v, nil
}

// keyHints — перечень записей строки клавиш просмотрщика (тип KeyHint и
// единственная отрисовка KeyBarOf — задача 3): прокрутка, страница, края,
// поиск, переход к упавшему шагу, закрытие. При активном поиске запись
// поиска заменяется записью перехода по совпадениям — иначе строка не
// уложилась бы в пол 80.
//
// Ширина в ячейках (пол 80, доступно 78 за вычетом отступов от краёв),
// шесть записей и пять разделителей " · " (3 ячейки каждый):
//
//	обычный просмотр:
//	  "↑↓ выбор"(8) + "pgup/pgdn экран"(15) + "gG края"(7) + "/ поиск"(7) +
//	  "f падение"(10) + "esc назад"(9) + 5*" · "(15) = 71 <= 78
//	поиск активен (запись поиска заменена переходом по совпадениям):
//	  8 + 15 + 7 + "nN совпадения"(13) + 10 + 9 + 15 = 77 <= 78
func (v logView) keyHints(k KeyMap) []KeyHint {
	hints := []KeyHint{
		Hint(k.Up),
		Hint(k.PageUp),
		Hint(k.Top),
	}
	if v.searching {
		hints = append(hints, Hint(k.NextMatch))
	} else {
		hints = append(hints, Hint(k.Search))
	}
	hints = append(hints, Hint(k.Failed), HintAs(k.Back, "назад"))
	return hints
}

// openLogMsg — открыть полноэкранный просмотрщик (задача 3): title и lines —
// готовые к показу заголовок и строки, tail/mark/pull — источник (setSource
// выше). Сообщение несёт ГОТОВЫЕ строки, а не ссылку на источник, ровно
// потому, что источников два и они несовместимы (память панели лога
// настоящего прогона, internal/ui/joblog.go, и ограниченный буфер сессии
// локального воспроизведения, internal/ui/job.go) — просмотрщик про
// источники не знает вовсе.
type openLogMsg struct {
	title string
	lines []string
	tail  bool
	mark  int
	pull  string
}

// logDismissMsg — человек закрыл полноэкранный просмотрщик (тот же приём,
// что у сообщения возврата экрана проводника, internal/ui/guide.go).
type logDismissMsg struct{}

// highlight подсвечивает в строке s все вхождения query без учёта регистра:
// начертанием, а не заменой текста — строка после подсветки состоит из тех
// же символов, что и до неё. Строка текущего совпадения (current) рисуется
// инверсией, остальные — акцентом.
//
// Поиск позиции идёт по копиям строки и запроса в нижнем регистре; если
// приведение к нижнему регистру изменило длину строки в байтах (такое
// бывает на отдельных письменностях), позиции фрагментов доверять нельзя —
// неверное смещение разрезало бы UTF-8 посередине и выдало бы в кадр мусор
// вместо текста лога, поэтому в этом случае подсвечивается строка целиком,
// без разбора на фрагменты.
func highlight(s, query string, t Theme, current bool) string {
	if query == "" {
		return s
	}

	style := t.Accent
	if current {
		style = t.Selected
	}

	lower := strings.ToLower(s)
	q := strings.ToLower(query)
	if len(lower) != len(s) || len(q) == 0 {
		return style.Render(s)
	}

	var b strings.Builder
	rest, restLower := s, lower
	found := false
	for {
		idx := strings.Index(restLower, q)
		if idx < 0 {
			break
		}
		found = true
		b.WriteString(rest[:idx])
		b.WriteString(style.Render(rest[idx : idx+len(q)]))
		rest = rest[idx+len(q):]
		restLower = restLower[idx+len(q):]
	}
	if !found {
		return s
	}
	b.WriteString(rest)
	return b.String()
}
