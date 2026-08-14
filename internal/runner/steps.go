package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/f3rym/ci-shell/internal/event"
	"github.com/f3rym/ci-shell/internal/provider"
)

// Step — один шаг джобы: секция (before_script или script), в которой он
// объявлен, и сама команда — теми же строками, что напечатаны в выводе
// Фазы 1, чтобы пользователь узнавал в прогрессе то, что уже видел в блоке
// шагов.
type Step struct {
	Section string
	Command string
}

// Outcome — результат прогона шагов джобы. Failed равен nil, когда ни один
// шаг не упал; FailedIndex — порядковый номер упавшего шага, считая от
// единицы.
type Outcome struct {
	Total       int
	Executed    int
	Failed      *Step
	FailedIndex int
	ExitCode    int
}

// Steps строит порядок шагов джобы: сначала все элементы cfg.BeforeScript,
// затем все элементы cfg.Script, в исходном порядке. Пустые и состоящие из
// пробелов строки отбрасываются — ранер их тоже не исполняет, а в прогрессе
// они выглядели бы пропущенными шагами.
func Steps(cfg provider.JobConfig) []Step {
	steps := make([]Step, 0, len(cfg.BeforeScript)+len(cfg.Script))
	steps = appendSteps(steps, "before_script", cfg.BeforeScript)
	steps = appendSteps(steps, "script", cfg.Script)
	return steps
}

func appendSteps(steps []Step, section string, commands []string) []Step {
	for _, c := range commands {
		if strings.TrimSpace(c) == "" {
			continue
		}
		steps = append(steps, Step{Section: section, Command: c})
	}
	return steps
}

// RunSteps прогоняет steps внутри уже поднятого контейнера c одной
// shell-сессией — как GitLab-ранер: export, cd, функции и опции оболочки из
// шага N действуют в шаге N+1. Все шаги склеиваются buildScript в один
// скрипт с set -e и echo-маркером перед каждым шагом; скрипт уходит одним
// docker exec, маркеры вылавливаются из stdout на лету (markerScanner): по
// ним em эмитит пошаговый прогресс, сами маркеры в out не попадают,
// остальной вывод идёт в out и errOut насквозь. Упавший шаг определяется
// последним увиденным маркером: set -e обрывает скрипт на первом ненулевом
// коде с кодом упавшей команды, поэтому шаги после упавшего не выполняются
// никогда — их выполнение изменило бы состояние контейнера относительно
// воспроизводимого прогона. Ненулевой err означает сбой самого docker
// (контейнер умер, демон отвалился) или прерывание пользователем —
// вызывающий код должен убрать за собой.
//
// Вердикт о шагах (упал/прошли) здесь НЕ эмитится: только оркестратор знает,
// первый это прогон или чистый, и только у него вердикт стоит на своём
// месте в порядке строк — эмиссия отсюда переставила бы строки местами
// относительно отложенных предупреждений об уборке чистого прогона.
func RunSteps(ctx context.Context, c *Container, steps []Step, out, errOut io.Writer, em event.Emitter) (Outcome, error) {
	o := Outcome{Total: len(steps)}
	if len(steps) == 0 {
		em.Emit(event.StepsSummary{Executed: 0, Total: 0, Stopped: false})
		return o, nil
	}

	// Нонс — часть защиты от коллизии с выводом самой джобы (CR-02 обзора
	// v1.0.0, см. markerNonce ниже): без него текст echo-маркера — статичная,
	// заранее известная строка, и джоба сама может её напечатать, случайно
	// или подделывая номер упавшего шага.
	nonce, err := markerNonce()
	if err != nil {
		return o, err
	}

	scan := &markerScanner{out: out, steps: steps, em: em, nonce: nonce}
	code, err := c.Exec(ctx, buildScript(steps, nonce), scan, errOut)
	scan.flush()
	if err != nil {
		return o, err
	}

	if code != 0 {
		idx := scan.last
		if idx < 1 {
			// Скрипт упал до первого маркера (оболочка не разобрала сам
			// скрипт) — считаем упавшим первый шаг.
			idx = 1
		}
		failed := steps[idx-1]
		o.Failed = &failed
		o.FailedIndex = idx
		o.ExitCode = code
		o.Executed = idx - 1
	} else {
		o.Executed = len(steps)
	}

	em.Emit(event.StepsSummary{Executed: o.Executed, Total: o.Total, Stopped: o.Failed != nil})
	return o, nil
}

// markerPrefix и markerSuffix обрамляют нонс и номер шага в echo-маркере,
// печатаемом скриптом перед каждым шагом.
const (
	markerPrefix = "__CI_SHELL_STEP_"
	markerSuffix = "__"
)

// markerNonceBytes — длина случайного нонса маркера в байтах (32
// шестнадцатеричных символа на выходе) — достаточно, чтобы угадать или
// подобрать значение перебором за время одного прогона джобы было
// невозможно на практике.
const markerNonceBytes = 16

