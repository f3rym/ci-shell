// toy.go — «игрушка» (Фаза 16, план 16-04, TOY-01…TOY-04, idea-0.3.1 §1.1):
// живой символьный элемент в правом углу строки заголовка кадра корневой
// модели. Украшение поверх уже готового интерфейса, которое можно резать без
// ущерба остальному (idea-0.3.1 §9) — файл отвечает ровно за ЧТО рисовать
// (пять вариантов) и КОГДА тикать (toyModel.ensure ниже), но НЕ решает,
// видна ли игрушка прямо сейчас: это решение зависит от состояния экрана
// (какой оверлей открыт, идёт ли долгая операция), которое знает только
// App, и живёт в internal/ui/app.go (App.toyVisible).
package ui

import (
	"math/rand"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/f3rym/ci-shell/internal/textwidth"
)

// toyKind — вариант игрушки: пять значений, объявленных в том же порядке,
// что и таблица idea-0.3.1 §1.1 — порядок объявления совпадает с порядком
// случайного выбора (parseToyKind ниже берёт rand.Intn(5) напрямую как
// значение toyKind).
type toyKind int

const (
	toyBall toyKind = iota
	toyWave
	toyBelt
	toyCat
	toyBreathe
)

// parseToyKind разбирает строку настройки animation. Распознанные значения
// дают конкретный вариант; "off" выключает игрушку целиком (kind в этом
// случае не важен вызывающему — ensure ниже проверяет off раньше kind).
// Пустая строка и ЛЮБАЯ нераспознанная строка (в том числе опечатка)
// обрабатываются ОДИНАКОВО — случайным выбором: settings.yml человек может
// редактировать руками, и утилита не должна ни отказывать в запуске, ни
// тихо считать опечатку выключением. Опечатка вырождается в то же самое
// «выбор не сделан», что и пустая строка (TOY-01, «без явного выбора при
// каждом запуске подставляется случайный»).
func parseToyKind(s string) (kind toyKind, off bool) {
	switch s {
	case "ball":
		return toyBall, false
	case "wave":
		return toyWave, false
	case "belt":
		return toyBelt, false
	case "cat":
		return toyCat, false
	case "breathe":
		return toyBreathe, false
	case "off":
		return toyBall, true
	default:
		// planner-discipline-allow: rand.Intn(5)
		return toyKind(rand.Intn(5)), false
	}
}

// toyModel — состояние игрушки: вариант, признак выключения, номер кадра
// анимации (растёт на каждый тик; период конкретного варианта берёт остаток
// от деления сам, в своей функции отрисовки — advance ниже не обязана знать
// периоды всех пяти вариантов) и признак идущей цепочки тиков (ticking).
// TOY-03 («такт не крутится вхолостую, пока игрушка скрыта») держится ровно
// полем ticking, а не угадыванием состояния снаружи.
type toyModel struct {
	kind    toyKind
	off     bool
	frame   int
	ticking bool
}

// newToyModel собирает модель из строки настройки. Такт не стартует здесь:
// первый тик планирует первый вызов ensure (см. ниже), когда App узнает, что
// игрушка видима, — до этого момента планировать нечего.
func newToyModel(setting string) toyModel {
	kind, off := parseToyKind(setting)
	return toyModel{kind: kind, off: off}
}

// toyTickInterval — интервал одного тика: середина диапазона «150–200 мс»
// источника (idea-0.3.1 §1.1).
const toyTickInterval = 180 * time.Millisecond

// toyTickMsg — сообщение одного тика игрушки.
type toyTickMsg struct{}

// armTick планирует следующий тик тем же приёмом, что уже применяют
// jobsModel (spinner.Tick) и joblog.go (tea.Tick(logDebounce, ...)) — третьего
// способа планирования одноразового тика в пакете не заводится.
func (m toyModel) armTick() tea.Cmd {
	return tea.Tick(toyTickInterval, func(time.Time) tea.Msg {
		return toyTickMsg{}
	})
}

