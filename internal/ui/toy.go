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
	"time"

	tea "charm.land/bubbletea/v2"
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
