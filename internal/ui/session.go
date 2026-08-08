package ui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/f3rym/ci-shell/internal/cache"
	"github.com/f3rym/ci-shell/internal/config"
	"github.com/f3rym/ci-shell/internal/env"
	"github.com/f3rym/ci-shell/internal/event"
	"github.com/f3rym/ci-shell/internal/joburl"
	"github.com/f3rym/ci-shell/internal/provider"
	"github.com/f3rym/ci-shell/internal/render"
	"github.com/f3rym/ci-shell/internal/repo"
	"github.com/f3rym/ci-shell/internal/runner"
	"github.com/f3rym/ci-shell/internal/token"
)

// Session — сессия воспроизведения упавшей джобы внутри интерфейса. Этот
// файл не реализует воспроизведение заново — он вызывает ровно те же
// функции доменных пакетов в ровно том же порядке, что и обычный режим
// (cmd/ci/main.go: runShell + reproduce), и отличается одним: каждая долгая
// операция уходит в горутину командой Bubble Tea, а её ход виден по
// событиям, а не по печати. Любое расхождение с порядком обычного режима —
// дефект, а не свобода.
type Session struct {
	ref joburl.Ref
	// p — значение интерфейса провайдера: то же самое, что уже построил
	// список джоб (internal/ui/jobs.go) — второго клиента GitLab в проекте
	// не появляется.
	p   provider.Provider
	job provider.Job

	em event.Emitter
	// log — ограниченный буфер вывода шагов и тяги образа, только в памяти;
	// собирается в PrepareCmd сразу после сборки окружения — маскирующие
	// пары нужны ему уже к первой строке вывода.
	log *logBuffer

	mu sync.Mutex
	// cancel отменяет контекст текущей долгой операции. Доступ — под mu:
	// поле пишет горутина команды, а Cancel() читает его из основной
	// горутины Bubble Tea.
	cancel context.CancelFunc

	jobCfg      provider.JobConfig
	img         runner.ImageChoice
	environment env.Environment
	// tok — токен, читается ровно в одном месте всей сессии: при сборке
	// запроса на подготовку кода (repo.Request), где он и нужен. Ни одна
	// модель экрана значения токена не получает.
	tok token.Token

	d *runner.Docker

	code *repo.Checkout
	fv   *runner.FileVars
	spec runner.Spec
	c    *runner.Container

	steps   []runner.Step
	outcome runner.Outcome

	// owner — форма "<uid>:<gid>" для возврата владельца файлам рабочего
	// каталога; пусто, когда uid/gid хоста недоступны.
	owner string
	// ownerRestored — идемпотентность restoreOwner: повторный вызов после
	// успешного возврата ничего не делает (та же причина, что у
	// Container.Remove в internal/runner/docker.go).
	ownerRestored bool
}

// NewSession собирает сессию из того, что уже получено заставкой и списком
// джоб: разобранная ссылка, значение провайдера и метаданные джобы.
// Конструктор ничего не запрашивает и ничего не читает: всё, что требует
// сети или диска, делается не здесь, а командами (PrepareCmd и остальные).
func NewSession(ref joburl.Ref, p provider.Provider, job provider.Job, em event.Emitter) *Session {
	return &Session{ref: ref, p: p, job: job, em: em}
}

// sessionReadyMsg — подготовка закончена успешно: результат прогона шагов и
// список переменных, не попавших в контейнер (имя или значение несовместимы
// с файлом окружения).
type sessionReadyMsg struct {
	outcome runner.Outcome
	skipped []string
}

// sessionFailedMsg — подготовка не удалась; reason — текст исходной ошибки
// домена, сессия не заводит второго перевода ошибок в текст. canceled
// истинно, когда причина отказа — отмена контекста самим человеком
// (Session.Cancel): это осознанное действие, а не поломка, и экран обязан
// показать её без цвета отказа.
type sessionFailedMsg struct {
	reason   string
	canceled bool
}

// shellFinishedMsg — механизм передачи терминала Bubble Tea вернул
// управление. err — НЕ код выхода оболочки пользователя, а признак того, что
// сам запуск docker exec не поднялся (см. ShellCmd).
type shellFinishedMsg struct {
	err error
}

