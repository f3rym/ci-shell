package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/f3rym/ci-shell/internal/provider"
	"github.com/f3rym/ci-shell/internal/render"
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
// ним печатается пошаговый прогресс через p, сами маркеры в out не
// попадают, остальной вывод идёт в out и errOut насквозь. Упавший шаг
// определяется последним увиденным маркером: set -e обрывает скрипт на
// первом ненулевом коде с кодом упавшей команды, поэтому шаги после
// упавшего не выполняются никогда — их выполнение изменило бы состояние
// контейнера относительно воспроизводимого прогона. Ненулевой err означает
// сбой самого docker (контейнер умер, демон отвалился) или прерывание
// пользователем — вызывающий код должен убрать за собой.
func RunSteps(ctx context.Context, c *Container, steps []Step, out, errOut io.Writer, p render.Progress) (Outcome, error) {
	o := Outcome{Total: len(steps)}
	if len(steps) == 0 {
		p.Summary(0, 0, false)
		return o, nil
	}

	scan := &markerScanner{out: out, steps: steps, p: p}
	code, err := c.Exec(ctx, buildScript(steps), scan, errOut)
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

	p.Summary(o.Executed, o.Total, o.Failed != nil)
	return o, nil
}

// markerPrefix и markerSuffix обрамляют номер шага в echo-маркере,
// печатаемом скриптом перед каждым шагом. Строка выбрана так, чтобы не
// встретиться в реальном выводе джобы.
const (
	markerPrefix = "__CI_SHELL_STEP_"
	markerSuffix = "__"
)

// buildScript склеивает шаги в один скрипт единой shell-сессии: set -e
// обрывает исполнение на первом ненулевом коде выхода — с кодом упавшей
// команды, как у GitLab-ранера, — а echo-маркер перед каждым шагом отдаёт
// прогресс наружу. Маркер печатается без кавычек: он состоит только из
// букв, цифр и подчёркиваний, оболочке в нём раскрывать нечего.
func buildScript(steps []Step) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	for i, s := range steps {
		fmt.Fprintf(&b, "echo %s%d%s\n", markerPrefix, i+1, markerSuffix)
		b.WriteString(s.Command)
		b.WriteString("\n")
	}
	return b.String()
}

// parseMarker распознаёт строку-маркер и возвращает номер шага, считая от
// единицы. Сравнение строгое — обрезаются только завершающие \r\n, чтобы
// похожая строка из вывода джобы с отступом не была проглочена как маркер.
func parseMarker(line string) (int, bool) {
	trimmed := strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(trimmed, markerPrefix) || !strings.HasSuffix(trimmed, markerSuffix) {
		return 0, false
	}
	digits := strings.TrimSuffix(strings.TrimPrefix(trimmed, markerPrefix), markerSuffix)
	n, err := strconv.Atoi(digits)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// markerScanner — io.Writer поверх out, вылавливающий строки-маркеры из
// stdout скрипта: маркер превращается в строку прогресса и в out не
// попадает, остальной вывод проходит насквозь. Неполные строки копятся в
// буфере до перевода строки; хвост без перевода строки дописывает flush.
type markerScanner struct {
	out   io.Writer
	steps []Step
	p     render.Progress
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

// line обрабатывает одну строку: маркер — в прогресс, всё остальное —
// насквозь в out. Ошибка записи в out игнорируется по образцу
// render.Progress: сбой служебного вывода не должен ронять воспроизведение.
func (m *markerScanner) line(line string) {
	if n, ok := parseMarker(line); ok && n <= len(m.steps) {
		m.last = n
		s := m.steps[n-1]
		m.p.Step(n, len(m.steps), s.Section, s.Command)
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
