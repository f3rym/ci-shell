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

	// browsing, host, user, prov, hostsCount — режим обхода (Фаза 10,
	// BROW-01): пустой ввод на подтверждении включает browsing, host берётся
	// из token.Hosts(), а проверка ответа API вместо метаданных джобы
	// спрашивает CurrentUser и несёт готовое значение интерфейса провайдера
	// дальше в browseMsg — второй раз токен не резолвится.
	browsing   bool
	host       string
	user       provider.User
	prov       provider.Provider
	hostsCount int

	// failedIndex — индекс провалившейся проверки (Фаза 11): заставка
	// повторяет ровно её, а не все три заново, после ответа на экране
	// проводника.
	failedIndex int
	// notice — короткое сообщение после ответа проводника (токен сохранён/
	// не сохранён), показывается под проверками одним кадром до перезапуска.
	notice string

	width  int
	height int
}

// checkResultMsg — результат одной проверки заставки.
type checkResultMsg struct {
	index  int
	ok     bool
	reason string
	// err — сама ошибка домена провалившейся проверки (не только её текст):
	// распознавание ситуации словарём проводника (Фаза 11) работает по
	// errors.Is, а не по тексту, и без самой ошибки заставка умела бы
	// только показать текст, а не открыть экран.
	err error
	// job — метаданные джобы, полученные проверкой ответа API; поле
	// заполнено только для checkAPIIndex в обычном режиме, чтобы список
	// джоб не запрашивал их повторно.
	job provider.Job
	// user, prov — то же самое для режима обхода: CurrentUser и уже
	// созданное значение интерфейса провайдера.
	user provider.User
	prov provider.Provider
}

// jobRefMsg — заставка успешно прошла все три проверки в обычном режиме:
// разобранная ссылка и метаданные открывающей джобы уходят экрану списка
// джоб.
type jobRefMsg struct {
	ref joburl.Ref
	job provider.Job
}

// browseMsg — заставка успешно прошла все три проверки в режиме обхода
// (пустой ввод, Фаза 10, BROW-01): хост, полученный пользователь и уже
// созданное значение интерфейса провайдера уходят экрану дерева групп и
// проектов. Второй раз токен здесь не резолвится и в сообщение не попадает —
// дальше по интерфейсу едет значение, умеющее ходить в API, а не секрет.
type browseMsg struct {
	Host     string
	User     provider.User
	Provider provider.Provider
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
				if raw == "" {
					// Пустой ввод — режим обхода (BROW-01): ссылку
					// вставлять не обязательно. Хост берётся перечислением
					// из пакета токена; пустой список — тот же честный
					// отказ, что и провалившаяся проверка.
					hosts := token.Hosts()
					if len(hosts) == 0 {
						m.checks[checkTokenIndex].Reason = "токен не найден ни для одного хоста"
						return m, nil
					}
					m.browsing = true
					m.host = hosts[0]
					m.hostsCount = len(hosts)
					m.asking = false
					m.checks[checkTokenIndex].State = checkRunning
					return m, m.checkToken()
				}
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
	case tea.PasteMsg:
		// Скобочная вставка включена в Bubble Tea v2 по умолчанию (кадр не
		// выставляет View.DisableBracketedPasteMode), и вставленный текст
		// приходит отдельным сообщением, а НЕ клавишей: без этой ветви
		// вставленная из буфера ссылка не доезжала до поля ввода вовсе.
		// Собственной вставки в значение поля здесь нет — textinput.Model
		// понимает tea.PasteMsg сам (charm.land/bubbles/v2/textinput).
		if m.asking {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		return m, nil
	case checkResultMsg:
		return m.applyCheckResult(msg)
	case guideDoneMsg:
		return m.applyGuideDone(msg)
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
	if msg.prov != nil {
		m.user = msg.user
		m.prov = msg.prov
	}

	if !msg.ok {
		m.checks[msg.index].State = checkFailed
		m.checks[msg.index].Reason = msg.reason
		m.failedIndex = msg.index

		// Провалившаяся проверка ищет ситуацию в единственном словаре
		// распознавания (Фаза 11), а не строит экран на месте.
		ref := m.ref
		if m.browsing {
			ref = joburl.Ref{Host: m.host}
		}
		if g, ok := guideFor(msg.err, whereSplash, ref); ok {
			guideCopy := g
			return m, func() tea.Msg { return guideMsg{Guide: guideCopy} }
		}

		// Ситуация не найдена — прежнее поведение фазы 9: словарь не
		// проглатывает незнакомое, молчаливого «всё хорошо» не появляется.
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
		if m.browsing {
			host, user, prov := m.host, m.user, m.prov
			return m, tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg {
				return browseMsg{Host: host, User: user, Provider: prov}
			})
		}
		ref, job := m.ref, m.job
		return m, tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg {
			return jobRefMsg{ref: ref, job: job}
		})
	}
	return m, nil
}

