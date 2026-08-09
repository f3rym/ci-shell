// joblog.go — панель лога джобы настоящего прогона (Фаза 10, BROW-04):
// хвост упавшего шага сразу при выборе джобы, полный лог по явной команде,
// маскирование известных значений при записи, память в пределах сессии.
//
// Не панель лога экрана джобы (internal/ui/job.go): там — вывод локального
// воспроизведения из буфера сессии, здесь — лог настоящего прогона,
// полученный от API через клиент обхода. Источники разные, и общих строк у
// них нет намеренно.
package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"

	"github.com/f3rym/ci-shell/internal/browse"
	"github.com/f3rym/ci-shell/internal/provider"
)

// logDebounce — та же задержка, что у правой панели пайплайнов (Фаза 10,
// план 10-02): курсор по списку джоб ходит быстро, и запрос на каждое
// движение — отказ в обслуживании, устроенный самому себе и чужому серверу.
const logDebounce = 250 * time.Millisecond

// logMemoLimit — сколько логов джоб панель держит в памяти одновременно, с
// вытеснением самого давнего. Память ограничена не из экономии, а потому
// что лог может содержать значение секрета, случайно напечатанное самим
// шагом, и держать в процессе сто таких логов — увеличивать поверхность без
// всякой пользы.
const logMemoLimit = 16

// logEntry — один запомненный лог: строки, секция и признак «это полный
// лог, а не хвост».
type logEntry struct {
	log  provider.Log
	full bool
}

// logPanel — панель лога джобы: клиент обхода, путь проекта, показываемая
// джоба, вьюпорт Bubbles для прокрутки, память логов по идентификатору
// джобы с порядком вытеснения, счётчик поколения запроса, идентификатор
// джобы, для которой запрос сейчас идёт, спиннер, текст ошибки, признак
// «показан полный лог» и размеры.
type logPanel struct {
	b           *browse.Client
	projectPath string

	job    provider.Job
	hasJob bool

	viewport viewport.Model
	spin     spinner.Model

	memo     map[int64]logEntry
	memoOrd  []int64
	pending  int64
	loading  bool
	loadErr  string
	fullShow bool

	generation int
	width      int
}

// Сообщения панели лога. Все несут поколение запроса — ответ с устаревшим
// поколением отбрасывается молча: курсор к этому моменту уже ушёл, и
// подставлять человеку лог чужой джобы нельзя.
type logDebounceMsg struct {
	jobID      int64
	generation int
}
type logLoadedMsg struct {
	jobID      int64
	generation int
	log        provider.Log
	full       bool
}
type logFailedMsg struct {
	jobID      int64
	generation int
	reason     string
}

// newLogPanel собирает панель со вьюпортом высотой в строках панели из
// темы. Клиент обхода b — тот же, что у списка джоб: второго клиента в
// интерфейсе не появляется.
func newLogPanel(b *browse.Client, projectPath string) logPanel {
	vp := viewport.New(viewport.WithHeight(LogPanelLines))
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return logPanel{
		b: b, projectPath: projectPath,
		viewport: vp, spin: sp,
		memo: map[int64]logEntry{},
	}
}

func (p logPanel) setSize(width, height int) logPanel {
	p.width = width
	p.viewport.SetWidth(width)
	p.viewport.SetHeight(LogPanelLines)
	return p
}

// Select — курсор списка встал на джобу job. Три ветки, все обязательны:
// джоба не падала — запроса нет вовсе (D-02); лог этой джобы уже в памяти
// панели — показывается из памяти, запроса нет; иначе — дребезг
// (logDebounce), сам запрос уходит только по приходу тика с неустаревшим
// поколением.
func (p *logPanel) Select(job provider.Job) tea.Cmd {
	p.job = job
	p.hasJob = true
	p.fullShow = false
	p.loadErr = ""

	if job.Status != StatusFailed {
		p.loading = false
		p.viewport.SetContent("")
		return nil
	}

	if entry, ok := p.memo[job.ID]; ok {
		p.loading = false
		p.fullShow = entry.full
		p.viewport.SetContent(strings.Join(entry.log.Lines, "\n"))
		return nil
	}

	p.generation++
	gen, jobID := p.generation, job.ID
	p.loading = true
	return tea.Tick(logDebounce, func(time.Time) tea.Msg {
		return logDebounceMsg{jobID: jobID, generation: gen}
	})
}

// Full — явная команда на полный лог (:log): счётчик поколения
// увеличивается, запрос уходит немедленно, без дребезга (человек уже сказал,
// чего хочет), с нулевым размером хвоста — то есть за логом целиком в
// пределах, заданных провайдером. Признак полного лога выставляется по
// приходу ответа, а не по отправке запроса.
func (p *logPanel) Full(job provider.Job) tea.Cmd {
	if !p.hasJob || p.job.ID != job.ID {
		p.job = job
		p.hasJob = true
	}
	p.generation++
	gen := p.generation
	p.loading = true
	p.loadErr = ""
	return p.fetchFull(job.ID, gen)
}

