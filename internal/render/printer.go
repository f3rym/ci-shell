package render

import (
	"fmt"
	"io"
	"sync"

	"github.com/f3rym/ci-shell/internal/event"
	"github.com/f3rym/ci-shell/internal/textwidth"
)

// Printer — единственный печатающий подписчик и единственное место в
// проекте, где событие превращается в текст. Пока он держит формулировки
// дословно, `ci shell` в чужом CI выдаёт ровно то же, что вчера (EVT-02).
//
// Printer ничего не накапливает: ни истории событий, ни счётчиков, ни
// последнего шага. Он получил событие, сложил строку, записал её и забыл.
// Причина не в экономии памяти, а в том, что поле события может однажды
// оказаться носителем чужого секрета, и живущий в процессе буфер продлил бы
// ему жизнь.
type Printer struct {
	w  io.Writer
	mu sync.Mutex
}

// NewPrinter строит Printer поверх w. Нулевой писатель безопасен: Emit при
// нём молча возвращается, ровно как нынешний прогресс с нулевым писателем.
func NewPrinter(w io.Writer) *Printer {
	return &Printer{w: w}
}

// Проверка соответствия интерфейсу event.Sink на этапе компиляции — тот же
// приём, что у соответствия provider.Provider в Фазе 1.
var _ event.Sink = (*Printer)(nil)

// Emit разбирает e переключателем по типу и печатает соответствующую
// строку. Мьютекс берётся на всё время формирования и записи строки — не
// для красоты: вывод шагов приходит из горутины, копирующей стандартный
// вывод дочернего процесса, а этапы — из основной; сегодня их спасает
// только то, что каждая печать — один вызов Fprintf, и это уже гонка,
// которую видно на длинном выводе. Ошибка записи игнорируется: прогресс
// служебный, его сбой не должен ронять воспроизведение — та же политика,
// что у нынешнего прогресса, и она отличается от Job/Env/Banner, где ошибка
// возвращается вызывающему.
func (p *Printer) Emit(e event.Event) {
	if p == nil || p.w == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	switch e := e.(type) {
	case event.PreflightStarted:
		fmt.Fprintf(p.w, "  проверяю Docker и воспроизводимость джобы…\n")
	case event.ImageLocal:
		fmt.Fprintf(p.w, "  образ %s уже есть локально\n", e.Image)
	case event.ImagePulling:
		fmt.Fprintf(p.w, "  тяну образ %s…\n", e.Image)
	case event.ContainerStarting:
		fmt.Fprintf(p.w, "  поднимаю контейнер из образа %s…\n", e.Image)
	case event.ContainerReady:
		fmt.Fprintf(p.w, "  контейнер %s готов, рабочий каталог %s\n", shortID(e.ID), e.WorkDir)
	case event.CodePreparing:
		fmt.Fprintf(p.w, "  готовлю код на коммите %s…\n", shortSHA(e.SHA))
	case event.CodeReady:
		fmt.Fprintf(p.w, "  код на коммите %s: %s\n", shortSHA(e.SHA), e.Dir)
	case event.CommitFetching:
		fmt.Fprintf(p.w, "  тяну коммит %s с %s…\n", shortSHA(e.SHA), e.Host)
	case event.CommitFetched:
		fmt.Fprintf(p.w, "  коммит %s скачан в зеркало\n", shortSHA(e.SHA))
	case event.StepStarted:
		fmt.Fprintf(p.w, "  [%d/%d] %s: %s\n", e.Index, e.Total, e.Section, truncate(e.Command, 60))
	case event.StepsSummary:
		if e.Stopped {
			fmt.Fprintf(p.w, "  выполнено %d из %d шагов, стою перед упавшим\n", e.Executed, e.Total)
		} else {
			fmt.Fprintf(p.w, "  выполнено %d из %d шагов, ни один шаг не упал\n", e.Executed, e.Total)
		}
	case event.CleanRunStarting:
		fmt.Fprintf(p.w, "  поднимаю свежий контейнер из образа %s для чистого прогона…\n", e.Image)
	case event.ContainerRemoveFailed:
		fmt.Fprintf(p.w, "  предупреждение: %s; добейте вручную: docker rm -f %s\n", e.Reason, e.ID)
	case event.OwnerRestoreFailed:
		fmt.Fprintf(p.w, "  предупреждение: %s; файлы в каталоге кода могли остаться под root\n", e.Reason)
	case event.ShellOpening:
		fmt.Fprintf(p.w, "  вы внутри контейнера, рабочий каталог %s; для выхода — exit или Ctrl-D\n", e.WorkDir)
	case event.CleanupStarted:
		fmt.Fprintf(p.w, "  убираю контейнер и временный чекаут…\n")
	case event.StepsFailed:
		switch e.Kind {
		case event.RunFirst:
			fmt.Fprintf(p.w, "шаг %d/%d (%s) упал с кодом %d: %s\nшелл открывается сразу после последнего успешного шага\n"+
				"  retry           — перезапустить этот шаг против того же грязного состояния, что оставили предыдущие шаги\n"+
				"  retry --rest    — прогнать оставшиеся шаги, тоже против грязного состояния\n"+
				"  единственная честная проверка «в CI будет зелено» — прогон начисто в свежем контейнере, который утилита предложит после выхода из шелла\n"+
				"  файлы правятся в своей IDE на хосте: каталог смонтирован, редактор внутри образа не нужен\n",
				e.Index, e.Total, e.Section, e.ExitCode, e.Command)
		case event.RunClean:
			fmt.Fprintf(p.w, "чистый прогон: шаг %d/%d (%s) всё ещё падает с кодом %d — фикс ещё не готов\n",
				e.Index, e.Total, e.Section, e.ExitCode)
		}
	case event.StepsPassed:
		switch e.Kind {
		case event.RunFirst:
			fmt.Fprintf(p.w, "все %d шагов прошли — сбой не воспроизвёлся. Вероятные причины: недостающие маскированные значения (см. предупреждение выше), артефакты предыдущих стадий и кэш ранера, которые v0.1.0 не восстанавливает\n", e.Total)
		case event.RunClean:
			fmt.Fprintf(p.w, "✓ джоба воспроизводится зелёной, можно пушить\n")
		}
	}
}

// truncate обрезает s до n ячеек терминала, добавляя многоточие при
// обрезке. Делегат единственной меры ширины проекта (internal/textwidth,
// Фаза 12, POL-02) — своего обхода по рунам в печатающем подписчике не
// остаётся.
func truncate(s string, n int) string {
	return textwidth.Truncate(s, n)
}

// shortID обрезает id контейнера до двенадцати символов для печати — та же
// длина, что и в internal/runner/docker.go. Копия, а не переезд: у себя в
// пакете runner.shortID обслуживает ещё и тексты ошибок, которые событиями
// не становятся.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// shortSHA обрезает sha до восьми символов для печати — та же длина, что и
// в internal/repo/worktree.go. Копия по той же причине, что у shortID.
func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
