package ui

import (
	"context"
	"errors"
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

	// patch/patchStats/patchRoot — перенос правок (задача 2, план 09-03):
	// снятый и проверенный CaptureCmd патч, отчёт по изменённым файлам и
	// корень репозитория человека. ApplyCmd и CommitCmd читают их без
	// второго снятия и без второго разбора — второй сбор мог бы захватить
	// то, что появилось после проверки путей. Синхронизация та же, что и у
	// остального состояния подготовки (jobCfg, code, steps выше): поля
	// пишет горутина команды, а читает только код экрана, уже получивший её
	// сообщение (captureDoneMsg) — доставку сообщения Bubble Tea это
	// happens-before устанавливает само, отдельный мьютекс не нужен.
	patch      repo.Patch
	patchStats []repo.FileStat
	patchRoot  string
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

// runKind — вид прогона шагов джобы (задача 3): перезапуск упавшего шага,
// прогон оставшихся, чистый прогон в свежем контейнере. Один тип, чтобы одно
// сообщение об окончании (runFinishedMsg) обслуживало все три исхода —
// данные у них одинаковые, а следующий шаг человека разный.
type runKind int

const (
	runRetry runKind = iota
	runRest
	runClean
)

// runFinishedMsg — один из трёх прогонов задачи 3 закончился. offset —
// смещение среза шагов относительно полного списка: абсолютный номер
// упавшего шага в outcome уже посчитан с этим смещением (adjustOutcome), а
// не заново самим экраном.
type runFinishedMsg struct {
	kind    runKind
	outcome runner.Outcome
	offset  int
	err     error
}

// adjustOutcome переводит результат прогона среза шагов (индексы которого
// считаются от начала среза) в абсолютный — только FailedIndex сдвигается на
// offset, потому что только он используется как номер шага во внешнем мире;
// нулевой Outcome (срез был пуст) сюда не попадает — вызывающие проверяют
// длину среза заранее.
func adjustOutcome(o runner.Outcome, offset int) runner.Outcome {
	if o.Failed != nil {
		o.FailedIndex += offset
	}
	return o
}

// RetryStepCmd перезапускает ровно упавший шаг (FIXUI-01): прогон шагов по
// срезу из одного элемента, начинающемуся с упавшего — тем же прогоном
// шагов, что и первый проход, с маркерами и точным номером. Команда retry
// внутри контейнера (internal/runner/retry.go) по-прежнему устанавливается
// и нужна человеку, вошедшему в шелл — там хост слеп, и знание об упавшем
// шаге обязано быть внутри контейнера (решение фазы 7). Но интерфейс не
// слеп: ему нужен точный номер шага, на котором всё остановилось, а его
// дают только маркеры прогона шагов — семантика при этом та же самая, шаги
// исполняются против того же грязного состояния, что оставили предыдущие.
func (s *Session) RetryStepCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		if s.outcome.Failed == nil {
			return runFinishedMsg{kind: runRetry, err: errNothingFailed}
		}
		offset := s.outcome.FailedIndex - 1
		slice := s.steps[offset : offset+1]

		cctx, cancel := context.WithCancel(ctx)
		s.setCancel(cancel)
		defer s.clearCancel()

		outcome, err := runner.RunSteps(cctx, s.c, slice, s.log, s.log, s.em)
		if err != nil {
			if cctx.Err() != nil {
				return runFinishedMsg{kind: runRetry, err: context.Canceled}
			}
			return runFinishedMsg{kind: runRetry, err: err}
		}
		s.outcome = adjustOutcome(outcome, offset)
		return runFinishedMsg{kind: runRetry, outcome: s.outcome, offset: offset}
	}
}

// RestCmd прогоняет шаги, идущие после упавшего, до конца (FIXUI-02) — тем
// же прогоном шагов, что и RetryStepCmd, срезом сразу за упавшим шагом.
func (s *Session) RestCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		if s.outcome.Failed == nil {
			return runFinishedMsg{kind: runRest, err: errNothingFailed}
		}
		offset := s.outcome.FailedIndex
		if offset >= len(s.steps) {
			return runFinishedMsg{kind: runRest, err: errNoStepsAfter}
		}
		slice := s.steps[offset:]

		cctx, cancel := context.WithCancel(ctx)
		s.setCancel(cancel)
		defer s.clearCancel()

		outcome, err := runner.RunSteps(cctx, s.c, slice, s.log, s.log, s.em)
		if err != nil {
			if cctx.Err() != nil {
				return runFinishedMsg{kind: runRest, err: context.Canceled}
			}
			return runFinishedMsg{kind: runRest, err: err}
		}
		s.outcome = adjustOutcome(outcome, offset)
		return runFinishedMsg{kind: runRest, outcome: s.outcome, offset: offset}
	}
}

