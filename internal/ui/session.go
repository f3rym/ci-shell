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
	"github.com/f3rym/ci-shell/internal/editor"
	"github.com/f3rym/ci-shell/internal/env"
	"github.com/f3rym/ci-shell/internal/event"
	"github.com/f3rym/ci-shell/internal/joburl"
	"github.com/f3rym/ci-shell/internal/provider"
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

	// mu защищает ВСЁ состояние подготовки, а не только cancel: поля ниже
	// пишет горутина команды (PrepareCmd и остальные), а Close/Cancel/Ready
	// читают их из горутины Bubble Tea по нажатию клавиши — happens-before
	// доставки сообщения Bubble Tea на этот путь не распространяется, потому
	// что клавиша приходит не по sessionReadyMsg (CR-02 обзора v0.3.0).
	mu sync.Mutex
	// cancel отменяет контекст текущей долгой операции.
	cancel context.CancelFunc
	// closed — сессия уже убрана (Close). Подготовка, добравшаяся до конца
	// после закрытия, снимает созданное сама, а не сохраняет его в полях:
	// убирать их было бы уже некому.
	closed bool

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
	// last — абсолютный номер последнего исполненного шага. Хранится
	// отдельно от «упавшего», потому что после успешного перезапуска шага
	// упавшего шага уже нет, а :rest всё равно обязан знать, откуда
	// продолжать (WR-07 обзора v0.3.0).
	last int

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

	// overrides/editedVars — журнал правок сессии (Фаза 20, VAR-01/VAR-02).
	// overrides — значения обычных (не секретных) переменных, подставляемые
	// верхним слоем в КАЖДУЮ сборку окружения этой сессии (env.Assemble,
	// Input.Overrides) — значит, они переживают и R (следующий прогон retry
	// идёт уже в контейнере, поднятом ПОСЛЕ правки, потому что сама правка
	// вызывает RestartCmd), и :clean (CleanRun использует s.spec, собранный
	// той же PrepareCmd, что уже наложила override, — второй сборки
	// спецификации для чистого прогона в проекте нет). editedVars — имена
	// ВСЕХ переменных, правленных за сессию, и через overrides, и через файл
	// секретов (Фаза 15) — общий список для .edit_vars (VAR-02), секретных и
	// обычных имён вперемешку: файл называет, что менялось, а не как.
	overrides  map[string]string
	editedVars map[string]struct{}

	// imageOverride — образ, заданный экраном проводника (действие
	// actionSetImage, конфиг с extends, Фаза 11, GUIDE-05) до того, как
	// контейнер вообще поднялся. PrepareCmd подставляет его вместо пустой
	// строки при повторной попытке — второй точки выбора образа в
	// интерфейсе не появляется.
	imageOverride string
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
// показать её без цвета отказа. err — сама ошибка домена (не только текст):
// распознавание ситуации словарём проводника (Фаза 11) работает по
// errors.Is, а не по тексту.
type sessionFailedMsg struct {
	reason   string
	canceled bool
	err      error
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
			return sessionFailedMsg{reason: err.Error(), err: err}
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

		// Override — образ, заданный экраном проводника до того, как
		// контейнер вообще поднялся (Фаза 11, GUIDE-05); пусто в первой
		// попытке. Приоритет — та же функция, что и в обычном режиме.
		img := runner.ResolveImage(jobCfg.Image, s.imageOverride)

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

		// Снимок текущих override под s.mu, а не прямое чтение s.overrides под
		// тем же вызовом: горутина команды не должна держать s.mu на всё время
		// сборки (сеть, диск, Docker) — тот же приём, каким уже защищены
		// остальные поля подготовки. Пустая карта (правок ещё не было) —
		// env.Assemble работает с nil/пустой картой без изменений в поведении
		// остальных слоёв.
		s.mu.Lock()
		overrides := make(map[string]string, len(s.overrides))
		for k, v := range s.overrides {
			overrides[k] = v
		}
		s.mu.Unlock()

		e := env.Assemble(env.Input{
			Job:         s.job,
			Host:        s.ref.Host,
			JobConfig:   jobCfg,
			APIVars:     varSet.Variables,
			Notices:     append(append([]string{}, notices...), varSet.Notes...),
			Secrets:     secrets.Values,
			SecretsPath: secrets.Path,
			Overrides:   overrides,
		})

		// Файл секретов интерфейс НЕ создаёт: заготовка файла, которую
		// заводит обычный режим при недостающих значениях, здесь не
		// заводится вовсе — редактирование недостающих значений из
		// интерфейса это GUIDE-03, фаза 11. Панель окружения (job.go) сама
		// называет их число и команду ci secrets по e.Missing.

		// Ограниченный буфер лога собирается здесь, сразу после сборки
		// окружения: маска нужна ему уже к первой строке вывода тяги образа,
		// которая пойдёт в него ниже. buildSecretMask — единственный
		// маскировщик проекта (internal/ui/mask.go, Фаза 10), общий с
		// панелью лога настоящего прогона (internal/ui/joblog.go).
		logBuf := newLogBuffer(logBufferLines, buildSecretMask(e))
		s.setLog(logBuf)

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
		d, err := runner.New(s.em, logBuf)
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
			o, err := runner.RunSteps(cctx, c, steps, stepMarkWriter{logBuf, 0}, logBuf, s.em)
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
			logBuf.note(fmt.Sprintf("предупреждение: %s — команда retry в контейнере недоступна, упавший шаг придётся перезапускать через s", err))
		}

		uid, gid := os.Getuid(), os.Getgid()
		owner := ""
		if uid >= 0 && gid >= 0 {
			owner = fmt.Sprintf("%d:%d", uid, gid)
		}

		// Итоговые поля присваиваются одним блоком под тем же мьютексом,
		// которым их читают Close/Ready/остальные команды. Если сессию уже
		// закрыли, пока шла подготовка, сохранять их нельзя — убирать их было
		// бы некому: созданное снимается прямо здесь (CR-02).
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			bg := context.Background()
			if err := c.Remove(bg); err != nil {
				logBuf.note(fmt.Sprintf("предупреждение: %s", err))
			}
			if err := fv.Remove(); err != nil {
				logBuf.note(fmt.Sprintf("предупреждение: %s", err))
			}
			if err := code.Remove(bg); err != nil {
				logBuf.note(fmt.Sprintf("предупреждение: %s", err))
			}
			return sessionFailedMsg{reason: "прервано пользователем", canceled: true}
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
		s.last = lastExecuted(outcome, 0)
		s.owner = owner
		s.ownerRestored = false
		s.mu.Unlock()

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

