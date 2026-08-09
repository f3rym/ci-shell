// Package render печатает доменные типы provider в читаемом виде.
package render

import (
	"fmt"
	"io"

	"github.com/f3rym/ci-shell/internal/provider"
	"github.com/f3rym/ci-shell/internal/textwidth"
)

// Job печатает метаданные джобы job и разобранную конфигурацию cfg в w:
// шапку с именем, id и статусом, блок выровненных полей, затем нумерованные
// списки before_script и script.
func Job(w io.Writer, job provider.Job, cfg provider.JobConfig) error {
	if _, err := fmt.Fprintf(w, "джоба %s #%d — %s\n", orDash(job.Name), job.ID, orDash(job.Status)); err != nil {
		return err
	}
	if job.FailureReason != "" {
		if _, err := fmt.Fprintf(w, "  причина падения: %s\n", job.FailureReason); err != nil {
			return err
		}
	}

	fields := []struct {
		label string
		value string
	}{
		{"проект", orDash(job.ProjectPath)},
		{"стадия", orDash(job.Stage)},
		{"ref", orDash(job.Ref)},
		{"коммит", commitLine(job)},
		{"образ", orDash(cfg.Image)},
		{"ранер", orDash(job.RunnerDesc)},
		{"ссылка", orDash(job.WebURL)},
	}
	// Ширина левой колонки — максимум графемной меры (internal/textwidth) по
	// подписям блока, а не байтовая длина форматного глагола: подпись
	// «проект:» в байтах длиннее восьми, поэтому не дополнялась вовсе, а
	// «ref:» дополнялась — «блок выровненных полей» из документации функции
	// блоком не был (01-REVIEW.md IN-01).
	labelWidth := 0
	for _, f := range fields {
		if lw := textwidth.Of(f.label + ":"); lw > labelWidth {
			labelWidth = lw
		}
	}
	for _, f := range fields {
		if _, err := fmt.Fprintf(w, "  %s %s\n", textwidth.Pad(f.label+":", labelWidth), f.value); err != nil {
			return err
		}
	}

	if err := printSteps(w, "before_script", cfg.BeforeScript); err != nil {
		return err
	}
	if err := printSteps(w, "script", cfg.Script); err != nil {
		return err
	}
	return nil
}

func commitLine(job provider.Job) string {
	if job.CommitSHA == "" {
		return "—"
	}
	sha := job.CommitSHA
	if len(sha) > 8 {
		sha = sha[:8]
	}
	if job.CommitTitle == "" {
		return sha
	}
	return fmt.Sprintf("%s (%s)", sha, job.CommitTitle)
}

// printSteps печатает нумерованный список шагов с их количеством в
// заголовке. Пустой список — это "—", а не пустая строка.
func printSteps(w io.Writer, label string, steps []string) error {
	if len(steps) == 0 {
		_, err := fmt.Fprintf(w, "%s: —\n", label)
		return err
	}
	if _, err := fmt.Fprintf(w, "%s (%d %s):\n", label, len(steps), plural(len(steps), "шаг", "шага", "шагов")); err != nil {
		return err
	}
	for i, step := range steps {
		if _, err := fmt.Fprintf(w, "  %d. %s\n", i+1, step); err != nil {
			return err
		}
	}
	return nil
}

// plural выбирает форму русского числительного по числу n: one — «1», few —
// «2»…«4», many — всё остальное, с исключением для «11»…«14» (те звучат как
// many, а не few, несмотря на последнюю цифру 1-4). Закрывает 01-REVIEW.md
// IN-02: единственный шаг печатался «(1 шагов)», а не «(1 шаг)».
func plural(n int, one, few, many string) string {
	if n%100 >= 11 && n%100 <= 14 {
		return many
	}
	switch n % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	default:
		return many
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