// applyGuideDone разбирает ответ экрана проводника (Фаза 11): сохранение
// токена идёт функцией фазы 4 (token.Save) — второго места записи токена в
// проекте нет. Успех — подсказка о сохранении с путём, неудача — подсказка
// о том, что токен не сохранён и используется только в этом запуске (то же
// решение, что принял вопрос в терминале). В обоих случаях заставка
// перезапускает ровно ту проверку, которая провалилась.
func (m splashModel) applyGuideDone(msg guideDoneMsg) (splashModel, tea.Cmd) {
	switch msg.Action {
	case actionSaveToken:
		host := m.checkHost()
		if path, err := token.Save(host, msg.Value); err != nil {
			m.notice = HintTokenNotSaved()
		} else {
			m.notice = HintTokenSaved(path)
		}
		return m.retryFailedCheck()
	case actionRetryChecks, actionSaveDataDir:
		return m.retryFailedCheck()
	}
	return m, nil
}

// retryFailedCheck перезапускает ровно ту проверку, которая провалилась, а
// не все три заново.
func (m splashModel) retryFailedCheck() (splashModel, tea.Cmd) {
	switch m.failedIndex {
	case checkTokenIndex:
		m.checks[checkTokenIndex] = splashCheck{State: checkRunning}
		return m, m.checkToken()
	case checkAPIIndex:
		m.checks[checkAPIIndex] = splashCheck{State: checkRunning}
		return m, m.checkAPI()
	case checkDockerIndex:
		m.checks[checkDockerIndex] = splashCheck{State: checkRunning}
		return m, m.checkDocker()
	}
	return m, nil
}

// checkHost — хост текущей проверки: хост режима обхода или хост
// разобранной ссылки, в зависимости от того, каким путём заставка пошла.
func (m splashModel) checkHost() string {
	if m.browsing {
		return m.host
	}
	return m.ref.Host
}

// checkToken резолвит токен для хоста. Успех несёт редактированную
// строковую форму токена (Token.String) — само значение токена в интерфейс
// не попадает никогда (T-09-01).
func (m splashModel) checkToken() tea.Cmd {
	host := m.checkHost()
	return func() tea.Msg {
		tok, err := token.Resolve(host)
		if err != nil {
			return checkResultMsg{index: checkTokenIndex, ok: false, reason: err.Error(), err: err}
		}
		return checkResultMsg{index: checkTokenIndex, ok: true, reason: tok.String()}
	}
}

// checkAPI спрашивает у API, кто мы такие. В обычном режиме — метаданные
// джобы по ссылке (успех кладёт их в результат, чтобы список джоб не
// запрашивал их повторно); в режиме обхода (BROW-01) — CurrentUser, а
// справа от отметки успеха показывается имя пользователя (idea-0.3.0 §1,
// «хост · имя»).
func (m splashModel) checkAPI() tea.Cmd {
	if m.browsing {
		host := m.host
		return func() tea.Msg {
			tok, err := token.Resolve(host)
			if err != nil {
				return checkResultMsg{index: checkAPIIndex, ok: false, reason: err.Error(), err: err}
			}
			p := gitlab.New(host, tok)
			var prov provider.Provider = p
			user, err := prov.CurrentUser(context.Background())
			if err != nil {
				return checkResultMsg{index: checkAPIIndex, ok: false, reason: err.Error(), err: err}
			}
			return checkResultMsg{index: checkAPIIndex, ok: true, reason: user.Name, user: user, prov: prov}
		}
	}
	ref := m.ref
	return func() tea.Msg {
		tok, err := token.Resolve(ref.Host)
		if err != nil {
			return checkResultMsg{index: checkAPIIndex, ok: false, reason: err.Error(), err: err}
		}
		var p provider.Provider = gitlab.New(ref.Host, tok)
		job, err := p.JobByID(context.Background(), ref.ProjectPath, ref.JobID)
		if err != nil {
			return checkResultMsg{index: checkAPIIndex, ok: false, reason: err.Error(), err: err}
		}
		return checkResultMsg{index: checkAPIIndex, ok: true, job: job}
	}
}

// checkDocker создаёт клиент контейнерного CLI и опрашивает демон.
func (m splashModel) checkDocker() tea.Cmd {
	return func() tea.Msg {
		d, err := runner.New(event.Emitter{}, io.Discard)
		if err != nil {
			return checkResultMsg{index: checkDockerIndex, ok: false, reason: err.Error(), err: err}
		}
		if err := d.Ping(context.Background()); err != nil {
			return checkResultMsg{index: checkDockerIndex, ok: false, reason: err.Error(), err: err}
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
		b.WriteString("\n")
		b.WriteString(t.Muted.Render("пустой ввод откроет дерево групп и проектов"))
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

	if m.browsing && m.hostsCount > 1 {
		b.WriteString(t.Muted.Render(fmt.Sprintf("хост %s — первый из файла токенов; вставьте ссылку, чтобы открыть другой", m.host)))
		b.WriteString("\n")
	}

	if m.notice != "" {
		b.WriteString(t.Muted.Render(m.notice))
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
		label = fmt.Sprintf("проверяю токен %s…", m.checkHost())
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