// lastExecuted — абсолютный номер последнего исполненного шага среза,
// начинающегося со смещения offset: упавший шаг тоже исполнен, поэтому при
// отказе это его номер, а при успехе — конец среза. Считается по
// НЕсдвинутому результату (до adjustOutcome).
func lastExecuted(o runner.Outcome, offset int) int {
	if o.Failed != nil {
		return offset + o.FailedIndex
	}
	return offset + o.Total
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
		c := s.container()
		if c == nil {
			return runFinishedMsg{kind: runRetry, err: errNotReady}
		}
		if s.outcome.Failed == nil {
			return runFinishedMsg{kind: runRetry, err: errNothingFailed}
		}
		offset := s.outcome.FailedIndex - 1
		slice := s.steps[offset : offset+1]

		lb := s.logBuf()
		// Граница перезапускаемого шага метится СРАЗУ здесь, до строк с
		// cctx/cancel — не дожидаясь, пока RunSteps реально начнёт исполнение
		// в контейнере и маркер долетит до markerScanner. Окно между
		// нажатием R и первым маркером могло бы быть заметным (запуск docker
		// exec, старт оболочки), и всё это время старый сегмент того же шага
		// иначе оставался бы виден (LOG-06). s.outcome.FailedIndex —
		// абсолютный номер перезапускаемого шага, проверка s.outcome.Failed
		// == nil уже прошла строкой выше. Крючок stepBoundary, сработавший
		// чуть позже изнутри RunSteps через обёртку с тем же offset ниже,
		// пометит ТУ ЖЕ границу практически тем же значением ещё раз — это не
		// гонка и не двойная работа: markStep — идемпотентная запись «текущая
		// длина буфера», а не накопление.
		lb.markStep(s.outcome.FailedIndex)

		cctx, cancel := context.WithCancel(ctx)
		s.setCancel(cancel)
		defer s.clearCancel()

		outcome, err := runner.RunSteps(cctx, c, slice, stepMarkWriter{lb, offset}, lb, s.em)
		if err != nil {
			if cctx.Err() != nil {
				return runFinishedMsg{kind: runRetry, err: context.Canceled}
			}
			return runFinishedMsg{kind: runRetry, err: err}
		}
		s.mu.Lock()
		s.last = lastExecuted(outcome, offset)
		s.mu.Unlock()
		s.outcome = adjustOutcome(outcome, offset)
		return runFinishedMsg{kind: runRetry, outcome: s.outcome, offset: offset}
	}
}

