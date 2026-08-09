// Package provider описывает единственную точку доступа к CI-системе.
//
// Набор методов интерфейса Provider зафиксирован в idea §6; JobByID добавлен
// для пути «пользователь дал ссылку на конкретную джобу». Пакет содержит
// только доменные типы и ошибки — ни транспорта, ни упоминаний конкретной
// CI-системы здесь быть не должно: реализации живут в подпакетах.
package provider

import (
	"context"
	"errors"
	"time"
)

// Provider — единственная точка доступа к CI-системе. Реализации живут в
// подпакетах; интерфейс не должен меняться при добавлении следующих
// провайдеров (idea §6).
type Provider interface {
	// FindFailedJob ищет последнюю упавшую джобу по репозиторию и ref (idea §3.1, запуск без аргументов).
	FindFailedJob(ctx context.Context, repo, ref string) (Job, error)
	// JobByID возвращает метаданные конкретной джобы по пути проекта и её id.
	JobByID(ctx context.Context, projectPath string, jobID int64) (Job, error)
	// MergedConfig возвращает конфигурацию пайплайна на указанном коммите.
	MergedConfig(ctx context.Context, projectPath, sha string) (Config, error)
	// Artifacts возвращает артефакты предыдущих стадий для джобы.
	Artifacts(ctx context.Context, job Job) ([]Artifact, error)
	// Variables возвращает переменные проекта и всех родительских групп,
	// видимые джобе job, а также переменные, переданные при запуске
	// пайплайна (ручной запуск, trigger-токен) — четвёртый источник
	// возвращается тем же набором, отличаясь только Scope.
	//
	// Отклонение от буквальной сигнатуры idea §6 (map[string]string):
	// плоская карта не различает переменную, у которой значение есть, и
	// маскированную, у которой значения не будет никогда — а на этом
	// различии стоит весь ENV-02. VariableSet.Notes несёт честные оговорки
	// о недоступных источниках, которые нельзя вернуть через error, потому
	// что обход остальных источников продолжается. Отклонение безопасно:
	// метод сегодня нигде не вызывается кроме cmd/ci, прежняя заглушка —
	// единственная реализация.
	Variables(ctx context.Context, job Job) (VariableSet, error)
	// CurrentUser возвращает того, чьим токеном обход ходит в API (Фаза 10,
	// BROW-02): нужен для правой части заголовка экрана («хост · имя») и
	// для узла личного пространства в дереве.
	CurrentUser(ctx context.Context) (User, error)
	// Groups возвращает страницу групп: пустой parent — группы верхнего
	// уровня, видимые токену; непустой — подгруппы группы с этим полным
	// путём (BROW-02, D-01).
	Groups(ctx context.Context, parent string, req PageRequest) (Page[Group], error)
	// Projects возвращает страницу проектов пространства имён ns — группы
	// или личного пространства человека (BROW-02, D-01).
	Projects(ctx context.Context, ns Namespace, req PageRequest) (Page[Project], error)
	// ProjectPipelines возвращает страницу пайплайнов проекта, свежие
	// первыми (BROW-02, D-01).
	ProjectPipelines(ctx context.Context, projectPath string, req PageRequest) (Page[Pipeline], error)
	// PipelineJobs возвращает страницу джоб одного пайплайна (TUI-03).
	// Расширение делается с прицелом на второй провайдер, а не «пока для
	// GitLab» (idea-0.3.0 §5) — остальные списки (группы токена, проекты
	// группы, пайплайны проекта, лог джобы) принадлежат Фазе 10 и
	// добавляются туда же, тем же интерфейсом. Метод объявлен планом 09-01
	// одностраничным с комментарием, прямо обещавшим постраничный обход
	// этой фазе (BROW-02) — обещание выполняется здесь: это не новый метод,
	// а обещанное продолжение старого, поэтому сигнатура получает запрос
	// страницы.
	PipelineJobs(ctx context.Context, projectPath string, pipelineID int64, req PageRequest) (Page[Job], error)
	// JobLog возвращает лог джобы — по умолчанию хвостом заданного размера
	// (opts.TailBytes), а не целиком (BROW-04, D-02).
	JobLog(ctx context.Context, projectPath string, jobID int64, opts LogOptions) (Log, error)
}

