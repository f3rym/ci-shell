package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/f3rym/ci-shell/internal/env"
)

// Env печатает собранное окружение e в w: заголовок с количеством
// переменных, затем по строке на переменную — ключ, значение и источник в
// скобках. Пустой список переменных печатается строкой "окружение: —", по
// образцу orDash в internal/render/job.go. Если в e есть оговорки
// (недоступные источники, переменные с чужой областью окружения), они
// печатаются отдельным блоком после списка переменных — по строке на
// оговорку. После оговорок печатается отчёт о недостающих маскированных
// переменных (missingReport), если такие есть.
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

	if len(e.Notices) > 0 {
		if _, err := fmt.Fprintln(w, "оговорки:"); err != nil {
			return err
		}
		for _, n := range e.Notices {
			if _, err := fmt.Fprintf(w, "  - %s\n", n); err != nil {
				return err
			}
		}
	}

	return missingReport(w, e)
}

// missingReport печатает отчёт о маскированных переменных, значение
// которых не покрыто ни одним слоем (e.Missing): сколько их и какие,
// фактический путь к файлу секретов (e.SecretsPath), готовый к вставке
// фрагмент YAML и команду ограничения прав. Пустой e.Missing — отчёт не
// печатается вовсе. В отчёте печатаются только ключи и заглушки — ни
// Variable.Value, ни e.Map() в этот блок не попадают (T-02-10).
func missingReport(w io.Writer, e env.Environment) error {
	if len(e.Missing) == 0 {
		return nil
	}

	keys := make([]string, 0, len(e.Missing))
	for _, m := range e.Missing {
		keys = append(keys, m.Key)
	}

	if _, err := fmt.Fprintf(w, "\nGitLab не отдаёт значения %d переменных (тип masked and hidden — пишутся один раз и через API не читаются): %s\n", len(e.Missing), strings.Join(keys, ", ")); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "заполните их один раз в файле %s:\n\n", e.SecretsPath); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "projects:\n  %s:\n", projectKey(e)); err != nil {
		return err
	}
	for _, m := range e.Missing {
		if _, err := fmt.Fprintf(w, "    %s: \"<значение>\"\n", m.Key); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "\nи ограничьте права файла: chmod 600 %s\n", e.SecretsPath); err != nil {
		return err
	}
	return nil
}

// projectKey собирает ключ проекта для фрагмента YAML из e.Host и
// e.ProjectPath — тот же ключ, по которому LoadSecrets ищет запись в файле
// секретов, чтобы фрагмент можно было скопировать без правок. Умышленно не
// читает собранные переменные (CI_SERVER_HOST/CI_PROJECT_PATH): их может
// перекрыть верхний слой — секрет из локального файла попал бы в отчёт
// в обход displayValue, а маскированная переменная API испортила бы ключ
// пустым значением (WR-01).
func projectKey(e env.Environment) string {
	return e.Host + "/" + e.ProjectPath
}

// displayValue — единственная точка, где решается, показывать значение
// переменной пользователю или нет: для Variable.Secret возвращает
// плейсхолдер вместо значения (T-02-01); для файловых переменных
// (env.KindFile) — маркер вместо инлайна содержимого (WR-05).
func displayValue(v env.Variable) string {
	if v.Secret {
		return "<скрыто>"
	}
	if v.Kind == env.KindFile {
		// Значение файловой переменной — целое содержимое файла
		// (kubeconfig, CA bundle, .npmrc): инлайн в списке окружения оно
		// не печатается. Материализация во временный файл перед docker run
		// — задача Фазы 3.
		return "<file variable>"
	}
	return v.Value
}