// RestCmd прогоняет шаги, идущие после последнего исполненного, до конца
// (FIXUI-02) — тем же прогоном шагов, что и RetryStepCmd. Смещение берётся
// у номера последнего исполненного шага (LastStep), а НЕ у упавшего: после
// успешного перезапуска упавшего шага уже нет, а продолжать с него всё ещё
// надо — иначе петля фикса рвётся ровно там, где подсказка велит нажать
// :rest (WR-07 обзора v0.3.0).
func (s *Session) RestCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		c := s.container()
		if c == nil {
			return runFinishedMsg{kind: runRest, err: errNotReady}
		}
		offset := s.LastStep()
		if offset >= len(s.steps) {
			return runFinishedMsg{kind: runRest, err: errNoStepsAfter}
		}
		slice := s.steps[offset:]

		cctx, cancel := context.WithCancel(ctx)
		s.setCancel(cancel)
		defer s.clearCancel()

		lb := s.logBuf()
		// :rest продвигается по ещё не начинавшимся шагам — крючок
		// stepBoundary расставляет им свежие границы по мере того, как
		// RunSteps реально доходит до каждого маркера; отдельной опережающей
		// пометки, как у R (RetryStepCmd), здесь не требуется.
		outcome, err := runner.RunSteps(cctx, c, slice, stepMarkWriter{lb, offset}, lb, s.em)
		if err != nil {
			if cctx.Err() != nil {
				return runFinishedMsg{kind: runRest, err: context.Canceled}
			}
			return runFinishedMsg{kind: runRest, err: err}
		}
		s.mu.Lock()
		s.last = lastExecuted(outcome, offset)
		s.mu.Unlock()
		s.outcome = adjustOutcome(outcome, offset)
		return runFinishedMsg{kind: runRest, outcome: s.outcome, offset: offset}
	}
}

// errNothingFailed/errNoStepsAfter — те же честные формулировки, что печатает
// скрипт retry внутри контейнера (internal/runner/retry.go), для случаев,
// когда перезапускать нечего.
var (
	errNothingFailed = errors.New("перезапускать нечего: ни один шаг не упал")
	errNoStepsAfter  = errors.New("после последнего исполненного шагов нет")
	// errNotReady — команда пришла в сессию, у которой контейнера ещё (или
	// уже) нет: подготовка сорвалась и экран стоит в phaseBlocked. Экран
	// такие команды не пускает (jobModel.canRun), но экран не единственный
	// вход в сессию, и разыменование nil тут стоило бы всей уборки (CR-03).
	errNotReady = errors.New("контейнер джобы не поднят")
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
		if s.container() == nil {
			return runFinishedMsg{kind: runClean, err: errNotReady}
		}
		cctx, cancel := context.WithCancel(ctx)
		s.setCancel(cancel)
		defer s.clearCancel()

		s.restoreOwner(context.Background())
		if err := s.removeContainer(context.Background()); err != nil {
			return runFinishedMsg{kind: runClean, err: fmt.Errorf("чистый прогон пропущен: старый контейнер ещё жив: %w", err)}
		}

		lb := s.logBuf()
		// :clean, как и :rest, продвигается по шагам без опережающей
		// пометки — свежие границы всех шагов расставляет крючок
		// stepBoundary по мере того, как CleanRun доходит до каждого
		// маркера в новом контейнере.
		outcome, err := runner.CleanRun(cctx, s.d, s.spec, s.steps, s.owner, stepMarkWriter{lb, 0}, lb, s.em)
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
			lb.note(fmt.Sprintf("предупреждение: %s — команда retry в контейнере недоступна", err))
		}

		s.setContainer(c)
		s.mu.Lock()
		s.last = lastExecuted(outcome, 0)
		s.mu.Unlock()
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
// ошибки домена, второго перевода в текст здесь нет. err — сама ошибка:
// сорвавшийся перенос правок ищет ситуацию в словаре распознавания с
// местом «перенос правок» (Фаза 11).
type captureFailedMsg struct {
	reason string
	err    error
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
		codeDir := s.checkoutDir()
		if codeDir == "" {
			return captureFailedMsg{reason: errNotReady.Error(), err: errNotReady}
		}
		patch, notes, err := repo.Capture(context.Background(), codeDir, s.ref.Host, s.ref.ProjectPath, s.job.ID, s.job.CommitSHA)
		// Печатается в лог независимо от исхода ниже, тем же порядком, что и
		// в обычном режиме (cmd/ci/main.go): отказ регистрации новых файлов
		// значит, что созданные человеком файлы в патч не попали — и когда
		// патч всё равно сохранён, и когда разницы не нашлось вовсе.
		if notes.UntrackedErr != nil {
			s.note(fmt.Sprintf("предупреждение: %s — созданные вами файлы (ещё не отслеживаемые git) в патч не попали", notes.UntrackedErr))
		}
		if err != nil {
			if errors.Is(err, repo.ErrNoChanges) {
				return captureEmptyMsg{}
			}
			return captureFailedMsg{reason: err.Error(), err: err}
		}
		// Патч сохранён на диск уже сейчас — запоминается немедленно, а не
		// только вместе с остальным состоянием в конце: отказ переноса вне
		// рабочей копии (ниже) обязан назвать пользователю путь патча,
		// который уже лежит на диске и не потерян (Фаза 11).
		s.patch = patch

		// .edit_vars пишется сразу после патча, тем же вызовом (VAR-02): если
		// за сессию были правки, список их имён едет рядом с патчем работы.
		// Отказ этой второстепенной записи не должен выглядеть как отказ
		// всего переноса правок — патч уже сохранён, поэтому ни при какой
		// ошибке здесь CaptureCmd не возвращает captureFailedMsg (тот же
		// принцип, что уже применён к notes.UntrackedErr выше).
		if names := s.editedVarNames(); len(names) > 0 {
			evPath, evErr := repo.WriteEditVars(s.ref.Host, s.ref.ProjectPath, names)
			if evErr != nil {
				s.note(fmt.Sprintf("предупреждение: список правленных переменных не сохранён: %s", evErr))
			} else if evPath != "" {
				s.note(fmt.Sprintf("список правленных переменных сохранён: %s", evPath))
			}
		}

		root, err := repo.Root(context.Background())
		if err != nil {
			return captureFailedMsg{reason: err.Error(), err: err}
		}

		stats, err := repo.PatchStat(context.Background(), root, patch.Path)
		if err != nil {
			return captureFailedMsg{reason: err.Error(), err: err}
		}
		if err := repo.CheckPaths(stats); err != nil {
			return captureFailedMsg{reason: err.Error(), err: err}
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
	c := s.container()
	if c == nil {
		return func() tea.Msg { return shellFinishedMsg{err: errNotReady} }
	}
	cmd, err := c.ShellCommand(ctx)
	if err != nil {
		return func() tea.Msg { return shellFinishedMsg{err: err} }
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return shellFinishedMsg{err: err}
	})
}