// Job — метаданные джобы CI, полученные через API провайдера.
type Job struct {
	ID            int64
	Name          string
	Stage         string
	Status        string
	FailureReason string
	Ref           string
	// Tag — true, если пайплайн запущен по тегу, а не по ветке. От этого
	// зависит, восстанавливать ли CI_COMMIT_TAG или CI_COMMIT_BRANCH.
	Tag           bool
	CommitSHA     string
	CommitTitle   string
	WebURL        string
	ProjectPath   string
	ProjectID     int64
	PipelineID    int64
	PipelineIID   int64
	RunnerDesc    string
	StartedAt     time.Time
	FinishedAt    time.Time
}

// Config — конфигурация пайплайна целиком: в Фазе 1 это разобранный сырой
// файл конфига пайплайна, начиная с NEXT-02 — уже смёрженный конфиг.
// Форма для потребителей одна и та же.
type Config struct {
	Jobs map[string]JobConfig
	// Unresolved — джобы, которые без смёрженного конфига не разобрать
	// (extends и подобное): имя → причина. Отказ точечный: соседняя джоба с
	// extends не должна лишать образа и шагов ту, которую запросили.
	Unresolved map[string]error
	Raw        []byte
}

// JobConfig — конфигурация одной джобы: образ, шаги, переменные.
type JobConfig struct {
	Name         string
	Stage        string
	Image        string
	BeforeScript []string
	Script       []string
	Variables    map[string]string
	// Environment — имя окружения джобы (environment: name или короткая
	// форма environment: <имя>), пустое, если у джобы окружения нет. Без
	// него переменные с областью окружения не с чем сверять, и они
	// пропадают из окружения молча (ENV-05).
	Environment string
}

// Artifact — артефакт предыдущей стадии джобы.
type Artifact struct {
	Filename string
	Size     int64
	FileType string
}

// VariableScope — принадлежность переменной: проекту или одной из
// родительских групп.
type VariableScope string

const (
	ScopeProject  VariableScope = "проект"
	ScopeGroup    VariableScope = "группа"
	// ScopePipeline — переменные, переданные при запуске пайплайна: то, что
	// человек ввёл в форме ручного запуска (when: manual) или передал
	// trigger-токеном.
	ScopePipeline VariableScope = "пайплайн"
)

// Variable — переменная проекта или группы, видимая джобе.
type Variable struct {
	Key       string
	Value     string
	Masked    bool
	Protected bool
	// IsFile — переменная файлового типа (variable_type: "file"): Value —
	// это содержимое файла, а в реальной джобе переменная окружения несёт
	// путь к временному файлу с этим содержимым.
	IsFile bool
	// EnvironmentScope — область окружения, которой ограничена переменная
	// ("*" означает «без ограничений»). У переменных пайплайна
	// (ScopePipeline) этого поля в ответе API нет вовсе — пустое значение
	// здесь равнозначно отсутствию ограничения, как и для остальных
	// источников.
	EnvironmentScope string
	Scope            VariableScope
	// Owner — путь проекта или группы, которой принадлежит переменная.
	Owner string
}

// VariableSet — переменные, собранные Provider.Variables.
type VariableSet struct {
	Variables []Variable
	// Notes несёт честные оговорки о недоступных источниках («к
	// переменным группы X нет доступа»), которые нельзя вернуть через
	// error, потому что сбор остальных источников продолжается.
	Notes []string
}

// JobByName ищет джобу в конфиге по имени.
func (c Config) JobByName(name string) (JobConfig, bool) {
	jc, ok := c.Jobs[name]
	return jc, ok
}

// JobError возвращает причину, по которой джоба не была разобрана
// (например, использует extends), либо nil, если такой записи нет.
func (c Config) JobError(name string) error {
	return c.Unresolved[name]
}

// Cursor — непрозрачный маркер следующей страницы обхода списка (Фаза 10,
// BROW-02). Провайдер сам решает, что лежит внутри (номер страницы, ключ
// последней записи, курсор сервера) — потребитель обязан вернуть маркер как
// есть и не собирать следующий адрес сам. Пустое значение означает «страниц
// больше нет». Эта непрозрачность и есть то, что позволит перевести GitLab
// с offset-пагинации на keyset, не тронув ни этот интерфейс, ни
// потребителей.
type Cursor string

// PageRequest — запрос одной страницы списка. Пустой Cursor — первая
// страница, нулевой Size — умолчание провайдера.
type PageRequest struct {
	Cursor Cursor
	Size   int
}