// advance продвигает кадр анимации на один тик.
func (m toyModel) advance() toyModel {
	m.frame++
	return m
}

// ensure — ЕДИНСТВЕННАЯ точка входа/выхода такта игрушки: решает заново на
// каждый вызов, тикать ли дальше, и планирует следующий тик, только если
// видна. Выключенная игрушка (m.off) никогда не тикает — второй проверки off
// в вызывающем коде заводить незачем. Когда видимость ложна, цепочка тиков
// ОБРЫВАЕТСЯ ЗДЕСЬ (m.ticking = false, команда не возвращается) — второго
// места остановки такта в пакете нет и не появится: TOY-03 требует, чтобы
// таймер игрушки останавливался вместе с её скрытием, и второе место
// остановки расходилось бы с этим при следующей правке. Когда видимость
// истинна и цепочка уже идёт (m.ticking уже true), второй параллельной
// цепочки не заводится. Когда видимость истинна и цепочки ещё нет —
// цепочка запускается (или возобновляется после того, как видимость снова
// стала истинной).
func (m toyModel) ensure(visible bool) (toyModel, tea.Cmd) {
	if m.off {
		return m, nil
	}
	if !visible {
		m.ticking = false
		return m, nil
	}
	if m.ticking {
		return m, nil
	}
	m.ticking = true
	return m, m.armTick()
}

// toyStripWidth — ширина полосы игрушки в ячейках (TOY-04): вмещает самый
// широкий кадр волны (▁▂▃▄▅▆▇▆▅▄▃▂▁, 13 рун из закрытого набора блочных
// символов — ровно строка-образец idea-0.3.1 §1.1). Остальные три полосных
// варианта (мяч, конвейер, зверёк) рисуются в ТОЙ ЖЕ ширине, дополняясь
// пробелами через strip() ниже, чтобы полоса не меняла ширину строки
// заголовка от кадра к кадру.
const toyStripWidth = 13

// strip — ЕДИНСТВЕННАЯ точка сборки готовой полосы игрушки: пустая строка
// для выключенной игрушки и для дыхания (дыхание не рисует полосу вовсе —
// см. titleStyle ниже, оно меняет яркость самого заголовка), иначе — сырой
// кадр одной из четырёх функций отрисовки ниже, ПРОПУЩЕННЫЙ через
// textwidth.Fit(raw, toyStripWidth) и завёрнутый в приглушённый стиль темы
// (тот же, что уже несёт метаданные и заголовки панелей — игрушка не должна
// выглядеть ярче содержимого экрана). textwidth.Fit здесь — ЕДИНСТВЕННОЕ
// место, где ширина полосы становится гарантированной (TOY-04): ни одна из
// четырёх функций отрисовки ниже не обязана сама заботиться о точной
// ширине, только предложить кадр.
func (m toyModel) strip(t Theme) string {
	if m.off || m.kind == toyBreathe {
		return ""
	}
	var raw string
	switch m.kind {
	case toyBall:
		raw = ballFrame(m.frame)
	case toyWave:
		raw = waveFrame(m.frame)
	case toyBelt:
		raw = beltFrame(m.frame)
	case toyCat:
		raw = catFrame(m.frame)
	}
	return t.Muted.Render(textwidth.Fit(raw, toyStripWidth))
}

// ballFrame — мяч: дуга из 4 позиций (индексы 0–3), период 8 кадров. Первая
// половина периода (frame%8 в 0..3) идёт по позициям вперёд, вторая
// (frame%8 в 4..7) зеркалит те же позиции назад (7-й индекс отражает 0-й,
// 6-й — 1-й, и так далее) — мяч катится вправо и обратно по дуге из четырёх
// ячеек. Текущая позиция рисуется тем же глифом, что и активный статус
// (GlyphActive) — второго символа мяча в наборе темы не заводится; в
// остальных ячейках дуги — символ пола (▁), тот же блочный символ, что и у
// волны ниже.
func ballFrame(frame int) string {
	const arcLen = 4
	pos := frame % 8
	if pos >= arcLen {
		pos = 7 - pos
	}
	cells := make([]string, arcLen)
	for i := range cells {
		if i == pos {
			cells[i] = GlyphActive
		} else {
			cells[i] = "▁"
		}
	}
	return strings.Join(cells, "")
}