// secretsEditedMsg — редактор файла секретов вернул терминал. Path — путь
// файла секретов, Err — НЕ код выхода редактора, а признак того, что сам
// запуск редактора не поднялся (см. editor.Command) — то же различение, что
// и у shellFinishedMsg.
type secretsEditedMsg struct {
	Path string
	Err  error
}

// EditSecretsCmd открывает файл секретов в редакторе пользователя (Фаза 11,
// GUIDE-03): сначала автосоздание файла секретов с хостом, путём проекта и
// списком недостающих переменных (существующий файл не читается и не
// переписывается — механика фазы 6 переиспользуется как есть), затем
// сборка команды редактора, затем механизм передачи терминала Bubble Tea —
// команда живёт в этом файле, а не рядом с экраном, потому что передача
// терминала в проекте собрана в одном файле. Обратный вызов механизма
// возвращает права файлу к 0600 в первый момент, когда редактор точно
// завершился, и отдаёт secretsEditedMsg.
func (s *Session) EditSecretsCmd(ctx context.Context) tea.Cmd {
	path, _, err := env.EnsureSecretsFile(s.ref.Host, s.ref.ProjectPath, s.environment.Missing)
	if err != nil {
		return func() tea.Msg { return secretsEditedMsg{Err: err} }
	}

	cmd, err := editor.Command(path)
	if err != nil {
		return func() tea.Msg { return secretsEditedMsg{Path: path, Err: err} }
	}

	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if restoreErr := editor.RestorePerms(path); restoreErr != nil && err == nil {
			err = restoreErr
		}
		return secretsEditedMsg{Path: path, Err: err}
	})
}

// RestartCmd отменяет идущую операцию, возвращает владельца файлов, снимает
// текущий контейнер и повторяет подготовку тем же вызовом, что и первый раз:
// переменные попадают в контейнер только при его создании, поэтому
// отредактированные секреты доезжают только через новый контейнер.
// Обслуживает все действия повтора проводника — второго места перезапуска
// подготовки не появляется.
//
// Отмена идёт ПЕРВОЙ строкой (WR-12 обзора v0.3.0): без неё повтор, нажатый
// поверх идущей подготовки или прогона шагов, сносил бы контейнер из-под
// работающего docker exec и запускал бы вторую подготовку поверх первой —
// обе писали бы одни и те же поля сессии.
func (s *Session) RestartCmd(ctx context.Context) tea.Cmd {
	s.Cancel()
	s.restoreOwner(context.Background())
	if err := s.removeContainer(context.Background()); err != nil {
		s.note(fmt.Sprintf("предупреждение: %s", err))
	}
	return s.PrepareCmd(ctx)
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
		c := s.container()
		if c == nil {
			s.note("предупреждение: " + errNotReady.Error())
			return execDoneMsg{code: -1}
		}

		ctx, cancel := context.WithCancel(ctx)
		s.setCancel(cancel)
		defer s.clearCancel()

		lb := s.logBuf()
		code, err := c.Exec(ctx, command, lb, lb)
		if err != nil && ctx.Err() == nil {
			lb.note("предупреждение: " + err.Error())
		}
		return execDoneMsg{code: code}
	}
}