// Page — одна страница ответа. Параметризованный тип вместо четырёх почти
// одинаковых GroupPage/ProjectPage/PipelinePage/JobPage: четыре копии одной
// и той же формы разъехались бы при первом добавленном поле — тот же довод,
// которым в проекте уже обоснованы общий toJob (план 09-01) и общий
// fetchVariables (Фаза 6).
type Page[T any] struct {
	Items []T
	Next  Cursor
}

// User — тот, чьим токеном обход ходит в API. Нужен для правой части
// заголовка экрана («хост · имя», idea-0.3.0 §1) и для узла личного
// пространства в дереве.
type User struct {
	Username string
	Name     string
}

// Group — группа GitLab (или её аналог у второго провайдера). FullPath —
// полный путь с предками, и есть то, чем группа адресуется в запросах.
type Group struct {
	ID       int64
	Name     string
	Path     string
	FullPath string
	ParentID int64
}

// Project — проект, видимый обходу.
type Project struct {
	ID            int64
	Name          string
	Path          string
	FullPath      string
	Namespace     string
	DefaultBranch string
	LastActivity  time.Time
}

// Pipeline — пайплайн проекта. Status — тем же словом, что отдаёт API:
// статус-слово в этом проекте не переводится (09-UI-SPEC.md, «Символы
// состояния»).
type Pipeline struct {
	ID        int64
	IID       int64
	Ref       string
	SHA       string
	Status    string
	Source    string
	WebURL    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NamespaceKind различает, где живёт проект: у GitLab это группа или личное
// пространство человека — два разных адреса; у второго провайдера —
// организация и пользователь. Один метод с этим различением честнее двух
// методов, отличающихся одним словом в адресе.
type NamespaceKind string

const (
	NamespaceGroup NamespaceKind = "группа"
	NamespaceUser  NamespaceKind = "пользователь"
)

// Namespace — пространство имён, в котором обходятся проекты.
type Namespace struct {
	Kind NamespaceKind
	Path string
}

// Log — лог джобы, уже разобранный: строки без управляющих
// последовательностей, имя последней открытой секции (тот самый «упавший
// шаг») и признак того, что показан не весь лог.
type Log struct {
	Lines     []string
	Section   string
	Truncated bool
}

// LogOptions — сколько лога запросить. TailBytes == 0 означает «весь лог в
// пределах провайдера».
type LogOptions struct {
	TailBytes int
}

// DefaultTailBytes — умолчание для хвоста лога (D-02: не тянуть мегабайты
// без спроса), 64 КиБ.
const DefaultTailBytes = 64 * 1024

// Sentinel-ошибки провайдеров. Конкретные реализации оборачивают их через
// fmt.Errorf("...: %w", ErrX), чтобы вызывающий код мог проверять причину
// через errors.Is, не завися от конкретного провайдера.
var (
	ErrNotSupported   = errors.New("метод не поддерживается этим провайдером")
	ErrJobNotFound    = errors.New("джоба не найдена")
	ErrConfigNotFound = errors.New("конфигурация пайплайна не найдена")
	ErrUnauthorized   = errors.New("нет доступа: токен отклонён")
	ErrUnresolvedRefs = errors.New("конфиг использует include/extends/!reference")
	// ErrGroupNotFound, ErrProjectNotFound, ErrPipelineNotFound — Фаза 10
	// (BROW-02): списки групп, проектов и пайплайнов получают собственные
	// sentinel-ошибки «не найдено», тем же приёмом, что уже применён к
	// джобе и конфигу.
	ErrGroupNotFound    = errors.New("группа не найдена")
	ErrProjectNotFound  = errors.New("проект не найден")
	ErrPipelineNotFound = errors.New("пайплайн не найден")
	// ErrLogUnavailable — у джобы может не быть лога вовсе (не запускалась,
	// лог протух по политике хранения) — это не поломка, а нормальная
	// ветка.
	ErrLogUnavailable = errors.New("лог джобы недоступен")
	// ErrBadPageLink — не «не найдено», а «сервер прислал ссылку на
	// следующую страницу, которой нельзя доверять». Такой ответ обрывает
	// обход ошибкой, а не молча заканчивает список: молчаливый конец списка
	// неотличим от настоящего конца и был бы враньём про полноту (T-10-01).
	ErrBadPageLink = errors.New("ссылка на следующую страницу указывает не туда, куда можно доверять")
)