// fetchTail запрашивает хвост лога размером provider.DefaultTailBytes —
// умолчание доменных типов (D-02: не тянуть мегабайты без спроса).
func (p *logPanel) fetchTail(jobID int64, gen int) tea.Cmd {
	b, projectPath := p.b, p.projectPath
	return func() tea.Msg {
		log, err := b.Log(context.Background(), projectPath, jobID, provider.LogOptions{TailBytes: provider.DefaultTailBytes})
		if err != nil {
			return logFailedMsg{jobID: jobID, generation: gen, reason: err.Error()}
		}
		return logLoadedMsg{jobID: jobID, generation: gen, log: log, full: false}
	}
}

// fetchFull запрашивает лог целиком — нулевой размер хвоста.
func (p *logPanel) fetchFull(jobID int64, gen int) tea.Cmd {
	b, projectPath := p.b, p.projectPath
	return func() tea.Msg {
		log, err := b.Log(context.Background(), projectPath, jobID, provider.LogOptions{TailBytes: 0})
		if err != nil {
			return logFailedMsg{jobID: jobID, generation: gen, reason: err.Error()}
		}
		return logLoadedMsg{jobID: jobID, generation: gen, log: log, full: true}
	}
}

func (p *logPanel) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		p.spin, cmd = p.spin.Update(msg)
		return cmd
	case logDebounceMsg:
		if msg.generation != p.generation {
			return nil
		}
		return p.fetchTail(msg.jobID, msg.generation)
	case logLoadedMsg:
		if msg.generation != p.generation {
			return nil
		}
		p.loading = false
		p.fullShow = msg.full
		p.remember(msg.jobID, logEntry{log: msg.log, full: msg.full})
		if p.hasJob && p.job.ID == msg.jobID {
			p.viewport.SetContent(strings.Join(msg.log.Lines, "\n"))
		}
		return nil
	case logFailedMsg:
		if msg.generation != p.generation {
			return nil
		}
		p.loading = false
		p.loadErr = msg.reason
		return nil
	case tea.KeyPressMsg:
		var cmd tea.Cmd
		p.viewport, cmd = p.viewport.Update(msg)
		return cmd
	}
	return nil
}

// remember кладёт запись в память панели с вытеснением самого давнего при
// переполнении logMemoLimit.
func (p *logPanel) remember(jobID int64, e logEntry) {
	if _, ok := p.memo[jobID]; !ok {
		p.memoOrd = append(p.memoOrd, jobID)
		if len(p.memoOrd) > logMemoLimit {
			oldest := p.memoOrd[0]
			p.memoOrd = p.memoOrd[1:]
			delete(p.memo, oldest)
		}
	}
	p.memo[jobID] = e
}

// currentLog отдаёт запомненный лог показанной джобы, если он есть.
func (p logPanel) currentLog() (provider.Log, bool) {
	if !p.hasJob {
		return provider.Log{}, false
	}
	e, ok := p.memo[p.job.ID]
	return e.log, ok
}

// title собирает заголовок панели: с секцией, целиком или без джобы.
func (p logPanel) title() string {
	switch {
	case !p.hasJob:
		return "ЛОГ ДЖОБЫ"
	case p.fullShow:
		return fmt.Sprintf("ЛОГ ДЖОБЫ %s · целиком", p.job.Name)
	default:
		section := "?"
		if l, ok := p.currentLog(); ok && l.Section != "" {
			section = l.Section
		}
		return fmt.Sprintf("ЛОГ ДЖОБЫ %s · %s", p.job.Name, section)
	}
}

// view отрисовывает панель: заголовок, затем одно из пяти состояний тела.
func (p logPanel) view(theme Theme) string {
	title := theme.PanelTitle.Render(p.title())

	var body string
	switch {
	case !p.hasJob:
		body = "лог появится, когда вы выберете упавшую джобу"
	case p.job.Status != StatusFailed:
		body = "эта джоба не падала — :log покажет её лог целиком"
	case p.loadErr != "":
		body = fmt.Sprintf("не удалось получить лог: %s", p.loadErr)
	case p.loading:
		body = p.spin.View() + " " + "тяну лог…"
	default:
		body = p.viewport.View()
	}

	parts := []string{title, body}
	if l, ok := p.currentLog(); ok && l.Truncated && !p.loading && p.loadErr == "" {
		parts = append(parts, theme.Muted.Render("показан хвост — :log вытянет лог целиком"))
	}
	return strings.Join(parts, "\n")
}

// hintText выбирает строку подсказки панели лога ровно по её четырём
// формулировкам.
func (p logPanel) hintText() string {
	switch {
	case p.loadErr != "":
		return HintLogUnavailable(p.loadErr)
	case p.fullShow:
		return HintLogFull()
	case p.loading:
		return HintLogLoading()
	}
	if l, ok := p.currentLog(); ok {
		return HintLogTail(l.Section)
	}
	return HintLogLoading()
}
