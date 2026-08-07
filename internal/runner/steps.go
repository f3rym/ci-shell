package runner

import (
	"context"
	"io"
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

// RunSteps прогоняет steps по порядку внутри уже поднятого контейнера c,
// печатая пошаговый прогресс через p и направляя вывод каждого шага в out и
// errOut. Обход прекращается на первом ненулевом коде выхода: шаги после
// упавшего не выполняются никогда, потому что их выполнение изменило бы
// состояние контейнера относительно воспроизводимого прогона. Ненулевой err
// означает сбой самого docker (контейнер умер, демон отвалился) — вызывающий
// код должен убрать за собой.
func RunSteps(ctx context.Context, c *Container, steps []Step, out, errOut io.Writer, p render.Progress) (Outcome, error) {
	o := Outcome{Total: len(steps)}

	for i, s := range steps {
		p.Step(i+1, len(steps), s.Section, s.Command)

		code, err := c.Exec(ctx, s.Command, out, errOut)
		if err != nil {
			return o, err
		}

		if code != 0 {
			failed := s
			o.Failed = &failed
			o.FailedIndex = i + 1
			o.ExitCode = code
			break
		}

		o.Executed++
	}

	p.Summary(o.Executed, o.Total, o.Failed != nil)
	return o, nil
}