// pullFinishedMsg — обновление рабочей копии (:pull, Фаза 11, GUIDE-04)
// закончилось; Output — первая строка вывода git, Err — причина отказа.
type pullFinishedMsg struct {
	Output string
	Err    error
}

// PullCmd находит корень рабочей копии текущего каталога и обновляет её
// доменной функцией repo.Pull — интерфейс git не запускает сам. Отсутствие
// рабочей копии здесь не выдумывается заново: оно приходит той же
// sentinel-ошибкой, что и всегда, и попадает в словарь распознавания.
func (s *Session) PullCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		root, err := repo.Root(ctx)
		if err != nil {
			return pullFinishedMsg{Err: err}
		}
		out, err := repo.Pull(ctx, root)
		if err != nil {
			return pullFinishedMsg{Err: err}
		}
		return pullFinishedMsg{Output: out}
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
	if s.container() == nil {
		// Контейнера ещё нет — подготовка сорвалась на выборе образа
		// (конфиг с extends, Фаза 11, GUIDE-05): второй точки выбора образа
		// в интерфейсе не появляется, образ только запоминается, и
		// подготовка повторяется тем же вызовом, что и первый раз.
		s.imageOverride = image
		return s.PrepareCmd(ctx)
	}
	return func() tea.Msg {
		if s.container() == nil {
			return imageSwapFailedMsg{reason: errNotReady.Error()}
		}
		cctx, cancel := context.WithCancel(ctx)
		s.setCancel(cancel)
		defer s.clearCancel()

		choice := runner.ResolveImage(s.jobCfg.Image, image)

		if err := s.d.EnsureImage(cctx, choice.Ref); err != nil {
			return imageSwapFailedMsg{reason: err.Error()}
		}

		s.restoreOwner(context.Background())
		if err := s.removeContainer(context.Background()); err != nil {
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
			s.note(fmt.Sprintf("предупреждение: %s — команда retry в контейнере недоступна", err))
		}

		s.setContainer(c)
		s.img = choice

		return imageSwappedMsg{image: choice.Ref}
	}
}

// restoreOwner возвращает владельца файлам рабочего каталога через
// Container.Chown — идемпотентно: повторный вызов после успешного возврата
// ничего не делает. Вызывается и из Close, и из CleanCmd (задача 3) перед
// снятием первого контейнера.
func (s *Session) restoreOwner(ctx context.Context) {
	s.mu.Lock()
	if s.owner == "" || s.ownerRestored || s.c == nil {
		s.mu.Unlock()
		return
	}
	s.ownerRestored = true
	c, owner, workDir := s.c, s.owner, s.spec.WorkDir
	s.mu.Unlock()

	if err := c.Chown(ctx, owner, workDir); err != nil {
		s.note(fmt.Sprintf("предупреждение: %s — файлы в каталоге кода могли остаться под root", err))
	}
}

