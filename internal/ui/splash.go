package ui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"

	"github.com/f3rym/ci-shell/internal/event"
	"github.com/f3rym/ci-shell/internal/joburl"
	"github.com/f3rym/ci-shell/internal/provider"
	"github.com/f3rym/ci-shell/internal/provider/gitlab"
	"github.com/f3rym/ci-shell/internal/repo"
	"github.com/f3rym/ci-shell/internal/runner"
	"github.com/f3rym/ci-shell/internal/token"
)

// logoLines — рисунок логотипа заставки (idea-0.3.0 §1) плюс строка со
// словом "c i - s h e l l" под ним. Единственное место контракта, где
// допущена вольность в рисунке, и единственное место пакета, где разрешены
// символы за пределами замкнутого набора (09-UI-SPEC.md).
var logoLines = []string{
	"                        ██████ ██████",
	"                        ██        ██",
	"                        ████    ████",
	"                        ██        ██",
	"                        ██        ██",
	"",
	"                         c i - s h e l l",
}

// checkState — состояние одной проверки заставки.
type checkState int

const (
	checkWaiting checkState = iota
	checkRunning
	checkPassed
	checkFailed
)

// Индексы проверок — именно в этом порядке, как в контракте: без хоста
// нечего проверять у токена и не к чему обращаться API.
const (
	checkTokenIndex = iota
	checkAPIIndex
	checkDockerIndex
)

// splashCheck — подпись, состояние и однострочная причина отказа одной
// проверки.
type splashCheck struct {
	State  checkState
	Reason string
}

// splashModel — заставка (TUI-02): три проверки фиксированным массивом,
// спиннер, поле ввода ссылки, разобранная ссылка, признак «спрашиваем
// ссылку» и текст фатального отказа.
type splashModel struct {
	checks [3]splashCheck
	spin   spinner.Model
	input  textinput.Model

	ref    joburl.Ref
	asking bool
	fatal  string

	job provider.Job

	width  int
	height int
}

// checkResultMsg — результат одной проверки заставки.
type checkResultMsg struct {
	index  int
	ok     bool
	reason string
	// job — метаданные джобы, полученные проверкой ответа API; поле
	// заполнено только для checkAPIIndex, чтобы список джоб не запрашивал
	// их повторно.
	job provider.Job
}

// jobRefMsg — заставка успешно прошла все три проверки: разобранная ссылка
// и метаданные открывающей джобы уходят экрану списка джоб.
type jobRefMsg struct {
	ref joburl.Ref
	job provider.Job
}

func newSplashModel() splashModel {
	ti := textinput.New()
	// Плейсхолдер без схемы намеренно: собственного разбора ссылки в
	// интерфейсе нет ни строчки (T-09-02), и пример ввода не должен
	// намекать на то, что схему здесь разбирают заново.
	ti.Placeholder = "gitlab.com/group/project/-/jobs/12345 или #12345"
	ti.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	return splashModel{
		asking: true,
		input:  ti,
		spin:   sp,
	}
}

// init запускает мигание курсора поля ввода и тик спиннера заставки.
func (m splashModel) init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.spin.Tick)
}

// update обрабатывает сообщения экрана заставки. Подтверждение ввода идёт
// ровно теми же функциями разбора, что и аргумент подкоманды
// воспроизведения (runShell) — собственного разбора схемы и пути в
// интерфейсе нет.
func (m splashModel) update(msg tea.Msg) (splashModel, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		if m.asking {
			if msg.String() == "enter" {
				raw := strings.TrimSpace(m.input.Value())
				ref, err := parseRef(context.Background(), raw)
				if err != nil {
					// Неудача разбора — не фатальный отказ, а сообщение
					// под полем ввода и возможность ввести заново.
					m.checks[checkTokenIndex].Reason = err.Error()
					return m, nil
				}
				m.ref = ref
				m.asking = false
				m.checks[checkTokenIndex].State = checkRunning
				return m, m.checkToken()
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		if m.fatal != "" && key.Matches(msg, DefaultKeys().Quit) {
			return m, tea.Quit
		}
		return m, nil
	case checkResultMsg:
		return m.applyCheckResult(msg)
	}
	return m, nil
}

// applyCheckResult разбирает результат одной проверки: провал —
// однострочная причина и честный фатальный отказ под проверками; успех —
// следующая проверка своей командой, а после третьей — пауза-тик и
// автопереход без ожидания клавиши.
func (m splashModel) applyCheckResult(msg checkResultMsg) (splashModel, tea.Cmd) {
	if msg.job.ID != 0 {
		m.job = msg.job
	}

	if !msg.ok {
		m.checks[msg.index].State = checkFailed
		m.checks[msg.index].Reason = msg.reason
		m.fatal = fmt.Sprintf("не удалось продолжить: %s — q для выхода", msg.reason)
		return m, nil
	}

	m.checks[msg.index].State = checkPassed
	m.checks[msg.index].Reason = msg.reason

	switch msg.index {
	case checkTokenIndex:
		m.checks[checkAPIIndex].State = checkRunning
		return m, m.checkAPI()
	case checkAPIIndex:
		m.checks[checkDockerIndex].State = checkRunning
		return m, m.checkDocker()
	case checkDockerIndex:
		ref, job := m.ref, m.job
		return m, tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg {
			return jobRefMsg{ref: ref, job: job}
		})
	}
	return m, nil
}

