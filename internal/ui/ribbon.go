// ribbon.go — лента колонок (Фаза 13, NAV-01…NAV-04): интерфейс — не набор
// экранов, а лента колонок слева направо; всё, что глубже, живёт правее.
// Лента отвечает на четыре вопроса и больше ни на какие: какие колонки
// сейчас есть, какая из них в фокусе, какие видны в окне терминала и какой
// ширины. Лента не рисует, не ходит в сеть, не завершает программу и не
// возвращает ни одной команды Bubble Tea — она значение, а не модель Bubble
// Tea; решения о командах и о выходе принимает корневая модель
// (internal/ui/app.go).
package ui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
)

// columnID — колонка ленты, ровно по цепочке idea-0.3.1 §3: репозитории →
// пайплайны → джобы → шаги → окружение·секреты·лог. Порядок объявления и
// есть порядок ленты — второго перечня порядка в проекте не заводится.
type columnID int

const (
	colRepos columnID = iota
	colPipelines
	colJobs
	colSteps
	colDetail
)

// columnTitle возвращает заголовок колонки id ВЕРХНИМ РЕГИСТРОМ
// (09-UI-SPEC.md, «Типографика», роль «заголовок панели/колонки»). Заголовок
// колонки заменяет путь хлебными крошками сверху — его в интерфейсе не
// появляется (idea-0.3.1 §3, последний абзац).
func columnTitle(id columnID) string {
	switch id {
	case colRepos:
		return "РЕПОЗИТОРИИ"
	case colPipelines:
		return "ПАЙПЛАЙНЫ"
	case colJobs:
		return "ДЖОБЫ"
	case colSteps:
		return "ШАГИ"
	case colDetail:
		return "ОКРУЖЕНИЕ · СЕКРЕТЫ · ЛОГ"
	}
	return ""
}

// screenFor возвращает экран из уже существующего перечня
// (internal/ui/app.go), который рисует колонку id: репозитории и пайплайны —
// экран дерева, джобы — экран списка джоб, шаги и окружение — экран джобы.
// Перечень экранов остаётся ровно тем же (его читает экран помощи), но
// выводится теперь из ленты, а не хранится полем корневой модели.
func screenFor(id columnID) screen {
	switch id {
	case colRepos, colPipelines:
		return screenTree
	case colJobs:
		return screenJobs
	case colSteps, colDetail:
		return screenJob
	}
	return screenSplash
}

// column — одна колонка ленты: какая колонка (id), уточнение заголовка
// (label — имя проекта, номер пайплайна, имя джобы; строка приходит уже
// очищенной от управляющих символов терминала — очистка делается
// вызывающим той же мерой, что и везде в проекте, см. reset/open ниже) и
// запомненная позиция (cursor, key) для плана 13-03 — структура колонки
// одна, а наполняет позицию следующий план.
type column struct {
	id     columnID
	label  string
	cursor int
	key    string
}

// ribbon — лента: перечень колонок, фокус, окно видимых колонок, ширины.
// Инвариант, названный прямо: 0 <= focus < len(cols) при непустой ленте, и
// first <= focus < first+visibleCount() всегда — «колонка с фокусом всегда
// в кадре» (NAV-03) держится этим инвариантом, а не проверкой при
// отрисовке.
type ribbon struct {
	cols  []column
	focus int
	first int

	width, height int
}

// empty сообщает, что в ленте нет ни одной колонки.
func (r ribbon) empty() bool {
	return len(r.cols) == 0
}

// focusedID — колонка в фокусе. На пустой ленте возвращает colRepos —
// значение никогда не читается вызывающим на пустой ленте (проверка
// делается через empty() отдельно), но и падать здесь не должно.
func (r ribbon) focusedID() columnID {
	if r.empty() {
		return colRepos
	}
	return r.cols[r.focus].id
}

// screen — экран колонки в фокусе.
func (r ribbon) screen() screen {
	return screenFor(r.focusedID())
}

// atLeftEdge — фокус стоит на самой левой колонке.
func (r ribbon) atLeftEdge() bool {
	return r.focus == 0
}

// hasDeeper — правее фокуса есть колонка.
func (r ribbon) hasDeeper() bool {
	return r.focus < len(r.cols)-1
}

// has сообщает, есть ли в ленте колонка id.
func (r ribbon) has(id columnID) bool {
	for _, c := range r.cols {
		if c.id == id {
			return true
		}
	}
	return false
}

// reset возвращает ленту из одной колонки id с уточнением заголовка label —
// вход по прямой ссылке на джобу и вход в браузер, где колонок левее нет:
// самая левая колонка становится id, и esc из неё выходит.
func (r ribbon) reset(id columnID, label string) ribbon {
	r.cols = []column{{id: id, label: label}}
	r.focus = 0
	r.first = 0
	return r.reflow()
}

// open кладёт колонку id с уточнением заголовка label правее колонки в
// фокусе, ОТБРАСЫВАЕТ всё, что было правее, переводит фокус на новую и
// возвращает перечень отброшенных колонок. Возврат отброшенного — не
// удобство, а требование уборки: владелец отброшенной колонки обязан
// прибраться за собой, и единственный такой владелец сегодня — сессия
// джобы (контейнер, чекаут, каталог файловых переменных с
// материализованными секретами). Без этого перечня открытие другого
// пайплайна оставляло бы живую сессию висеть до конца процесса.
func (r ribbon) open(id columnID, label string) (ribbon, []columnID) {
	var dropped []columnID
	if !r.empty() {
		for _, c := range r.cols[r.focus+1:] {
			dropped = append(dropped, c.id)
		}
		r.cols = r.cols[:r.focus+1]
	}
	r.cols = append(r.cols, column{id: id, label: label})
	r.focus = len(r.cols) - 1
	return r.reflow(), dropped
}

