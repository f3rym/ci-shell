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
// самая левая колонка становится id. С Фазы 18 (KEYS-02) esc/q из неё больше
// не выходят — leftmost()/shallower() на уже самой левой колонке не делают
// ничего (см. их комментарии ниже), выход остался на Cancel и на :q.
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
// делает — с Фазы 18 (KEYS-02) выхода из самой левой колонки по esc/q
// больше нет вовсе, выход остался на Cancel (ctrl+c) и на :q.
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

// collapsedFor сообщает, сколько колонок окажется свёрнуто (левее видимого
// окна), ЕСЛИ показать n полных колонок: та же формула первого элемента
// окна, что уже считает window() (first := r.focus - n + 1, прижатое к
// [0, len(r.cols)-n]), но БЕЗ памяти r.first — чистая функция от n и
// текущего фокуса. window() при выборе first помнит, откуда лента уже
// показывала (гистерезис Фазы 13, план 13-02) — collapsedFor этой памяти не
// держит, ей нужно только число колонок, оставшихся левее, для оценки
// бюджета ширины кандидата n, а не само окно.
func (r ribbon) collapsedFor(n int) int {
	if n <= 0 || r.empty() {
		return 0
	}
	first := r.focus - n + 1
	if first < 0 {
		first = 0
	}
	if first > len(r.cols)-n {
		first = len(r.cols) - n
	}
	if first < 0 {
		first = 0
	}
	return first
}