// checkToken резолвит токен для хоста ссылки. Успех несёт редактированную
// строковую форму токена (Token.String) — само значение токена в интерфейс
// не попадает никогда (T-09-01).
func (m splashModel) checkToken() tea.Cmd {
	host := m.ref.Host
	return func() tea.Msg {
		tok, err := token.Resolve(host)
		if err != nil {
			return checkResultMsg{index: checkTokenIndex, ok: false, reason: err.Error()}
		}
		return checkResultMsg{index: checkTokenIndex, ok: true, reason: tok.String()}
	}
}

// checkAPI запрашивает метаданные джобы по ссылке — успех кладёт их в
// результат, чтобы список джоб не запрашивал их повторно.
func (m splashModel) checkAPI() tea.Cmd {
	ref := m.ref
	return func() tea.Msg {
		tok, err := token.Resolve(ref.Host)
		if err != nil {
			return checkResultMsg{index: checkAPIIndex, ok: false, reason: err.Error()}
		}
		var p provider.Provider = gitlab.New(ref.Host, tok)
		job, err := p.JobByID(context.Background(), ref.ProjectPath, ref.JobID)
		if err != nil {
			return checkResultMsg{index: checkAPIIndex, ok: false, reason: err.Error()}
		}
		return checkResultMsg{index: checkAPIIndex, ok: true, job: job}
	}
}

// checkDocker создаёт клиент контейнерного CLI и опрашивает демон.
func (m splashModel) checkDocker() tea.Cmd {
	return func() tea.Msg {
		d, err := runner.New(event.Emitter{}, io.Discard)
		if err != nil {
			return checkResultMsg{index: checkDockerIndex, ok: false, reason: err.Error()}
		}
		if err := d.Ping(context.Background()); err != nil {
			return checkResultMsg{index: checkDockerIndex, ok: false, reason: err.Error()}
		}
		return checkResultMsg{index: checkDockerIndex, ok: true}
	}
}

// parseRef разбирает введённую строку ровно теми же функциями, что и
// аргумент подкоманды воспроизведения: сначала попытка распознать номер —
// тогда хост с проектом берутся из удалённого репозитория текущего
// каталога, иначе разбор полной ссылки. Собственного разбора ссылки в
// интерфейсе нет ни строчки.
func parseRef(ctx context.Context, raw string) (joburl.Ref, error) {
	if jobID, isNumber := joburl.JobNumber(raw); isNumber {
		root, err := repo.Root(ctx)
		if err != nil {
			return joburl.Ref{}, err
		}
		host, projectPath, err := repo.OriginRef(ctx, root)
		if err != nil {
			return joburl.Ref{}, err
		}
		return joburl.FromParts(host, projectPath, jobID), nil
	}
	return joburl.Parse(raw)
}

// view отрисовывает заставку: логотип, затем либо поле ввода ссылки, либо
// три проверки и честный отказ при неудаче любой из них.
func (m splashModel) view(t Theme) string {
	var b strings.Builder
	for _, line := range logoLines {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if m.asking {
		b.WriteString(t.Text.Render("ссылка на джобу или её номер:"))
		b.WriteString("\n")
		b.WriteString(m.input.View())
		b.WriteString("\n")
		b.WriteString(t.Muted.Render("номер работает из каталога репозитория проекта"))
		if reason := m.checks[checkTokenIndex].Reason; reason != "" && m.checks[checkTokenIndex].State == checkWaiting {
			b.WriteString("\n")
			b.WriteString(t.Danger.Render(reason))
		}
		return b.String()
	}

	for i, c := range m.checks {
		b.WriteString(m.renderCheck(t, i, c))
		b.WriteString("\n")
	}

	if m.fatal != "" {
		b.WriteString("\n")
		b.WriteString(t.Danger.Render(m.fatal))
	}

	return b.String()
}

// renderCheck отрисовывает один пункт проверки: в ожидании — без символа,
// идущий — спиннер перед подписью, успешный — GlyphOK обычным текстом,
// провалившийся — GlyphFail цветом отказа и однострочная причина справа от
// подписи.
func (m splashModel) renderCheck(t Theme, i int, c splashCheck) string {
	var label string
	switch i {
	case checkTokenIndex:
		label = fmt.Sprintf("проверяю токен %s…", m.ref.Host)
	case checkAPIIndex:
		label = "проверяю ответ API…"
	case checkDockerIndex:
		label = "проверяю Docker…"
	}

	switch c.State {
	case checkRunning:
		return m.spin.View() + " " + t.Text.Render(label)
	case checkPassed:
		line := t.Text.Render(GlyphOK) + " " + t.Text.Render(label)
		if c.Reason != "" {
			line += " " + t.Muted.Render(c.Reason)
		}
		return line
	case checkFailed:
		return t.Danger.Render(GlyphFail) + " " + t.Text.Render(label) + "  " + t.Danger.Render(c.Reason)
	default:
		return "  " + t.Muted.Render(label)
	}
}