// PrepareCmd — одна команда, повторяющая порядок обычного режима. Она
// состоит из двух участков, и оба берутся у подкоманды воспроизведения
// (cmd/ci/main.go: runShell + reproduce) дословно, без единой собственной
// формулы.
func (s *Session) PrepareCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		cctx, cancel := context.WithCancel(ctx)
		s.setCancel(cancel)
		defer s.clearCancel()

		// fail оборачивает ошибку домена в sessionFailedMsg. Отмена контекста
		// самим человеком (Cancel) различается здесь, а не в job.go: только
		// PrepareCmd знает, какой именно вызов не задался, и только у него
		// есть cctx под рукой.
		fail := func(err error) tea.Msg {
			if cctx.Err() != nil {
				return sessionFailedMsg{reason: "прервано пользователем", canceled: true}
			}
			return sessionFailedMsg{reason: err.Error()}
		}

		// --- Участок первый: контекст джобы (то, что обычный режим делает
		// до вызова воспроизведения). Каждый из четырёх шагов ниже не
		// критичен ровно там, где он не критичен сегодня: пустой коммит
		// джобы, неразобранный конфиг, недоступные переменные и
		// непрочитанный файл секретов дают оговорку в срезе оговорок и
		// работу дальше, а не отказ.
		var notices []string

		var jobCfg provider.JobConfig
		if s.job.CommitSHA == "" {
			notices = append(notices, "у джобы нет коммита — конфиг пайплайна не запрашивается")
		} else {
			cfg, err := s.p.MergedConfig(cctx, s.ref.ProjectPath, s.job.CommitSHA)
			switch {
			case err == nil:
				if jc, ok := cfg.JobByName(s.job.Name); ok {
					jobCfg = jc
				} else if jerr := cfg.JobError(s.job.Name); jerr != nil {
					notices = append(notices, jerr.Error())
				} else {
					notices = append(notices, fmt.Sprintf("джоба %q не найдена в конфиге пайплайна", s.job.Name))
				}
			default:
				notices = append(notices, fmt.Sprintf("конфиг пайплайна не разобран: %s", err.Error()))
			}
		}

		// Флага --image в интерфейсе этого плана нет (подмена образа
		// командой :image — план 09-03), поэтому override пустой: приоритет
		// та же функция, что и в обычном режиме.
		img := runner.ResolveImage(jobCfg.Image, "")

		var varSet provider.VariableSet
		if vs, err := s.p.Variables(cctx, s.job); err != nil {
			notices = append(notices, fmt.Sprintf("переменные проекта и групп не получены: %s", err.Error()))
		} else {
			varSet = vs
		}

		secrets, err := env.LoadSecrets(s.ref.Host, s.ref.ProjectPath)
		if err != nil {
			notices = append(notices, fmt.Sprintf("файл секретов не прочитан: %s", err.Error()))
		}

		e := env.Assemble(env.Input{
			Job:         s.job,
			Host:        s.ref.Host,
			JobConfig:   jobCfg,
			APIVars:     varSet.Variables,
			Notices:     append(append([]string{}, notices...), varSet.Notes...),
			Secrets:     secrets.Values,
			SecretsPath: secrets.Path,
		})

		// Файл секретов интерфейс НЕ создаёт: заготовка файла, которую
		// заводит обычный режим при недостающих значениях, здесь не
		// заводится вовсе — редактирование недостающих значений из
		// интерфейса это GUIDE-03, фаза 11. Панель окружения (job.go) сама
		// называет их число и команду ci secrets по e.Missing.

		// Ограниченный буфер лога собирается здесь, сразу после сборки
		// окружения: маскирующие пары нужны ему уже к первой строке вывода
		// тяги образа, которая пойдёт в него ниже.
		maskFrom, maskTo := buildMask(e)
		s.log = newLogBuffer(logBufferLines, maskFrom, maskTo)

		// Каталог чекаута — та же неинтерактивная функция, что раскрывает
		// настроенный каталог обычному режиму (cache.CheckoutBase): вопрос в
		// терминале посреди альтернативного экрана испортил бы кадр и
		// прочитал бы клавиши, предназначенные интерфейсу, поэтому третий
		// источник обычного режима (prompt) сюда не переезжает — сессия
		// работает без сохранения чекаута.
		settings, _, err := config.Load()
		if err != nil {
			return fail(err)
		}
		base, err := cache.CheckoutBase(settings.CheckoutDir)
		if err != nil {
			return fail(err)
		}

		// --- Участок второй: воспроизведение (то, что обычный режим делает
		// внутри reproduce). Порядок вызовов — дословно тот же.
		d, err := runner.New(s.em, s.log)
		if err != nil {
			return fail(err)
		}
		s.em.Emit(event.PreflightStarted{})
		if err := runner.Preflight(cctx, d, img.Ref); err != nil {
			return fail(err)
		}

		tok, err := token.Resolve(s.ref.Host)
		if err != nil {
			return fail(err)
		}

		code, err := repo.Materialize(cctx, repo.Request{
			Host:        s.ref.Host,
			ProjectPath: s.ref.ProjectPath,
			SHA:         s.job.CommitSHA,
			Ref:         s.job.Ref,
			Tag:         s.job.Tag,
			Base:        base,
			Persist:     false,
			Token:       tok.Secret,
		}, s.em)
		if err != nil {
			return fail(err)
		}

		workDir := e.WorkDir()

		fv, err := runner.MaterializeFileVars(e.FileContents(), workDir+".tmp")
		if err != nil {
			return fail(err)
		}

		if err := d.EnsureImage(cctx, img.Ref); err != nil {
			return fail(err)
		}

		envMap := e.Map(fv.Paths)

		// Собирается в переменную, а не литералом на месте вызова: ту же
		// спецификацию переиспользует чистый прогон (задача 3) — второе
		// построение по памяти разъехалось бы с первым.
		spec := runner.Spec{
			Image:         img.Ref,
			SourceDir:     code.Dir,
			WorkDir:       workDir,
			Env:           envMap,
			JobID:         s.job.ID,
			FileVarsDir:   fv.Dir,
			FileVarsMount: fv.Mount,
		}
		c, skipped, err := d.Start(cctx, spec)
		if err != nil {
			return fail(err)
		}

		if err := c.DetectShell(cctx); err != nil {
			return fail(err)
		}

		steps := runner.Steps(jobCfg)
		var outcome runner.Outcome
		if len(steps) > 0 {
			o, err := runner.RunSteps(cctx, c, steps, s.log, s.log, s.em)
			if err != nil {
				return fail(err)
			}
			outcome = o
		}

		// Кладётся в контейнер до готовности экрана: хост слеп внутри
		// интерактивного exec, поэтому знание об упавшем шаге должно быть
		// уже внутри — тот же порядок, что и в обычном режиме. Неудача не
		// прерывает подготовку, только уходит строкой в лог.
		if err := runner.InstallRetry(cctx, c, steps, outcome.FailedIndex); err != nil {
			s.log.note(fmt.Sprintf("предупреждение: %s — команда retry в контейнере недоступна, упавший шаг придётся перезапускать через s", err))
		}

		uid, gid := os.Getuid(), os.Getgid()
		owner := ""
		if uid >= 0 && gid >= 0 {
			owner = fmt.Sprintf("%d:%d", uid, gid)
		}

		s.jobCfg = jobCfg
		s.img = img
		s.environment = e
		s.tok = tok
		s.d = d
		s.code = code
		s.fv = fv
		s.spec = spec
		s.c = c
		s.steps = steps
		s.outcome = outcome
		s.owner = owner

		return sessionReadyMsg{outcome: outcome, skipped: skipped}
	}
}