// visibleCount — сколько колонок помещается одновременно: наибольшее n от
// одного до ColumnsVisibleMax, для которого n*ColumnMin + (n-1)*PanelGap
// укладывается в ширину за вычетом отступов от обоих краёв, но не больше
// числа колонок в самой ленте. Ширина, зарезервированная под свёрнутые
// колонки (collapsedFor(c), Фаза 16, LOOK-03), вычитается из бюджета ДО
// сравнения с avail: без этого вычета лента заняла бы больше ширины
// терминала, чем есть на самом деле, как только слева появляется хотя бы
// одна свёрнутая колонка — переполнение вправо, а не «прижаты к краям»
// (LOOK-02, план 16-01).
func (r ribbon) visibleCount() int {
	if r.empty() {
		return 0
	}
	avail := r.width - 2*OuterMargin
	n := 1
	for c := 2; c <= ColumnsVisibleMax; c++ {
		budget := c*ColumnMin + (c-1)*PanelGap
		if collapsed := r.collapsedFor(c); collapsed > 0 {
			budget += collapsed * (CollapsedColumnWidth + PanelGap)
		}
		if budget > avail {
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

// layout — ширины видимых колонок слева направо, по желаемой ширине каждой
// (Фаза 17, FIT-02/FIT-03): desired[i] — желаемая ширина колонки i, её
// считает вызывающий (App, единственный диспетчер — см. App.visibleLayout,
// internal/ui/app.go), лента по-прежнему не знает о содержимом колонок,
// шапка файла не меняется. Пол каждой колонки — ColumnMin (тот же, что уже
// держит visibleCount()); потолок каждой — avail-(count-1)*ColumnMin, чтобы
// одна колонка с очень большим желанием не столкнула соседей ниже пола.
//
// Если сумма желаемых ширин (после пола и потолка) укладывается в доступную
// ширину — каждая колонка получает ровно своё, а остаток уходит колонке(ям)
// с САМЫМ БОЛЬШИМ желанием: «мало текста слева — правая шире» — у левой
// желание меньше, у правой больше, весь остаток уходит правой (FIT-02).
// Если не укладывается — доступная ширина делится поровну с остатком
// колонке в фокусе, тем же способом, что действовал до этой фазы: «много и
// там и там — пополам». Оба случая вместе гарантируют, что сумма
// возвращённых ширин равна avail всегда — пустого места не остаётся ни в
// одном из них (FIT-03).
func (r ribbon) layout(desired []int) []int {
	first, count := r.window()
	if count == 0 {
		return nil
	}
	avail := r.width - 2*OuterMargin - (count-1)*PanelGap
	// Колонки [0, first) — все свёрнутые (Фаза 16, LOOK-03): их ширина
	// вычитается из бюджета ДО деления на count той же поправкой, что
	// visibleCount() уже применяет к своей оценке бюджета выше.
	if collapsedN := first; collapsedN > 0 {
		avail -= collapsedN * (CollapsedColumnWidth + PanelGap)
	}
	if avail < count*ColumnMin {
		avail = count * ColumnMin
	}

	clamped := make([]int, count)
	total := 0
	for i, d := range desired {
		ceil := avail - (count-1)*ColumnMin
		if ceil < ColumnMin {
			ceil = ColumnMin
		}
		c := d
		if c < ColumnMin {
			c = ColumnMin
		}
		if c > ceil {
			c = ceil
		}
		clamped[i] = c
		total += c
	}

	widths := make([]int, count)
	if total <= avail {
		copy(widths, clamped)
		extra := avail - total
		if extra > 0 {
			maxWant := clamped[0]
			for _, c := range clamped {
				if c > maxWant {
					maxWant = c
				}
			}
			var winners []int
			for i, c := range clamped {
				if c == maxWant {
					winners = append(winners, i)
				}
			}
			share := extra / len(winners)
			rem := extra % len(winners)
			for k, i := range winners {
				widths[i] += share
				if k < rem {
					widths[i]++
				}
			}
		}
		return widths
	}

	base := avail / count
	rem := avail % count
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

// collapsed возвращает колонки левее видимого окна (Фаза 16, LOOK-03): те же
// колонки, что раньше просто уезжали за край и переставали существовать в
// кадре вовсе (Фаза 13) — теперь они остаются на экране, просто уже, а не
// исчезают. Пустой срез (nil), если свёрнутых колонок нет.
func (r ribbon) collapsed() []column {
	first, _ := r.window()
	if first == 0 {
		return nil
	}
	return r.cols[:first]
}

// focusOn переводит фокус на колонку id, если она есть в ленте, и
// пересчитывает окно видимых колонок (r.reflow()). Одношаговые
// deeper()/shallower()/leftmost() выше двигают фокус НА ОДНУ позицию или в
// начало; клику мыши (Фаза 16, LOOK-05, internal/ui/app.go, clickRow) нужно
// попасть на ПРОИЗВОЛЬНУЮ уже открытую колонку одним вызовом — второго
// способа задать фокус в проекте не появляется, focusOn встаёт в тот же ряд
// методов, что и три перечисленных.
func (r ribbon) focusOn(id columnID) ribbon {
	for i, c := range r.cols {
		if c.id == id {
			r.focus = i
			break
		}
	}
	return r.reflow()
}

// hitTest переводит координату клика терминала (x, y, обе с нуля, как отдаёт
// tea.Mouse) в колонку и номер строки внутри её списка (Фаза 16, LOOK-05).
// contentTop — номер строки (считая с нуля от верха кадра), с которой
// начинается ПЕРВАЯ строка содержимого ВНУТРИ рамки первой колонки
// (заголовок колонки и верхняя граница рамки уже позади) — значение
// приходит от вызывающего (App.contentTop, internal/ui/app.go), потому что
// ленте по контракту файла ничего не известно про заголовок кадра и рамку
// сверх собственной геометрии колонок.
//
// Позиция X = 0 для самой левой колонки, а не OuterMargin: тело кадра
// (App.viewInner, internal/ui/app.go) пишет ribbonView() без отступа слева —
// отступ учтён только в бюджете ширины (visibleCount()/layout() выше вычитают
// 2*OuterMargin из доступной ширины терминала), но не в самих печатаемых
// строках тела. hitTest обязан мерить ту же геометрию, что печатает
// ribbonView, а не декларативный отступ, иначе клик по первой колонке не
// совпал бы с тем, что человек реально видит на экране.
//
// widths — ширины видимых колонок, уже посчитанные вызывающим (Фаза 17,
// App.visibleLayout): клик мыши обязан видеть ТЕ ЖЕ ширины, что только что
// нарисовал ribbonView — оба зовут один и тот же расчёт вызывающего кода, а
// не пересчитывают его дважды с риском разойтись в кадре, где содержимое
// между двумя вызовами могло уже измениться.
func (r ribbon) hitTest(widths []int, x, y, contentTop int) (id columnID, row int, ok bool) {
	col := 0
	for _, c := range r.collapsed() {
		if x >= col && x < col+CollapsedColumnWidth {
			id = c.id
			row = -1
			ok = true
			return
		}
		col += CollapsedColumnWidth + PanelGap
	}

	first, count := r.window()
	for i := 0; i < count; i++ {
		w := widths[i]
		if x >= col && x < col+w {
			id = r.cols[first+i].id
			if y < contentTop {
				row = -1
			} else {
				row = y - contentTop
			}
			ok = true
			return
		}
		col += w + PanelGap
	}

	return
}

// navAction — что означает нажатие в терминах ленты.
type navAction int

const (
	navNone navAction = iota
	navDeeper
	// navConfirm — ⏎: не «шаг вправо», а «жму то, на чём стою». Отличается
	// от navDeeper ровно одним: колонка правее пересобирается из текущего
	// выбора, даже если она уже открыта. Без этого различия человек,
	// вернувшийся влево и вставший на другую джобу, по ⏎ попадал в шаги
	// ПРЕЖНЕЙ джобы — фокус просто переезжал в уже открытую колонку, и
	// сменить выбор было нельзя вовсе.
	navConfirm
	navShallower
	navLeftmost
)

// navFor — единственная функция проекта, отображающая клавишу в движение по
// ленте (Фаза 13, NAV-01): стрелка вправо даёт navDeeper, ⏎ — navConfirm,
// стрелка влево — navShallower, esc — navLeftmost, всё остальное — navNone.
// Второе место толкования стрелки и есть та поломка, которую фаза чинит —
// закон живёт ровно здесь, а не веткой внутри разбора клавиш каждого экрана.
//
// ⏎ перестал быть точным синонимом → (правка после живого прогона): «жму»
// и «листаю» — разные намерения, и на уже открытой колонке справа они
// расходятся. → переводит фокус в неё как есть, ⏎ пересобирает её из того,
// на чём стоит курсор. Для случая «справа пусто» оба по-прежнему делают
// ровно одно и то же, поэтому обещание «⏎ работает как →» для человека,
// идущего вглубь впервые, остаётся верным.
//
// q матчится в ту же ветку, что и стрелка влево (Фаза 18, KEYS-02): с этой
// фазы q — ровно то же движение по ленте, что и ←, той же самой веткой —
// второй реализации «шага назад» здесь не появляется, q не получает своего
// navAction. Выход по q убран (internal/ui/app.go) — единственный оставшийся
// путь выхода это Cancel (ctrl+c) и команда :q.
func navFor(msg tea.KeyPressMsg, k KeyMap) navAction {
	switch {
	case key.Matches(msg, k.Open):
		return navConfirm
	case key.Matches(msg, k.Right):
		return navDeeper
	case key.Matches(msg, k.Left), key.Matches(msg, k.Quit):
		return navShallower
	case key.Matches(msg, k.Back):
		return navLeftmost
	}
	return navNone
}