// errNothingFailed/errNoStepsAfter — те же честные формулировки, что печатает
// скрипт retry внутри контейнера (internal/runner/retry.go), для случаев,
// когда перезапускать нечего.
var (
	errNothingFailed = errors.New("перезапускать нечего: ни один шаг не упал")
	errNoStepsAfter  = errors.New("после упавшего шагов нет")
)

// CleanCmd — чистый прогон всех шагов в свежем контейнере (FIXUI-02): сначала
// возврат владельца файлов, затем снятие первого контейнера, и только потом
// чистый прогон по уже собранной спецификации spec. Порядок не переизобретён
// — он взят у обычного режима (cmd/ci/main.go): двух живых контейнеров одной
// джобы быть не должно, и второй не должен драться за смонтированный каталог
// с первым. Отказ снятия — причина не запускать чистый прогон вовсе, а не
// предупреждение. После чистого прогона сессия поднимает контейнер заново
// той же спецификацией, заново пробует оболочку и заново кладёт команду
// перезапуска — человек остаётся в интерфейсе и должен иметь возможность
// продолжить чинить.
func (s *Session) CleanCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		cctx, cancel := context.WithCancel(ctx)
		s.setCancel(cancel)
		defer s.clearCancel()

		if s.owner != "" && !s.ownerRestored {
			s.ownerRestored = true
			if err := s.c.Chown(context.Background(), s.owner, s.spec.WorkDir); err != nil {
				s.log.note(fmt.Sprintf("предупреждение: %s — файлы в каталоге кода могли остаться под root", err))
			}
		}
		if err := s.c.Remove(context.Background()); err != nil {
			return runFinishedMsg{kind: runClean, err: fmt.Errorf("чистый прогон пропущен: старый контейнер ещё жив: %w", err)}
		}

		outcome, err := runner.CleanRun(cctx, s.d, s.spec, s.steps, s.owner, s.log, s.log, s.em)
		if err != nil {
			if cctx.Err() != nil {
				return runFinishedMsg{kind: runClean, err: context.Canceled}
			}
			return runFinishedMsg{kind: runClean, err: err}
		}

		c, _, startErr := s.d.Start(context.Background(), s.spec)
		if startErr != nil {
			return runFinishedMsg{kind: runClean, outcome: outcome, err: startErr}
		}
		if err := c.DetectShell(context.Background()); err != nil {
			return runFinishedMsg{kind: runClean, outcome: outcome, err: err}
		}
		if err := runner.InstallRetry(context.Background(), c, s.steps, outcome.FailedIndex); err != nil {
			s.log.note(fmt.Sprintf("предупреждение: %s — команда retry в контейнере недоступна", err))
		}

		s.ownerRestored = false
		s.c = c
		s.outcome = outcome

		return runFinishedMsg{kind: runClean, outcome: outcome}
	}
}

// captureDoneMsg — патч снят, отчёт по файлам получен и пути проверены:
// экран может показать список и спросить согласие на перенос. ahead —
// расхождение базы (число коммитов от коммита джобы до HEAD человека);
// aheadKnown ложно, когда коммита джобы в этом репозитории нет вовсе —
// базы для трёхстороннего мерджа нет, и перенос не предлагается (apply.go).
type captureDoneMsg struct {
	patch      repo.Patch
	stats      []repo.FileStat
	ahead      int
	aheadKnown bool
}

// captureEmptyMsg — в воспроизведённом чекауте нет правок: переносить
// нечего (repo.ErrNoChanges), панель списка не открывается.
type captureEmptyMsg struct{}

// captureFailedMsg — снятие патча, поиск корня репозитория человека, разбор
// отчёта по патчу или проверка путей отказали; reason — причина исходной
// ошибки домена, второго перевода в текст здесь нет.
type captureFailedMsg struct {
	reason string
}

