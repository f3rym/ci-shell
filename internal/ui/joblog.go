// joblog.go — панель лога джобы настоящего прогона (Фаза 10, BROW-04):
// хвост упавшего шага сразу при выборе джобы, полный лог по явной команде,
// маскирование известных значений при записи, память в пределах сессии.
//
// Не панель лога экрана джобы (internal/ui/job.go): там — вывод локального
// воспроизведения из буфера сессии, здесь — лог настоящего прогона,
// полученный от API через клиент обхода. Источники разные, и общих строк у
// них нет намеренно.
//
// С Фазы 15 (план 15-01) панель-предпросмотр под списком джоб и полноэкранный
// просмотрщик (internal/ui/logview.go) — одна и та же реализация: панель
// держит поле lv logView вместо стороннего компонента прокрутки, а Open ниже
// открывает ту же самую память полноэкранным оверлеем. Второй прокрутки в
// проекте не заводится (LOG-01).
package ui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/spinner"

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
// джоба, просмотрщик лога (internal/ui/logview.go) для предпросмотра под
// списком джоб, память логов по идентификатору джобы с порядком вытеснения,
// счётчик поколения запроса, идентификатор джобы, для которой запрос сейчас
// идёт, спиннер, текст ошибки, признак «показан полный лог» и размеры.
type logPanel struct {
	b *browse.Client
	// p и host — источники известных значений секретов для маскировки лога
	// перед показом (buildBrowseMask, internal/ui/mask.go, CR-04 обзора
	// v1.0.0): browse.Client.Log сам по себе секреты не маскирует (только
	// снимает управляющие последовательности терминала), а лог настоящего
	// прогона может нести немаскированное GitLab-ом значение (многострочные
	// секреты runner не маскирует вовсе). p может быть nil (хост, для
	// которого не удалось собрать клиента обхода, — loadErr уже объясняет
	// причину экрану) — buildBrowseMask это учитывает.
	p           provider.Provider
	host        string
	projectPath string

	job    provider.Job
	hasJob bool

	// lv — просмотрщик лога (internal/ui/logview.go), тот же тип, что и у
	// полноэкранного оверлея: панель показывает окно строк и строку
	// положения его же методами view/statusLine — второго механизма
	// прокрутки в проекте не заводится (LOG-01).
	lv   logView
	spin spinner.Model

	memo     map[int64]logEntry
	memoOrd  []int64
	pending  int64
	loading  bool
	loadErr  string
	fullShow bool

	// openAfterLoad — человек попросил открыть полноэкранный просмотрщик
	// (клавиша лога либо команда :log) раньше, чем лог приехал: по приходу
	// ответа с неустаревшим поколением панель дополнительно отдаёт команду
	// открытия и опускает признак — открывать пустой просмотрщик в ответ на
	// «покажи лог» значило бы соврать про содержимое.
	openAfterLoad bool

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

// newLogPanel собирает панель с пустым просмотрщиком; размер — временный,
// первый setSize (ниже) пересчитает его от высоты, оставшейся под списком
// джоб (Фаза 13, план 13-02, jobsModel.setSize) — второго расчёта здесь не
// заводится. Клиент обхода b — тот же, что у списка джоб: второго клиента
// в интерфейсе не появляется. p и host идут отдельно от b (browse.Client их
// не отдаёт наружу) — buildBrowseMask строит по ним маску известных секретов
// для лога (CR-04 обзора v1.0.0).
func newLogPanel(p provider.Provider, b *browse.Client, host, projectPath string) logPanel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return logPanel{
		b: b, p: p, host: host, projectPath: projectPath,
		lv:   newLogView(),
		spin: sp,
		memo: map[int64]logEntry{},
	}
}