// Close — уборка в том же порядке, что и разворачивание отложенных вызовов
// обычного режима: отмена идущей операции, возврат владельца файлов, снятие
// контейнера, снятие материализованных файловых переменных, снятие
// подготовленного кода. Всё — фоновым контекстом: отменённый основной
// контекст не должен помешать уборке.
//
// Отмена идёт ПЕРВОЙ (CR-02 обзора v0.3.0): без неё выход во время
// подготовки не останавливал бы её, и она досоздавала бы контейнер, чекаут и
// каталог файловых переменных уже после того, как убирать их стало некому.
// Повторный вызов ничего не делает (флаг closed) — Close зовётся и с экрана,
// и после завершения программы (internal/ui/app.go, Run).
func (s *Session) Close() {
	s.Cancel()

	ctx := context.Background()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()

	s.restoreOwner(ctx)

	s.mu.Lock()
	c, fv, code := s.c, s.fv, s.code
	s.c, s.fv, s.code = nil, nil, nil
	s.mu.Unlock()

	if c != nil {
		if err := c.Remove(ctx); err != nil {
			s.note(fmt.Sprintf("предупреждение: %s", err))
		}
	}
	if fv != nil {
		if err := fv.Remove(); err != nil {
			s.note(fmt.Sprintf("предупреждение: %s", err))
		}
	}
	if code != nil {
		if err := code.Remove(ctx); err != nil {
			s.note(fmt.Sprintf("предупреждение: %s", err))
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

// Ready — единственный предикат готовности сессии в проекте: контейнер
// поднят и код материализован. Экран джобы требует его в canRun, а команды
// сессии проверяют своё же условие сами (errNotReady) — экран не
// единственный вход в сессию (CR-03 обзора v0.3.0).
func (s *Session) Ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c != nil && s.code != nil
}

// LastStep — абсолютный номер последнего исполненного шага (0, пока не
// исполнялось ничего).
func (s *Session) LastStep() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// container отдаёт текущий контейнер под мьютексом; nil означает «подготовка
// сорвалась или контейнер только что снят».
func (s *Session) container() *runner.Container {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c
}

// setContainer заменяет контейнер сессии и сбрасывает признак возврата
// владельца: у нового контейнера файлы ещё не возвращались, а снятому
// контейнеру возвращать уже нечем.
func (s *Session) setContainer(c *runner.Container) {
	s.mu.Lock()
	s.c = c
	s.ownerRestored = false
	s.mu.Unlock()
}

// removeContainer снимает контейнер и обнуляет поле СРАЗУ после успешного
// снятия — состояние сессии обязано быть честным и на пути отказа, иначе
// следующие команды работают против уже снятого контейнера (WR-01 обзора
// v0.3.0). Отсутствие контейнера — не ошибка.
func (s *Session) removeContainer(ctx context.Context) error {
	c := s.container()
	if c == nil {
		return nil
	}
	if err := c.Remove(ctx); err != nil {
		return err
	}
	s.setContainer(nil)
	return nil
}

// checkoutDir — каталог материализованного кода джобы; пусто, пока
// подготовка его не создала.
func (s *Session) checkoutDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.code == nil {
		return ""
	}
	return s.code.Dir
}

// logBuf отдаёт буфер лога под мьютексом; nil, пока PrepareCmd не собрал
// окружение.
func (s *Session) logBuf() *logBuffer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.log
}

// setLog ставит буфер лога — пишет его горутина подготовки, читает горутина
// Bubble Tea (Log ниже).
func (s *Session) setLog(l *logBuffer) {
	s.mu.Lock()
	s.log = l
	s.mu.Unlock()
}