// ShellCmd берёт у контейнера собранную команду шелла и возвращает механизм
// передачи терминала Bubble Tea (tea.ExecProcess) с обратным вызовом,
// превращающим ошибку запуска в shellFinishedMsg. Ошибка в обратном вызове —
// НЕ код выхода оболочки пользователя, а признак того, что сам запуск не
// поднялся (контейнер уже снят, демон перезапустился); различение сделано в
// доменном пакете (Container.ShellCommand/Shell) и здесь не переоткрывается.
func (s *Session) ShellCmd(ctx context.Context) tea.Cmd {
	cmd, err := s.c.ShellCommand(ctx)
	if err != nil {
		return func() tea.Msg { return shellFinishedMsg{err: err} }
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return shellFinishedMsg{err: err}
	})
}

// restoreOwner возвращает владельца файлам рабочего каталога через
// Container.Chown — идемпотентно: повторный вызов после успешного возврата
// ничего не делает. Вызывается и из Close, и из CleanCmd (задача 3) перед
// снятием первого контейнера.
func (s *Session) restoreOwner(ctx context.Context) {
	if s.owner == "" || s.ownerRestored || s.c == nil {
		return
	}
	s.ownerRestored = true
	if err := s.c.Chown(ctx, s.owner, s.spec.WorkDir); err != nil {
		s.log.note(fmt.Sprintf("предупреждение: %s — файлы в каталоге кода могли остаться под root", err))
	}
}

// Close — уборка в том же порядке, что и разворачивание отложенных вызовов
// обычного режима: возврат владельца файлов, снятие контейнера, снятие
// материализованных файловых переменных, снятие подготовленного кода. Всё —
// фоновым контекстом: отменённый основной контекст не должен помешать
// уборке.
func (s *Session) Close() {
	ctx := context.Background()
	s.restoreOwner(ctx)
	if s.c != nil {
		if err := s.c.Remove(ctx); err != nil && s.log != nil {
			s.log.note(fmt.Sprintf("предупреждение: %s", err))
		}
	}
	if s.fv != nil {
		if err := s.fv.Remove(); err != nil && s.log != nil {
			s.log.note(fmt.Sprintf("предупреждение: %s", err))
		}
	}
	if s.code != nil {
		if err := s.code.Remove(ctx); err != nil && s.log != nil {
			s.log.note(fmt.Sprintf("предупреждение: %s", err))
		}
	}
}