// setSize принимает ширину и высоту КОЛОНКИ джоб (Фаза 13, план 13-02), а
// не ширину кадра целиком и не константу темы, задававшую число строк
// раньше (она удалена вместе с отдельной сеткой экрана джоб) — число
// видимых строк лога считает вызывающий (jobsModel.setSize) от высоты,
// оставшейся под списком джоб; здесь остаётся только защита от
// отрицательного или совсем крохотного значения — разумный минимум в две
// строки, ниже которого панель лога перестала бы что-либо показывать.
func (p logPanel) setSize(width, height int) logPanel {
	p.width = width
	if height < 2 {
		height = 2
	}
	p.lv = p.lv.setSize(width, height)
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
		p.lv = p.lv.setSource(false, -1, "").setLines(nil)
		return nil
	}

	if entry, ok := p.memo[job.ID]; ok {
		p.loading = false
		p.fullShow = entry.full
		p.lv = p.viewOf(entry)
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
//
// С Фазы 15 (план 15-01) :log значит не только «обнови панель-предпросмотр
// под списком джоб» — openAfterLoad поднимается безусловно, и панель
// дополнительно откроет полноэкранный просмотрщик, как только лог придёт:
// клавиша лога и команда полного лога обязаны вести в одно и то же
// (idea-0.3.1 §4).
func (p *logPanel) Full(job provider.Job) tea.Cmd {
	if !p.hasJob || p.job.ID != job.ID {
		p.job = job
		p.hasJob = true
	}
	p.generation++
	gen := p.generation
	p.loading = true
	p.loadErr = ""
	p.openAfterLoad = true
	return p.fetchFull(job.ID, gen)
}

// Open — открыть полноэкранный просмотрщик на джобе job (Фаза 15, план
// 15-01, задача 3): если лог этой джобы уже в памяти панели, команда шлёт
// сообщение открытия немедленно, готовыми строками; иначе запускается
// загрузка и признак openAfterLoad поднимается, чтобы панель открыла
// просмотрщик сама по приходу ответа (см. update ниже) — открывать пустой
// просмотрщик в ответ на «покажи лог» значило бы соврать про содержимое.
// Незапавшая джоба без памяти идёт тем же путём, что и команда :log (Full):
// Select для неё вообще не запрашивает лог (D-02), а «покажи лог» для
// незапавшей джобы означает «весь лог целиком», ровно как в applyCommand.
func (p *logPanel) Open(job provider.Job) tea.Cmd {
	if entry, ok := p.memo[job.ID]; ok {
		return openLogCmd(job, entry)
	}
	if job.Status != StatusFailed {
		return p.Full(job)
	}
	p.openAfterLoad = true
	return p.Select(job)
}

// fetchTail запрашивает хвост лога размером provider.DefaultTailBytes —
// умолчание доменных типов (D-02: не тянуть мегабайты без спроса). Строки
// маскируются той же формулой, что и живой вывод сессии, до попадания в
// сообщение (maskLog ниже, CR-04 обзора v1.0.0) — p.job на момент вызова
// уже указывает на джобу jobID: generation-защита в update() отбрасывает
// ответ раньше, чем курсор успевает уйти на другую джобу и переписать p.job.
func (p *logPanel) fetchTail(jobID int64, gen int) tea.Cmd {
	b, projectPath := p.b, p.projectPath
	prov, host, job := p.p, p.host, p.job
	return func() tea.Msg {
		log, err := b.Log(context.Background(), projectPath, jobID, provider.LogOptions{TailBytes: provider.DefaultTailBytes})
		if err != nil {
			return logFailedMsg{jobID: jobID, generation: gen, reason: err.Error()}
		}
		log = maskLog(context.Background(), prov, host, job, log)
		return logLoadedMsg{jobID: jobID, generation: gen, log: log, full: false}
	}
}

// fetchFull запрашивает лог целиком — нулевой размер хвоста. Маскируется
// той же формулой, что и fetchTail выше.
func (p *logPanel) fetchFull(jobID int64, gen int) tea.Cmd {
	b, projectPath := p.b, p.projectPath
	prov, host, job := p.p, p.host, p.job
	return func() tea.Msg {
		log, err := b.Log(context.Background(), projectPath, jobID, provider.LogOptions{TailBytes: 0})
		if err != nil {
			return logFailedMsg{jobID: jobID, generation: gen, reason: err.Error()}
		}
		log = maskLog(context.Background(), prov, host, job, log)
		return logLoadedMsg{jobID: jobID, generation: gen, log: log, full: true}
	}
}

// maskLog заменяет в log.Lines известные значения секретов их отображением
// (buildBrowseMask, internal/ui/mask.go) — browse.Client.Log сам по себе
// маскирует только управляющие последовательности терминала (SafeText в
// internal/provider/gitlab/log.go), не значения переменных: без этого шага
// лог настоящего прогона показывал бы секрет открытым текстом там, где
// GitLab-раннер сам его не замаскировал (многострочные значения он не
// маскирует вовсе) — CR-04 обзора исполняющей части v1.0.0.
func maskLog(ctx context.Context, p provider.Provider, host string, job provider.Job, log provider.Log) provider.Log {
	mask := buildBrowseMask(ctx, p, host, job)
	masked := make([]string, len(log.Lines))
	for i, line := range log.Lines {
		masked[i] = mask.Replace(line)
	}
	log.Lines = masked
	return log
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
		entry := logEntry{log: msg.log, full: msg.full}
		p.remember(msg.jobID, entry)
		if p.hasJob && p.job.ID == msg.jobID {
			p.lv = p.viewOf(entry)
		}
		if p.openAfterLoad {
			p.openAfterLoad = false
			return openLogCmd(p.job, entry)
		}
		return nil
	case logFailedMsg:
		if msg.generation != p.generation {
			return nil
		}
		p.loading = false
		p.loadErr = msg.reason
		p.openAfterLoad = false
		return nil
	}
	// Разбор клавиш здесь больше не ведётся (Фаза 15, план 15-01, задача 3):
	// встроенная в колонку панель — предпросмотр, клавиши забирает
	// полноэкранный просмотрщик (internal/ui/app.go, screenLog) — пока
	// панель ловила их сама, прокрутка спорила бы с движением курсора по
	// списку джоб: в одной колонке два списка, и стрелка не может значить и
	// то и другое.
	return nil
}