// note — служебная строка в буфер лога, безопасная до того, как буфер
// собран: до сборки окружения писать некуда, и это не повод падать.
func (s *Session) note(line string) {
	if l := s.logBuf(); l != nil {
		l.note(line)
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

// patchPath отдаёт путь уже сохранённого на диск патча — пусто, пока
// CaptureCmd ещё не снял его ни разу.
func (s *Session) patchPath() string {
	return s.patch.Path
}

// recordOverride запоминает новое значение обычной (не секретной)
// переменной key, подставляемое верхним слоем в каждую последующую сборку
// окружения этой сессии (env.Assemble, Input.Overrides), и заносит имя
// переменной в общий журнал правок (recordEdit) — тем же приёмом
// синхронизации, что и остальное состояние подготовки.
func (s *Session) recordOverride(key, value string) {
	s.mu.Lock()
	if s.overrides == nil {
		s.overrides = make(map[string]string)
	}
	s.overrides[key] = value
	s.mu.Unlock()
	s.recordEdit(key)
}

// recordEdit заносит key в общий журнал имён переменных, правленных за
// сессию (editedVars) — источник для будущего .edit_vars (VAR-02). Метод
// отдельный, а не только внутренняя часть recordOverride: правка секрета
// (env.SaveSecret, Фаза 15) ничего не знает о сессии и не может занести
// себя в журнал сама — вызывающий (internal/ui/job.go, план 20-02) зовёт
// recordEdit явно при успешной записи секрета, тем же способом, каким
// recordOverride заносит имя при правке обычной переменной; второго
// журнала для секретных имён не заводится.
func (s *Session) recordEdit(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.editedVars == nil {
		s.editedVars = make(map[string]struct{})
	}
	s.editedVars[key] = struct{}{}
}

// editedVarNames отдаёт имена всех переменных, правленных за сессию,
// отсортированными — тот же порядок сортировки, что примет
// repo.WriteEditVars. Сортировка здесь не обязательна для корректности
// (функция сортирует сама), но избавляет от случайного порядка карты в
// любом другом потребителе, который появится позже.
func (s *Session) editedVarNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.editedVars) == 0 {
		return nil
	}
	names := make([]string, 0, len(s.editedVars))
	for k := range s.editedVars {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Log отдаёт содержимое ограниченного буфера лога целиком. Пусто, пока
// PrepareCmd ещё не собрал окружение (маскирующие пары нужны буферу с
// первой строки).
func (s *Session) Log() []string {
	l := s.logBuf()
	if l == nil {
		return nil
	}
	return l.Lines()
}

// StepLog отдаёт сегмент лога, принадлежащий абсолютному номеру шага джобы
// index — единственная публичная точка чтения лога одного шага (Фаза 15,
// план 15-03, LOG-05/LOG-06). Пусто, пока PrepareCmd ещё не собрал буфер.
func (s *Session) StepLog(index int) (lines []string, tail bool, started bool) {
	l := s.logBuf()
	if l == nil {
		return nil, false, false
	}
	return l.stepLines(index)
}

// logBufferLines — предел ограниченного буфера лога джобы: сколько
// последних строк вывода шагов и тяги образа он хранит.
const logBufferLines = 4000

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
	// mask маскирует известные значения переменных при записи, до попадания
	// строки в память — единственный маскировщик проекта (internal/ui/mask.go,
	// Фаза 10), общий с панелью лога настоящего прогона.
	mask secretMask
	// stepStarts/stepClipped — границы шагов джобы в этом же буфере (Фаза 15,
	// план 15-03, LOG-05/LOG-06). stepStarts[index] — номер строки в l.lines
	// (та же единица измерения, что и у самих строк), с которой начинается
	// вывод АБСОЛЮТНОГО номера шага джобы index, считая от единицы.
	// stepClipped[index] — какие из этих границ уже теряли свои самые ранние
	// строки вытеснением буфера (appendLine).
	//
	// Перезапуск шага (R) переписывает СВОЮ границу новым, более поздним
	// значением — старая граница просто перестаёт упоминаться никаким
	// чтением, и старый вывод того же шага перестаёт быть виден, хотя
	// физически строки ещё могут лежать в буфере до вытеснения.
	stepStarts  map[int]int
	stepClipped map[int]bool
}

// newLogBuffer строит буфер с пределом limit и маской mask.
func newLogBuffer(limit int, mask secretMask) *logBuffer {
	return &logBuffer{limit: limit, mask: mask}
}

// stepMarkWriter — обёртка *logBuffer, переводящая ОТНОСИТЕЛЬНЫЙ номер
// маркера шага (event.StepStarted.Index, считая от начала среза, переданного
// в конкретный вызов RunSteps) в АБСОЛЮТНЫЙ номер шага джобы через offset
// (Фаза 15, план 15-03, LOG-05/LOG-06). *logBuffer встраивается анонимным
// полем — Write и вся остальная поверхность *logBuffer наследуются без
// переопределения.
//
// Обёртка, а не метод прямо на *logBuffer: *logBuffer используется в
// ЧЕТЫРЁХ разных вызовах RunSteps/CleanRun этого файла с РАЗНЫМ offset (0
// для первого прогона и чистого прогона, s.outcome.FailedIndex-1 для
// перезапуска шага, s.LastStep() для догона оставшихся) — сам буфер offset
// не знает и знать не должен, это знание вызывающего кода на каждый
// конкретный запуск.
type stepMarkWriter struct {
	*logBuffer
	offset int
}

// MarkStep реализует runner.stepBoundary — единственное место перевода
// относительного номера маркера в абсолютный номер шага джобы.
func (w stepMarkWriter) MarkStep(relIndex int) {
	w.logBuffer.markStep(w.offset + relIndex)
}

// Write реализует io.Writer: маскирует известные значения, копит неполные
// строки до перевода строки, хранит последние limit строк.
func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Маска применяется к СОБРАННОЙ строке, а не к пришедшему куску
	// (обзор v1.0.0, CR-01). Границы вызовов Write не совпадают с
	// границами строк: тяга образа и `:!` пишут сюда напрямую, минуя
	// построчный разбор, и значение секрета, разрезанное такой границей
	// пополам, ни в одном из двух кусков не совпадало с искомым — обе
	// половины уходили в буфер открытыми, а склеивались уже после
	// маскировки.
	l.partial += string(p)
	for {
		i := strings.IndexByte(l.partial, '\n')
		if i < 0 {
			break
		}
		l.appendLine(l.mask.Replace(l.partial[:i]))
		l.partial = l.partial[i+1:]
	}
	return len(p), nil
}

// note добавляет служебную строку (предупреждение сессии) в буфер. Строка
// собрана самой сессией, но подставленная в неё ошибка — это первая строка
// stderr дочернего процесса (runCaptured/runCmd, internal/runner/docker.go):
// если шаг напечатал значение секрета в stderr и следом упал, оно доехало бы
// до панели лога немаскированным, хотя весь остальной вывод того же процесса
// маскируется. Маскировщик идемпотентен, поэтому лишний проход ничего не
// стоит (WR-02 обзора v0.3.0).
func (l *logBuffer) note(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.appendLine(l.mask.Replace(line))
}