// CaptureCmd снимает патч из подготовленного воспроизведённого чекаута,
// находит корень репозитория человека, разбирает отчёт по патчу и проверяет
// пути — в точности тот же порядок, что и обычный режим переноса правок
// (cmd/ci/main.go: снятие патча после шелла + подкоманда переноса), всё до
// любого показа списка и до любого вопроса. Патч и проверенный отчёт
// сохраняются в самой сессии: ApplyCmd и CommitCmd читают их без второго
// снятия или второго разбора.
//
// Само снятие патча идёт фоновым контекстом, а не переданным ctx: то же
// решение и по той же причине, что и в обычном режиме — прерывание,
// дошедшее до группы процессов сразу после возврата из шелла, не должно
// оставить человека без того, ради чего он тут сидел. Остальные вызовы ниже
// (поиск корня, разбор отчёта, проверка путей, расхождение базы) — только
// чтение, отменять их нечем и незачем.
func (s *Session) CaptureCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		patch, notes, err := repo.Capture(context.Background(), s.code.Dir, s.ref.Host, s.ref.ProjectPath, s.job.ID, s.job.CommitSHA)
		// Печатается в лог независимо от исхода ниже, тем же порядком, что и
		// в обычном режиме (cmd/ci/main.go): отказ регистрации новых файлов
		// значит, что созданные человеком файлы в патч не попали — и когда
		// патч всё равно сохранён, и когда разницы не нашлось вовсе.
		if notes.UntrackedErr != nil {
			s.log.note(fmt.Sprintf("предупреждение: %s — созданные вами файлы (ещё не отслеживаемые git) в патч не попали", notes.UntrackedErr))
		}
		if err != nil {
			if errors.Is(err, repo.ErrNoChanges) {
				return captureEmptyMsg{}
			}
			return captureFailedMsg{reason: err.Error()}
		}

		root, err := repo.Root(context.Background())
		if err != nil {
			return captureFailedMsg{reason: err.Error()}
		}

		stats, err := repo.PatchStat(context.Background(), root, patch.Path)
		if err != nil {
			return captureFailedMsg{reason: err.Error()}
		}
		if err := repo.CheckPaths(stats); err != nil {
			return captureFailedMsg{reason: err.Error()}
		}

		ahead, aheadKnown := repo.AheadCount(context.Background(), root, patch.SHA)

		s.patch = patch
		s.patchStats = stats
		s.patchRoot = root

		return captureDoneMsg{patch: patch, stats: stats, ahead: ahead, aheadKnown: aheadKnown}
	}
}

// appliedMsg — наложение трёхсторонним мерджем прошло; files — число
// перенесённых файлов, взятое из уже снятого отчёта (ApplyCmd не считает
// заново).
type appliedMsg struct {
	files int
}

// applyFailedMsg — наложение отказало; патч остаётся файлом на диске
// (repo.Apply), его путь уже входит в reason.
type applyFailedMsg struct {
	reason string
}

// ApplyCmd накладывает уже снятый и проверенный патч трёхсторонним мерджем
// — ничего сверх. Патч и корень репозитория берутся из полей сессии,
// заполненных CaptureCmd: второй сбор здесь не заводится.
func (s *Session) ApplyCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		cctx, cancel := context.WithCancel(ctx)
		s.setCancel(cancel)
		defer s.clearCancel()

		if err := repo.Apply(cctx, s.patchRoot, s.patch.Path); err != nil {
			return applyFailedMsg{reason: err.Error()}
		}
		return appliedMsg{files: len(s.patchStats)}
	}
}

// committedMsg — фиксация перенесённого прошла.
type committedMsg struct{}

// commitFailedMsg — фиксация отказала; reason — причина исходной ошибки
// домена.
type commitFailedMsg struct {
	reason string
}