// deeper переводит фокус на колонку правее, если она есть, иначе лента
// возвращается неизменной.
func (r ribbon) deeper() ribbon {
	if r.hasDeeper() {
		r.focus++
	}
	return r.reflow()
}

// shallower переводит фокус на колонку левее; на самой левой ничего не
// делает — выход из самой левой колонки это esc, а не стрелка.
func (r ribbon) shallower() ribbon {
	if r.focus > 0 {
		r.focus--
	}
	return r.reflow()
}

// leftmost переводит фокус на самую левую колонку.
func (r ribbon) leftmost() ribbon {
	r.focus = 0
	return r.reflow()
}

// remember записывает позицию cursor и ключ key в колонку в фокусе — план
// 13-03 наполняет их смыслом, здесь только хранение.
func (r ribbon) remember(cursor int, key string) ribbon {
	if r.empty() {
		return r
	}
	r.cols[r.focus].cursor = cursor
	r.cols[r.focus].key = key
	return r
}

// recall читает запомненную позицию колонки id.
func (r ribbon) recall(id columnID) (int, string) {
	for _, c := range r.cols {
		if c.id == id {
			return c.cursor, c.key
		}
	}
	return 0, ""
}

// setSize передаёт ленте размер кадра — на этом же сообщении пересчитывается
// окно видимых колонок (tea.WindowSizeMsg, internal/ui/app.go).
func (r ribbon) setSize(width, height int) ribbon {
	r.width, r.height = width, height
	return r.reflow()
}

// visibleCount — сколько колонок помещается одновременно: наибольшее n от
// одного до ColumnsVisibleMax, для которого n*ColumnMin + (n-1)*PanelGap
// укладывается в ширину за вычетом отступов от обоих краёв, но не больше
// числа колонок в самой ленте.
func (r ribbon) visibleCount() int {
	if r.empty() {
		return 0
	}
	avail := r.width - 2*OuterMargin
	n := 1
	for c := 2; c <= ColumnsVisibleMax; c++ {
		if c*ColumnMin+(c-1)*PanelGap > avail {
			break
		}
		n = c
	}
	if n > len(r.cols) {
		n = len(r.cols)
	}
	return n
}

// window — окно видимых колонок, подтянутое так, чтобы колонка фокуса в
// него попадала (уехал фокус влево — окно едет влево, уехал правее правого
// края — окно едет вправо), с прижатием к границам ленты.
func (r ribbon) window() (first, count int) {
	count = r.visibleCount()
	if count == 0 {
		return 0, 0
	}
	first = r.first
	if r.focus < first {
		first = r.focus
	}
	if r.focus >= first+count {
		first = r.focus - count + 1
	}
	if first+count > len(r.cols) {
		first = len(r.cols) - count
	}
	if first < 0 {
		first = 0
	}
	return first, count
}

// reflow пересчитывает окно видимых колонок так, чтобы инвариант «фокус
// всегда в кадре» держался после любого изменения фокуса, состава или
// размера ленты — единственное место, где r.first меняется.
func (r ribbon) reflow() ribbon {
	r.first, _ = r.window()
	return r
}

// layout — ширины видимых колонок слева направо: доступная ширина за
// вычетом зазоров делится поровну, остаток от деления уходит колонке в
// фокусе. Прежние доли и минимумы были у каждого экрана свои, и именно
// поэтому «стрелка вправо» значила разное — одна сетка на все колонки и
// есть содержание этой фазы.
func (r ribbon) layout() []int {
	first, count := r.window()
	if count == 0 {
		return nil
	}
	avail := r.width - 2*OuterMargin - (count-1)*PanelGap
	base := avail / count
	rem := avail % count
	widths := make([]int, count)
	for i := range widths {
		widths[i] = base
	}
	widths[r.focus-first] += rem
	return widths
}

// columnBoxHeight — высота ОДНОЙ колонки в кадре целиком: заголовок строкой
// плюс рамка колонки (Фаза 16, LOOK-01) — и она ОДНА на все видимые колонки
// ленты сразу, а не считается по содержимому каждой отдельно: именно так
// пропадает пустота внизу более коротких колонок (LOOK-02) — каждая колонка
// обязана дотянуться до пола кадра, а не только до конца своего содержимого.
// Метод ничего не рисует (лента по-прежнему не рисует, см. шапку файла) — он
// только число, как и layout() выше.
func (r ribbon) columnBoxHeight() int {
	h := r.height - RibbonChromeHeight
	if h < 0 {
		h = 0
	}
	return h
}

// navAction — что означает нажатие в терминах ленты.
type navAction int

const (
	navNone navAction = iota
	navDeeper
	navShallower
	navLeftmost
)

// navFor — единственная функция проекта, отображающая клавишу в движение по
// ленте (Фаза 13, NAV-01): стрелка вправо и ⏎ дают navDeeper, стрелка влево
// — navShallower, esc — navLeftmost, всё остальное — navNone. Второе место
// толкования стрелки и есть та поломка, которую фаза чинит — закон живёт
// ровно здесь, а не веткой внутри разбора клавиш каждого экрана.
func navFor(msg tea.KeyPressMsg, k KeyMap) navAction {
	switch {
	case key.Matches(msg, k.Right), key.Matches(msg, k.Open):
		return navDeeper
	case key.Matches(msg, k.Left):
		return navShallower
	case key.Matches(msg, k.Back):
		return navLeftmost
	}
	return navNone
}
