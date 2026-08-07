package render

import (
	"fmt"
	"io"

	"github.com/f3rym/ci-shell/internal/env"
)

// Env печатает собранное окружение e в w: заголовок с количеством
// переменных, затем по строке на переменную — ключ, значение и источник в
// скобках. Пустой список переменных печатается строкой "окружение: —", по
// образцу orDash в internal/render/job.go. Если в e есть оговорки
// (недоступные источники, переменные с чужой областью окружения), они
// печатаются отдельным блоком после списка переменных — по строке на
// оговорку.
func Env(w io.Writer, e env.Environment) error {
	if len(e.Vars) == 0 {
		if _, err := fmt.Fprintln(w, "окружение: —"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(w, "окружение (%d переменных):\n", len(e.Vars)); err != nil {
			return err
		}
		for _, v := range e.Vars {
			if _, err := fmt.Fprintf(w, "  %s=%s (%s)\n", v.Key, displayValue(v), v.Source); err != nil {
				return err
			}
		}
	}

	if len(e.Notices) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w, "оговорки:"); err != nil {
		return err
	}
	for _, n := range e.Notices {
		if _, err := fmt.Fprintf(w, "  - %s\n", n); err != nil {
			return err
		}
	}
	return nil
}

// displayValue — единственная точка, где решается, показывать значение
// переменной пользователю или нет: для Variable.Secret возвращает
// плейсхолдер вместо значения (T-02-01).
func displayValue(v env.Variable) string {
	if v.Secret {
		return "<скрыто>"
	}
	return v.Value
}