// Cancel отменяет контекст текущей долгой операции, если он есть. Отмена —
// не ошибка, а осознанное действие человека: вызывающий экран обязан
// показать её без цвета отказа.
func (s *Session) Cancel() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Session) setCancel(cancel context.CancelFunc) {
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
}

func (s *Session) clearCancel() {
	s.mu.Lock()
	s.cancel = nil
	s.mu.Unlock()
}

// Log отдаёт содержимое ограниченного буфера лога целиком. Пусто, пока
// PrepareCmd ещё не собрал окружение (маскирующие пары нужны буферу с
// первой строки).
func (s *Session) Log() []string {
	if s.log == nil {
		return nil
	}
	return s.log.Lines()
}

// logBufferLines — предел ограниченного буфера лога джобы: сколько
// последних строк вывода шагов и тяги образа он хранит.
const logBufferLines = 4000

// minMaskLength — порог длины маскируемого значения: короче него значения в
// список маскирования не попадают — замена односимвольного значения сделала
// бы весь вывод нечитаемым и ничего бы не защитила.
const minMaskLength = 4

// logBuffer — io.Writer поверх ограниченного буфера строк в памяти: реализует
// запись, хранит последние строки до logBufferLines и отдаёт их целиком
// (Lines). Буфер живёт только в памяти и никогда не сохраняется на диск: вывод
// джобы может содержать значение секрета, случайно напечатанное самим шагом
// (эхом команды, отладочной печатью, трассировкой оболочки), и файл на диске
// пережил бы процесс.
type logBuffer struct {
	mu      sync.Mutex
	limit   int
	lines   []string
	partial string
	// replacer маскирует известные значения переменных при записи, до
	// попадания строки в память — построен один раз в newLogBuffer из пар,
	// собранных buildMask.
	replacer *strings.Replacer
}

// newLogBuffer строит буфер с пределом limit и заменами known[i] -> ...
// (значение переменной -> её отображение по единственному решению о показе).
func newLogBuffer(limit int, maskFrom, maskTo []string) *logBuffer {
	lb := &logBuffer{limit: limit}
	if len(maskFrom) > 0 {
		pairs := make([]string, 0, len(maskFrom)*2)
		for i := range maskFrom {
			pairs = append(pairs, maskFrom[i], maskTo[i])
		}
		lb.replacer = strings.NewReplacer(pairs...)
	}
	return lb
}

// buildMask собирает пары «известное значение -> чем его заменить» из
// собранного окружения e: для каждой переменной, чьё отображение по
// render.DisplayValue НЕ совпадает с её значением (то есть решение её
// прячет), в список идёт пара из значения и этого отображения. Второго
// правила сокрытия не заводится: замена берётся у той же функции, что рисует
// панель окружения. Пустые и короче minMaskLength значения не попадают в
// список; пары упорядочены от длинного значения к короткому, чтобы значение,
// целиком входящее в другое, не рвало замену на части.
func buildMask(e env.Environment) (from, to []string) {
	type pair struct{ value, display string }
	var pairs []pair
	for _, v := range e.Vars {
		if len(v.Value) < minMaskLength {
			continue
		}
		display := render.DisplayValue(v)
		if display == v.Value {
			continue
		}
		pairs = append(pairs, pair{value: v.Value, display: display})
	}
	sort.Slice(pairs, func(i, j int) bool { return len(pairs[i].value) > len(pairs[j].value) })

	from = make([]string, len(pairs))
	to = make([]string, len(pairs))
	for i, p := range pairs {
		from[i] = p.value
		to[i] = p.display
	}
	return from, to
}

// Write реализует io.Writer: маскирует известные значения, копит неполные
// строки до перевода строки, хранит последние limit строк.
func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	s := string(p)
	if l.replacer != nil {
		s = l.replacer.Replace(s)
	}
	l.partial += s
	for {
		i := strings.IndexByte(l.partial, '\n')
		if i < 0 {
			break
		}
		l.appendLine(l.partial[:i])
		l.partial = l.partial[i+1:]
	}
	return len(p), nil
}

// note добавляет служебную строку (предупреждение сессии) напрямую в буфер,
// минуя маскирование Write — строка собрана самой сессией, а не пришла из
// вывода контейнера.
func (l *logBuffer) note(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.appendLine(line)
}

// appendLine — общая часть Write/note; вызывающий обязан держать мьютекс.
func (l *logBuffer) appendLine(line string) {
	l.lines = append(l.lines, line)
	if len(l.lines) > l.limit {
		l.lines = l.lines[len(l.lines)-l.limit:]
	}
}

// Lines отдаёт содержимое буфера целиком, копией — вызывающий код не должен
// держать ссылку на внутренний срез мимо мьютекса.
func (l *logBuffer) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.lines))
	copy(out, l.lines)
	return out
}
