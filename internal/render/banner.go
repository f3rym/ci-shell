package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/f3rym/ci-shell/internal/env"
)

// BannerInput — вход баннера допущений, собранный из безопасных частей.
// internal/runner уже импортирует internal/render (ради Progress), поэтому
// обратный импорт дал бы цикл — на границу передаются только строки и
// логические флаги, тот же приём, что у Progress. Из доменных типов
// допускается единственный — env.Missing, в котором значению негде
// поместиться по устройству типа.
type BannerInput struct {
	// ImageRef — образ, который реально пошёл в контейнер.
	ImageRef string
	// ImageAssumed — true, если образ подставлен утилитой: в джобе не было
	// image:, и пользователь не задал --image.
	ImageAssumed bool
	// ImageOverridden — образ из конфига джобы, который заменил флаг
	// --image. Пусто, если замены не было.
	ImageOverridden string
	// Missing — маскированные переменные типа hidden, чьё значение не
	// покрыто ни одним слоем (env.Environment.Missing как есть).
	Missing []env.Missing
	// SecretsPath — фактический путь к файлу секретов, где заполняются
	// недостающие значения.
	SecretsPath string
	// FileVars — ключи файловых переменных, переданных содержимым, а не
	// путём к файлу (известное ограничение Фазы 3, закрывается в Фазе 6).
	FileVars []string
}

// Banner печатает баннер допущений — последнее, что видит пользователь
// перед тем, как терминал уйдёт в контейнер: перечень того, чем это
// воспроизведение отличается от настоящего прогона (idea §0). Ни одна
// строка баннера не является причиной не запуститься. Печатается при
// каждом запуске: две последние строки (submodules/LFS и артефакты
// предыдущих стадий) истинны всегда, поэтому пустого баннера не бывает и
// «тихого» расхождения не остаётся. Ошибка записи возвращается вызывающему
// — как в Env и Job, а не глотается, как в Progress.
func Banner(w io.Writer, in BannerInput) error {
	if _, err := fmt.Fprintln(w, "\n⚠ воспроизведение неточное:"); err != nil {
		return err
	}
	for _, line := range bannerLines(in) {
		if _, err := fmt.Fprintf(w, "   • %s\n", line); err != nil {
			return err
		}
	}
	return nil
}

// bannerLines собирает строки баннера в порядке idea §0: образ, недостающие
// секреты, файловые переменные, затем две строки, истинные при любом
// запуске.
func bannerLines(in BannerInput) []string {
	var lines []string

	switch {
	case in.ImageAssumed:
		lines = append(lines, fmt.Sprintf("образ подобран нами: в джобе нет image:, взят %s", in.ImageRef))
	case in.ImageOverridden != "":
		lines = append(lines, fmt.Sprintf("образ заменён флагом --image: %s вместо %s из конфига джобы", in.ImageRef, in.ImageOverridden))
	}

	if len(in.Missing) > 0 {
		keys := make([]string, 0, len(in.Missing))
		for _, m := range in.Missing {
			keys = append(keys, m.Key)
		}
		lines = append(lines, fmt.Sprintf("%d переменных типа hidden пусты: %s", len(in.Missing), strings.Join(keys, ", ")))
		lines = append(lines, fmt.Sprintf("  заполнить один раз: %s", in.SecretsPath))
	}

	if len(in.FileVars) > 0 {
		lines = append(lines, fmt.Sprintf("файловые переменные переданы содержимым, а не путём к файлу: %s", strings.Join(in.FileVars, ", ")))
	}

	return append(lines,
		"submodules и LFS не подтянуты",
		"артефакты предыдущих стадий и кэш ранера не восстановлены",
	)
}