// appendLine — общая часть Write/note; вызывающий обязан держать мьютекс.
// Очистка от управляющих последовательностей идёт здесь, построчно: вывод
// шага рисуется в кадр как есть (internal/ui/job.go, logPanel), а lipgloss
// escape-последовательности не вырезает — `\x1b[2J` из вывода шага стёр бы
// кадр и строку подсказки. Построчно, а не над всем куском, потому что
// Plain заменяет перевод строки пробелом, а разбиение на строки уже сделано
// вызывающим (WR-03 обзора v0.3.0).
//
// Вытеснение (len(l.lines) > l.limit) сдвигает ВСЕ сохранённые границы шагов
// (l.stepStarts) на то же число вытесненных строк — тот же приём, что уже
// применён к SectionLine GitLab-лога (internal/provider/gitlab/log.go,
// cleanTrace, план 15-01). Граница, ушедшая в минус после сдвига,
// прижимается к нулю (часть строк шага в буфере ещё есть — только не с
// самого начала), а не отбрасывается, и соответствующий ключ помечается
// истиной в l.stepClipped. stepClipped — отдельная карта, а не производная
// от нулевой границы: нулевая граница получается и естественным образом
// (шаг стартовал ровно с начала текущего содержимого буфера), и вытеснением
// — различить эти два случая без отдельного признака нечем, а путать «шаг
// начался с начала» с «шаг лишился начала» значило бы врать про хвост там,
// где его нет (тот же принцип, что уже назван в internal/ui/logview.go,
// statusLine).
func (l *logBuffer) appendLine(line string) {
	l.lines = append(l.lines, Plain(line))
	if len(l.lines) > l.limit {
		evicted := len(l.lines) - l.limit
		l.lines = l.lines[evicted:]
		for idx, start := range l.stepStarts {
			start -= evicted
			if start < 0 {
				start = 0
				if l.stepClipped == nil {
					l.stepClipped = make(map[int]bool)
				}
				l.stepClipped[idx] = true
			}
			l.stepStarts[idx] = start
		}
	}
}

// markStep запоминает, что вывод абсолютного номера шага джобы index
// начинается с текущей длины буфера. Два вызывающих места:
//  1. изнутри пакета runner — через структурное соответствие интерфейсу
//     stepBoundary (internal/runner/steps.go), когда обёртка stepMarkWriter
//     ниже передана в RunSteps вместо голого *logBuffer;
//  2. заранее, из RetryStepCmd этого же файла, ДО вызова RunSteps —
//     граница перезапускаемого шага обязана смениться раньше первой строки
//     его нового вывода, иначе между нажатием R и первым маркером на
//     экране ещё оставался бы виден старый сегмент того же шага (LOG-06).
//
// Идемпотентна: повторный вызов с тем же index просто перезаписывает
// границу текущей длиной — не гонка и не двойная работа.
func (l *logBuffer) markStep(index int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stepStarts == nil {
		l.stepStarts = make(map[int]int)
	}
	l.stepStarts[index] = len(l.lines)
	// Свежая граница шага по построению НЕ урезана (Фаза 17, WR-04 обзора
	// v0.3.1): вытеснение (appendLine) может пометить её урезанной заново,
	// если это действительно случится, но перезапуск сам по себе не имеет
	// права оставлять признак урезанности от ПРЕЖНЕГО прогона того же шага —
	// без сброса перезапущенный шаг ложно показывал «показан хвост» с пустой
	// подписью команды дотягивания.
	if l.stepClipped != nil {
		delete(l.stepClipped, index)
	}
}

// stepLines отдаёт сегмент лога, принадлежащий абсолютному номеру шага
// джобы index: строки от его границы (markStep) до начала границы
// следующего шага, если та ЕСТЬ и НЕ МЕНЬШЕ текущей (next >= start), иначе —
// до конца буфера. Условие next >= start, а не просто «следующая граница
// как конец»: после перезапуска шага его новая граница может оказаться
// БОЛЬШЕ, чем всё ещё хранящаяся в карте граница следующего шага, оставшаяся
// от прошлого прогона (тот следующий шаг после этого перезапуска ещё не
// переехал на новую границу) — использование такой устаревшей границы
// обрезало бы свежий вывод перезапущенного шага пустым или укороченным
// срезом; условие next >= start отбрасывает устаревшую границу и честно
// показывает всё до текущего конца буфера.
//
// started=false, если index ещё ни разу не помечался markStep — шаг ещё не
// запускался, показывать нечего, и это не то же самое, что «шаг запускался
// и не вывел ни строки».
func (l *logBuffer) stepLines(index int) (lines []string, tail bool, started bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	start, ok := l.stepStarts[index]
	if !ok {
		return nil, false, false
	}
	end := len(l.lines)
	if next, ok := l.stepStarts[index+1]; ok && next >= start {
		end = next
	}
	if start > end {
		// Защита от любого не предусмотренного здесь состояния: пустой, а не
		// паникующий срез.
		start = end
	}
	out := make([]string, end-start)
	copy(out, l.lines[start:end])
	return out, l.stepClipped[index], true
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