// markerNonce возвращает случайный шестнадцатеричный токен для ОДНОГО
// вызова RunSteps. Раньше текст echo-маркера (__CI_SHELL_STEP_N__) был
// статичной, заранее известной строкой — и джоба на любом шаге могла
// напечатать точно такую же строку, случайно (эхо переменной окружения,
// тестовые данные, вывод другого инструмента с похожим форматом маркеров
// прогресса) или намеренно. markerScanner.line принял бы её за настоящий
// маркер и сдвинул scan.last вперёд или назад — «упавшим» назвался бы не
// тот шаг, который упал по-настоящему, а retry перезапускал бы не ту
// команду (CR-02 обзора исполняющей части v1.0.0). Нонс печатается только
// как часть маркера в СКРИПТЕ, который docker exec ещё не начал исполнять
// в момент генерации — job не может напечатать его заранее, потому что не
// видела текста собственного скрипта до его исполнения.
func markerNonce() (string, error) {
	b := make([]byte, markerNonceBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("runner: не удалось получить случайный нонс маркера шага: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// buildScript склеивает шаги в один скрипт единой shell-сессии: set -e
// обрывает исполнение на первом ненулевом коде выхода — с кодом упавшей
// команды, как у GitLab-ранера, — а echo-маркер перед каждым шагом отдаёт
// прогресс наружу. nonce — тот же самый, каким затем читает маркеры
// markerScanner (RunSteps генерирует его один раз на вызов и передаёт в
// обе стороны) — без общего нонса читатель не узнал бы свой же маркер.
// Маркер печатается без кавычек: он состоит только из букв, цифр и
// подчёркиваний, оболочке в нём раскрывать нечего.
func buildScript(steps []Step, nonce string) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	for i, s := range steps {
		fmt.Fprintf(&b, "echo %s%s_%d%s\n", markerPrefix, nonce, i+1, markerSuffix)
		b.WriteString(s.Command)
		b.WriteString("\n")
	}
	return b.String()
}

// parseMarker распознаёт строку-маркер с нонсом nonce (см. markerNonce) и
// возвращает номер шага, считая от единицы. Сравнение строгое — обрезаются
// только завершающие \r\n, чтобы похожая строка из вывода джобы с отступом
// не была проглочена как маркер.
func parseMarker(nonce, line string) (int, bool) {
	prefix := markerPrefix + nonce + "_"
	trimmed := strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(trimmed, prefix) || !strings.HasSuffix(trimmed, markerSuffix) {
		return 0, false
	}
	digits := strings.TrimSuffix(strings.TrimPrefix(trimmed, prefix), markerSuffix)
	n, err := strconv.Atoi(digits)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// stepBoundary — необязательный крючок для приёмника вывода (out,
// переданного в RunSteps): если он реализует этот интерфейс, markerScanner
// сообщает ему номер строки, с которой начинается очередной шаг. Не
// реализует — крючок молча не срабатывает, ровно как у любого необязательного
// интерфейса Go (тот же приём, что уже применён к интерфейсам стандартной
// библиотеки в проекте).
//
// Индекс, который получает MarkStep, ОТНОСИТЕЛЬНЫЙ — то же число, что несёт
// event.StepStarted.Index, считая от начала steps, переданного в ЭТОТ вызов
// RunSteps, а не абсолютный номер шага джобы; перевод в абсолютный — забота
// вызывающего кода, потому что только он знает offset среза (Фаза 15, план
// 15-03, LOG-05/LOG-06).
//
// Пакет runner не импортирует пакет интерфейса и не обязан знать о типе
// приёмника ничего сверх этого метода — соответствие интерфейсу
// устанавливается структурно (duck typing).
type stepBoundary interface {
	MarkStep(index int)
}

// markerScanner — io.Writer поверх out, вылавливающий строки-маркеры из
// stdout скрипта: маркер превращается в событие шага и в out не попадает,
// остальной вывод проходит насквозь. Неполные строки копятся в буфере до
// перевода строки; хвост без перевода строки дописывает flush.
type markerScanner struct {
	out   io.Writer
	steps []Step
	em    event.Emitter
	// nonce — случайный токен ОДНОГО вызова RunSteps (markerNonce), с
	// которым должен совпасть маркер в line ниже — без него любая строка
	// вывода джобы, похожая на __CI_SHELL_STEP_N__, принималась бы за
	// настоящий маркер (CR-02 обзора v1.0.0).
	nonce string
	last  int // номер последнего встреченного маркера, считая от единицы
	buf   bytes.Buffer
}

func (m *markerScanner) Write(b []byte) (int, error) {
	m.buf.Write(b)
	for {
		line, err := m.buf.ReadString('\n')
		if err != nil {
			// Неполная строка: вернуть хвост в буфер и ждать продолжения.
			m.buf.WriteString(line)
			break
		}
		m.line(line)
	}
	return len(b), nil
}

// line обрабатывает одну строку: маркер — событием шага, всё остальное —
// насквозь в out. Метод исполняется в горутине копирования стандартного
// вывода дочернего процесса (Container.Exec запускает cmd.Run синхронно, но
// сам docker exec копирует поток в scan из своей горутины io.Copy) — поэтому
// подписчик обязан быть потокобезопасным (контракт event.Sink, план 08-01).
func (m *markerScanner) line(line string) {
	if n, ok := parseMarker(m.nonce, line); ok && n <= len(m.steps) {
		m.last = n
		// Приёмник вывода МОЖЕТ уметь запоминать границы шагов (stepBoundary,
		// Фаза 15, план 15-03) — проверка типа в Go с двумя возвращаемыми
		// значениями не паникует при неудаче, поэтому крючок безопасен, даже
		// если out не реализует интерфейс.
		if sb, ok := m.out.(stepBoundary); ok {
			sb.MarkStep(n)
		}
		s := m.steps[n-1]
		m.em.Emit(event.StepStarted{Index: n, Total: len(m.steps), Section: s.Section, Command: s.Command})
		return
	}
	_, _ = m.out.Write([]byte(line))
}

// flush дописывает в out хвост, оставшийся без завершающего перевода строки.
func (m *markerScanner) flush() {
	if m.buf.Len() == 0 {
		return
	}
	m.line(m.buf.String())
	m.buf.Reset()
}