// viewOf — просмотрщик предпросмотра, наполненный записью e: источник
// (хвост ли, где упавший шаг, чем дотянуть лог целиком) и строки — тем же
// приёмом, каким наполняется полноэкранный просмотрщик (openLogCmd ниже).
// Размер (p.lv.width/height) не трогается — setSource/setLines его не
// меняют.
func (p logPanel) viewOf(e logEntry) logView {
	pull := ""
	if e.log.Truncated {
		pull = logPullLabel()
	}
	return p.lv.setSource(e.log.Truncated, e.log.SectionLine, pull).setLines(e.log.Lines)
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

// title собирает заголовок панели: с секцией, целиком или без джобы. Имя
// джобы и имя секции идут через очистку: заголовок попадает в кадр минуя
// меру ширины, а имя секции приходит из маркеров лога ранера (CR-06 обзора
// v0.3.0).
func (p logPanel) title() string {
	switch {
	case !p.hasJob:
		return "ЛОГ ДЖОБЫ"
	case p.fullShow:
		return fmt.Sprintf("ЛОГ ДЖОБЫ %s · целиком", Plain(p.job.Name))
	default:
		section := "?"
		if l, ok := p.currentLog(); ok && l.Section != "" {
			section = l.Section
		}
		return fmt.Sprintf("ЛОГ ДЖОБЫ %s · %s", Plain(p.job.Name), Plain(section))
	}
}

// logTitleFor — заголовок полноэкранного просмотрщика: та же формула, что и
// у title() панели выше (имя джобы, секция или «целиком»), но принимает
// джобу и запись явными аргументами, а не читает состояние панели — Open и
// приход ответа (update выше) зовут её независимо от того, на какую джобу
// сейчас смотрит панель-предпросмотр.
func logTitleFor(job provider.Job, e logEntry) string {
	if e.full {
		return fmt.Sprintf("ЛОГ ДЖОБЫ %s · целиком", Plain(job.Name))
	}
	section := "?"
	if e.log.Section != "" {
		section = e.log.Section
	}
	return fmt.Sprintf("ЛОГ ДЖОБЫ %s · %s", Plain(job.Name), Plain(section))
}

// logPullLabel — подпись команды полного лога для строки положения
// просмотрщика (logView.statusLine): имя команды берётся из реестра команд
// (internal/ui/command.go, knownCommands) вместо литерала — второе место
// того же текста в проекте не заводится.
func logPullLabel() string {
	if spec, ok := findCommand("log"); ok {
		return ":" + spec.Name
	}
	return ""
}

// openLogCmd собирает команду открытия полноэкранного просмотрщика
// (internal/ui/logview.go, openLogMsg): заголовок — logTitleFor выше,
// строки, признак урезанности и номер строки секции — прямо из записи,
// подпись дотягивания — logPullLabel, и только когда лог урезан (уже
// целиком дотягивать нечем).
func openLogCmd(job provider.Job, e logEntry) tea.Cmd {
	title := logTitleFor(job, e)
	pull := ""
	if e.log.Truncated {
		pull = logPullLabel()
	}
	return func() tea.Msg {
		return openLogMsg{title: title, lines: e.log.Lines, tail: e.log.Truncated, mark: e.log.SectionLine, pull: pull}
	}
}

// view отрисовывает панель: заголовок, затем одно из четырёх состояний тела
// (пятое, что показывался хвост, теперь называет строка положения
// просмотрщика — она же и печатает то же самое, отдельной приглушённой
// строки под телом больше нет, LOG-04).
func (p logPanel) view(theme Theme) string {
	// И заголовок, и все четыре состояния тела обрезаются по ширине панели
	// (обзор v1.0.0, живой прогон): строка, длиннее отведённого, не
	// «немного вылезала» — Lip Gloss переносил её хвост на следующую
	// строку, кадр раздувался по высоте, а в узкой колонке заголовок
	// разрывался посередине слова. Ширина известна панели (setSize), и
	// обрезать обязана она сама: снаружи, из columnView, длина этих строк
	// уже не видна.
	title := theme.PanelTitle.Render(Fit(p.title(), p.width))

	var body string
	switch {
	case !p.hasJob:
		body = Fit("лог появится, когда вы выберете упавшую джобу", p.width)
	case p.job.Status != StatusFailed:
		body = Fit("эта джоба не падала — :log покажет её лог целиком", p.width)
	case p.loadErr != "":
		body = Fit(fmt.Sprintf("не удалось получить лог: %s", p.loadErr), p.width)
	case p.loading:
		body = Fit(p.spin.View()+" "+"тяну лог…", p.width)
	default:
		body = p.lv.view(theme) + "\n" + p.lv.statusLine(theme)
	}

	return title + "\n" + body
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