// waveSample — строка-образец волны, дословно из idea-0.3.1 §1.1: 13 рун,
// период отражения уже заложен в саму строку (она сама симметрична).
const waveSample = "▁▂▃▄▅▆▇▆▅▄▃▂▁"

// waveFrame — волна: waveSample циклически сдвигается на frame % len
// позиций (символ, ушедший с одного края, появляется с другого) — бежит
// слева направо и, дойдя до края периода, продолжает тем же сдвигом; сам
// образец уже симметричен, поэтому визуально читается как «туда-обратно», а
// код не хранит отдельно направление.
func waveFrame(frame int) string {
	r := []rune(waveSample)
	n := len(r)
	shift := frame % n
	shifted := make([]rune, n)
	for i := 0; i < n; i++ {
		shifted[i] = r[(i+shift)%n]
	}
	return string(shifted)
}

// beltFrame — конвейер: лента из ─ шириной toyStripWidth, поверх которой в
// позиции frame % toyStripWidth едет коробка (▪). Период всей анимации —
// 2*toyStripWidth: первая половина (frame % (2*toyStripWidth) < toyStripWidth)
// показывает коробку, едущую до правого края; вторая половина не показывает
// коробку вовсе — «упала в трубу» — прежде чем период начнётся заново с
// коробкой у левого края.
func beltFrame(frame int) string {
	cells := make([]string, toyStripWidth)
	for i := range cells {
		cells[i] = "─"
	}
	cyc := frame % (2 * toyStripWidth)
	if cyc < toyStripWidth {
		cells[cyc] = "▪"
	}
	return strings.Join(cells, "")
}

// catFrame — зверёк: ходит влево-вправо по 5 позициям (0..4) внутри строки
// длиной toyStripWidth, период 10 кадров. Первые пять кадров периода
// (c=0..4) идут по позициям вперёд; следующие четыре (c=5..8) зеркалят их
// назад (3,2,1,0); последний кадр периода (c=9) задерживает зверька у
// левого края на один лишний такт — вместе с раз-в-период морганием ниже это
// и даёт неровный, живой шаг вместо механического маятника. Раз в полный
// период (c==0) зверёк на один кадр «моргает» — рисуется приглушённым
// глифом GlyphIdle (○) вместо активного GlyphActive (●), тем же замкнутым
// набором символов темы, а не отдельным новым.
func catFrame(frame int) string {
	const period = 10
	c := frame % period
	pos := c
	if c > 4 {
		pos = 8 - c
		if pos < 0 {
			pos = 0
		}
	}
	glyph := GlyphActive
	if c == 0 {
		glyph = GlyphIdle
	}
	cells := make([]string, toyStripWidth)
	for i := range cells {
		cells[i] = " "
	}
	cells[pos] = glyph
	return strings.Join(cells, "")
}

// titleStyle — ЕДИНСТВЕННАЯ точка изменения стиля ЗАГОЛОВКА кадра (не
// полосы): дыхание — единственный вариант, трогающий стиль заголовка, а не
// полосу, потому что idea-0.3.1 §1.1 описывает его буквально как «само
// слово ci-shell медленно меняет яркость», не отдельный элемент рядом с
// ним. Переключение идёт тем же полем стиля Faint, каким уже помечена роль
// Muted этой же темы, — новой яркости, которой в проекте нет средства
// выразить, здесь не появляется.
func (m toyModel) titleStyle(base lipgloss.Style) lipgloss.Style {
	if !m.off && m.kind == toyBreathe {
		return base.Faint(m.frame%2 == 0)
	}
	return base
}