// CommitCmd фиксирует перенесённое сообщением message. Пути берутся из уже
// снятого отчёта патча (поле сессии, заполненное CaptureCmd), а не
// собираются заново — второй сбор мог бы захватить то, что появилось в
// рабочем дереве после проверки путей. Для переименования в список путей
// идут ОБЕ стороны: исходный путь и новый — те же, что видит человек в
// списке (apply.go), и те же, что уже прошли проверку.
func (s *Session) CommitCmd(ctx context.Context, message string) tea.Cmd {
	return func() tea.Msg {
		cctx, cancel := context.WithCancel(ctx)
		s.setCancel(cancel)
		defer s.clearCancel()

		paths := make([]string, 0, len(s.patchStats))
		for _, st := range s.patchStats {
			if st.From != "" {
				paths = append(paths, st.From)
			}
			paths = append(paths, st.Path)
		}

		if err := repo.Commit(cctx, s.patchRoot, message, paths); err != nil {
			return commitFailedMsg{reason: err.Error()}
		}
		return committedMsg{}
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

// execDoneMsg — выполнение произвольной команды в контейнере джобы
// закончилось; code — код выхода команды (T-09-21). Ненулевой код не
// считается поломкой утилиты — человек мог намеренно запустить то, что
// падает.
type execDoneMsg struct {
	code int
}

// ExecCmd выполняет command внутри уже поднятого контейнера ровно тем
// текстом, который набрал человек (T-09-21): строка идёт выполнению
// контейнера единственным параметром — доменный пакет (Container.Exec)
// отправляет её отдельным элементом среза аргументов оболочке внутри
// контейнера, а здесь она не склеивается ни с чем: ни со значением
// переменной окружения, ни с рабочим каталогом, ни с чем-либо ещё.
// Выполнение произвольной команды в контейнере — осознанная возможность
// продукта (idea-0.3.0 §2), а не дыра; дырой она станет ровно в тот момент,
// когда команда начнёт собираться конкатенацией. Оба потока выполнения идут
// в тот же ограниченный буфер лога, что и вывод шагов, — на диск ничего не
// сохраняется.
func (s *Session) ExecCmd(ctx context.Context, command string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(ctx)
		s.setCancel(cancel)
		defer s.clearCancel()

		code, err := s.c.Exec(ctx, command, s.log, s.log)
		if err != nil && ctx.Err() == nil {
			s.log.note("предупреждение: " + err.Error())
		}
		return execDoneMsg{code: code}
	}
}

// imageSwappedMsg — подмена образа прошла: новый контейнер поднят, оболочка
// нащупана, команда retry положена заново. image — новая ссылка на образ.
type imageSwappedMsg struct {
	image string
}

// imageSwapFailedMsg — подмена образа отказала; reason — причина.
type imageSwapFailedMsg struct {
	reason string
}

// SwapImageCmd подменяет образ джобы и перезапускает контейнер — в порядке,
// взятом у обычного режима и у чистого прогона (CleanCmd выше):
//
//  1. выбор образа — та же функция, что и у подготовки сессии и у запуска с
//     флагом переопределения (runner.ResolveImage): решение о том, какой
//     образ считать образом джобы, остаётся в одном месте;
//  2. тяга нового образа — ДО того, как тронут старый контейнер: отказ тяги
//     не должен стоить человеку рабочего контейнера, в котором он сидел;
//  3. возврат владельца файлов, затем снятие старого контейнера — отказ
//     снятия отменяет подмену целиком, а не понижается до предупреждения:
//     двух живых контейнеров одной джобы быть не должно (T-09-27);
//  4. подмена поля образа в уже собранной спецификации и подъём контейнера
//     ею же;
//  5. проба оболочки и установка команды перезапуска в новый контейнер —
//     иначе человек, вошедший в него, потеряет знание об упавшем шаге.
func (s *Session) SwapImageCmd(ctx context.Context, image string) tea.Cmd {
	return func() tea.Msg {
		cctx, cancel := context.WithCancel(ctx)
		s.setCancel(cancel)
		defer s.clearCancel()

		choice := runner.ResolveImage(s.jobCfg.Image, image)

		if err := s.d.EnsureImage(cctx, choice.Ref); err != nil {
			return imageSwapFailedMsg{reason: err.Error()}
		}

		if s.owner != "" && !s.ownerRestored {
			s.ownerRestored = true
			if err := s.c.Chown(context.Background(), s.owner, s.spec.WorkDir); err != nil {
				s.log.note(fmt.Sprintf("предупреждение: %s — файлы в каталоге кода могли остаться под root", err))
			}
		}
		if err := s.c.Remove(context.Background()); err != nil {
			return imageSwapFailedMsg{reason: fmt.Sprintf("подмена образа отменена: старый контейнер ещё жив: %s", err)}
		}

		s.spec.Image = choice.Ref
		c, _, startErr := s.d.Start(context.Background(), s.spec)
		if startErr != nil {
			return imageSwapFailedMsg{reason: startErr.Error()}
		}
		if err := c.DetectShell(context.Background()); err != nil {
			return imageSwapFailedMsg{reason: err.Error()}
		}
		if err := runner.InstallRetry(context.Background(), c, s.steps, s.outcome.FailedIndex); err != nil {
			s.log.note(fmt.Sprintf("предупреждение: %s — команда retry в контейнере недоступна", err))
		}

		s.ownerRestored = false
		s.img = choice
		s.c = c

		return imageSwappedMsg{image: choice.Ref}
	}
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
