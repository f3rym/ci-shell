// Package env собирает доменную модель окружения упавшей джобы:
// предопределённые CI_* переменные и переменные конфигурации пайплайна,
// слитые в единый список с указанием источника каждой переменной.
package env

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/f3rym/ci-shell/internal/provider"
)

// Source — источник переменной окружения. Значения — русский текст,
// который печатается пользователю как есть.
type Source string

const (
	SourcePredefined Source = "предопределённая"
	SourceConfig     Source = "конфиг пайплайна"
	SourceGroup      Source = "группа"
	SourceProject    Source = "проект"
	// SourcePipeline — переменная, переданная при запуске пайплайна: то,
	// что человек ввёл в форме ручного запуска (when: manual) или передал
	// trigger-токеном.
	SourcePipeline Source = "запуск пайплайна"
	// SourceLocal — значение из локального файла секретов пользователя
	// (internal/env/secrets.go). Верхний слой приоритета в Assemble: файл
	// существует именно потому, что API значение не отдал.
	SourceLocal Source = "локальный файл"
)

// Kind — тип переменной GitLab (variable_type). Пустое значение
// равнозначно KindEnv: обычная переменная окружения.
type Kind string

const (
	// KindEnv — обычная переменная окружения (variable_type: env_var).
	KindEnv Kind = "env_var"
	// KindFile — файловая переменная (variable_type: file): Value — это
	// содержимое файла, а в реальной джобе переменная несёт путь к
	// временному файлу с этим содержимым. В v0.1.0 содержимое не
	// печатается инлайн (render показывает <file variable>);
	// материализация во временный файл перед docker run — задача Фазы 3
	// (WR-05).
	KindFile Kind = "file"
)

// Variable — одна переменная собранного окружения.
type Variable struct {
	Key    string
	Value  string
	Source Source
	// Origin уточняет источник: путь группы или проекта. Пусто для
	// предопределённых переменных и переменных конфига пайплайна.
	Origin string
	// Secret — признак «значение нельзя печатать». Заводится сразу: на нём
	// держится вся политика вывода Фазы 2 (render.displayValue).
	Secret bool
	// Kind — тип переменной (env_var или file). Пусто для предопределённых
	// переменных и переменных конфига пайплайна — они всегда обычные.
	Kind Kind
}

// Environment — собранное окружение упавшей джобы.
type Environment struct {
	Vars []Variable
	// Host и ProjectPath скопированы из Input как есть — для ключа проекта
	// в отчёте о недостающих переменных. Ключ нельзя выводить из собранных
	// переменных (CI_SERVER_HOST/CI_PROJECT_PATH): их может перекрыть
	// секрет из локального файла или маскированная переменная API — тогда
	// в отчёт утёк бы секрет или пустая строка (WR-01).
	Host        string
	ProjectPath string
	// Notices — честные оговорки: недоступные источники переменных
	// (из provider.VariableSet.Notes) и переменные, пропущенные из-за
	// EnvironmentScope, отличного от "*".
	Notices []string
	// SecretsPath — фактический путь к файлу секретов (из Secrets.Path),
	// скопированный как есть для отчёта о недостающих переменных.
	SecretsPath string
	// Missing — маскированные переменные, чьё значение не покрыто ни одним
	// слоем: GitLab сообщил об их существовании, а локальный файл секретов
	// их не заполнил. Отсортирован по Key.
	Missing []Missing
}

// Missing — одна недостающая маскированная переменная. Умышленно не имеет
// поля под значение: тип, в котором значению негде поместиться, не может
// его случайно вынести наружу.
type Missing struct {
	Key string
	// Origin описывает, откуда известно о существовании переменной — путь
	// проекта или группы, приславшей её как маскированную.
	Origin string
}

// FileContents отдаёт ключ и содержимое переменных с KindFile. Единственное
// место, где содержимое файловой переменной покидает доменную модель —
// потребитель у неё ровно один: материализация в пакете runner
// (internal/runner/filevars.go).
func (e Environment) FileContents() map[string]string {
	m := make(map[string]string)
	for _, v := range e.Vars {
		if v.Kind == KindFile {
			m[v.Key] = v.Value
		}
	}
	return m
}

// validWorkDir проверяет, годится ли значение CI_PROJECT_DIR на роль
// рабочего каталога контейнера. Значение приходит из переменной проекта,
// группы или пайплайна по API либо из локального файла секретов — то есть
// снаружи границы доверия, — а дальше становится и точкой монтирования
// каталога кода, и аргументом рабочего каталога контейнера, и основой пути
// файловых переменных (workDir + ".tmp").
//
// Требования: непустой абсолютный POSIX-путь, нормализованный (path.Clean
// возвращает его же — значит ни "..", ни "//", ни хвостовой "/"), без
// двоеточия и без управляющих символов. Двоеточие названо отдельно, потому
// что аргумент монтирования контейнерного CLI имеет форму
// "<источник>:<цель>": значение "/builds/p:ro" пересобрало бы монтирование
// в режим только для чтения, и правки пользователя не были бы видны на
// хосте вовсе. Пустое значение (переменная проекта, выставленная в "")
// давало бы рабочий каталог "" и монтирование файловых переменных в ".tmp".
func validWorkDir(dir string) bool {
	if dir == "" || !path.IsAbs(dir) || path.Clean(dir) != dir {
		return false
	}
	if strings.ContainsRune(dir, ':') {
		return false
	}
	for _, r := range dir {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// WorkDir — рабочий каталог джобы: значение CI_PROJECT_DIR из собранных
// переменных, а при его отсутствии — правило GitLab по умолчанию, каталог
// сборок с путём проекта (/builds/<ProjectPath>). Правило доменное, а не про
// вывод, поэтому живёт здесь, а не в cmd/ci: от него зависит и рабочий
// каталог контейнера, и точка монтирования файловых переменных (сосед
// рабочего каталога с суффиксом .tmp).
//
// Негодное значение CI_PROJECT_DIR (см. validWorkDir) не берётся вовсе —
// возвращается тот же каталог по умолчанию: доверять переменной, пришедшей
// по API, в вопросе о том, что именно монтируется с хоста в контейнер,
// нельзя.
func (e Environment) WorkDir() string {
	for _, v := range e.Vars {
		if v.Key == "CI_PROJECT_DIR" {
			if validWorkDir(v.Value) {
				return v.Value
			}
			break
		}
	}
	return "/builds/" + e.ProjectPath
}

// Map отдаёт плоскую карту ключ→значение для docker run. Параметр filePaths
// обязателен, а не необязателен: карта — единственный канал значений в
// контейнер, и возможность собрать её без путей означала бы возможность
// вернуть сегодняшнюю поломку (содержимое файловой переменной вместо пути к
// ней) одной строкой. Для обычной переменной в карту уходит её значение, как
// и раньше. Для переменной с KindFile в карту уходит путь из filePaths, а
// если пути для неё нет (материализация её пропустила), ключ в карту не
// попадает вовсе.
func (e Environment) Map(filePaths map[string]string) map[string]string {
	m := make(map[string]string, len(e.Vars))
	for _, v := range e.Vars {
		if v.Kind == KindFile {
			if p, ok := filePaths[v.Key]; ok {
				m[v.Key] = p
			}
			continue
		}
		m[v.Key] = v.Value
	}
	return m
}

// Editable сообщает, можно ли поменять значение переменной v сессионной
// правкой (VAR-01). Единственная точка этого решения в проекте: её зовёт и
// сам Assemble ниже (вторая, доменная линия защиты того же правила), и
// экран (internal/ui/job.go, план 20-02) — второй, самостоятельной проверки
// класса переменной нигде не заводится. Три класса исключены, каждый по
// своей причине:
//
//   - Secret: у секретных переменных уже есть свой путь правки — файл
//     секретов через SaveSecret (Фаза 15), с диском и точечной записью по
//     узлам YAML; заводить для того же значения ВТОРОЙ, несохраняемый путь
//     значило бы держать в проекте два разных способа поменять одно и то
//     же, расходящихся в том, переживают ли они выход из ci-shell.
//   - Kind == KindFile: значение файловой переменной — содержимое файла
//     (kubeconfig, CA bundle, .npmrc), а не однострочный скаляр; однострочное
//     поле ввода для него не годится, а редактор многострочного содержимого
//     — вне охвата этой фазы.
//   - Source == SourcePredefined: предопределённые CI_* — это личность
//     самого воспроизведённого прогона. internal/repo.Materialize берёт
//     коммит из s.job.CommitSHA, а не из карты окружения; patchesDir/
//     WriteEditVars берут хост и путь проекта из s.ref, тоже не из карты.
//     Правка текста этих переменных в окружении контейнера оставила бы его
//     с CI_COMMIT_SHA, расходящимся с тем, что реально смонтировано с
//     хоста, — прямое нарушение обещания проекта «тот же коммит»
//     (PROJECT.md, Core Value).
//
// Проверка идёт и по метке Source, и по ИМЕНИ (PredefinedKeys): метка
// принадлежит слою, который последним записал ключ в карту, а слои конфига
// пайплайна и переменных проекта пишут безусловно. Переменная проекта,
// названная CI_COMMIT_SHA — а такую GitLab принимает и в `variables:`, и
// через API, — отбирала бы у настоящей предопределённой записи не только
// метку, но и запрет на правку, и обещание «личность прогона неприкосновенна»
// держалось бы лишь до первого совпадения имён (обзор v1.0.0, WR-1).
func Editable(v Variable) bool {
	return !v.Secret && v.Kind != KindFile &&
		v.Source != SourcePredefined && !PredefinedKeys(v.Key)
}

// Input — входные данные для сборки окружения.
type Input struct {
	Job       provider.Job
	Host      string
	JobConfig provider.JobConfig
	// APIVars — переменные проекта и групп, полученные через
	// Provider.Variables (GLAB-02).
	APIVars []provider.Variable
	// Notices — оговорки, собранные при опросе API (недоступные или не
	// найденные источники), скопированные как есть в Environment.Notices.
	Notices []string
	// Secrets — значения маскированных переменных из локального файла
	// секретов пользователя (Secrets.Values из internal/env/secrets.go).
	// Верхний слой приоритета в Assemble.
	Secrets map[string]string
	// SecretsPath — фактический путь к файлу секретов (Secrets.Path),
	// скопированный в Environment.SecretsPath для отчёта о недостающих
	// переменных.
	SecretsPath string
	// Overrides — значения переменных, которые человек поменял на экране
	// джобы В ЭТОЙ СЕССИИ (Фаза 20). Слой применяется САМЫМ ВЕРХНИМ, после
	// локального файла секретов, потому что это самое недавнее осознанное
	// решение человека в рамках сессии. Ключ обязан уже существовать в
	// собранном на предыдущих слоях окружении — Overrides не объявляет
	// новых переменных, только меняет значение уже показанных на экране.
	// Исходные Secret/Kind/Source/Origin переменной СОХРАНЯЮТСЯ правкой
	// значения — правка не может тайно превратить обычную переменную в
	// секрет или наоборот.
	Overrides map[string]string
}

// Assemble собирает окружение джобы, накладывая слои в порядке возрастания
// приоритета — более поздний слой перезаписывает значение и источник
// одноимённой переменной: Predefined(in.Job, in.Host) → in.JobConfig.Variables
// (SourceConfig) → in.APIVars со Scope == ScopeGroup (SourceGroup) →
// in.APIVars со Scope == ScopeProject (SourceProject) → in.APIVars со
// Scope == ScopePipeline (SourcePipeline) → in.Secrets (SourceLocal) →
// in.Overrides (сессионные правки, только для переменных, которым Editable
// разрешает класс). Порядок повторяет приоритет переменных в самом GitLab,
// где предопределённые — самый нижний слой, переменные проекта перекрывают
// переменные групп, а переменные, переданные при запуске пайплайна
// (ручной запуск, trigger-токен), перекрывают их все; локальный файл
// секретов — верхний слой из API/файла, потому что он существует именно для
// значений, которые API не отдал, и пользователь ввёл их осознанно вручную
// (T-02-13); сессионные правки (Фаза 20) — самый верхний слой из всех,
// потому что это последнее, самое недавнее осознанное решение человека в
// рамках сессии. Итоговый список отсортирован по Key, чтобы вывод был
// стабильным между запусками.
func Assemble(in Input) Environment {
	vars := make(map[string]Variable)

	for _, v := range Predefined(in.Job, in.Host) {
		vars[v.Key] = v
	}
	for k, v := range in.JobConfig.Variables {
		vars[k] = Variable{Key: k, Value: v, Source: SourceConfig}
	}

	notices := append([]string{}, in.Notices...)

	// Имя окружения раскрывается из уже собранных переменных (Predefined +
	// JobConfig) до сверки областей: environment: preprod/$CI_COMMIT_REF_SLUG
	// — типовая запись, и без раскрытия сверка сравнивала бы шаблон со
	// строкой, содержащей знак доллара, теряя переменные молча.
	environment, unresolvedEnv := expandEnvName(in.JobConfig.Environment, vars)
	if len(unresolvedEnv) > 0 {
		notices = append(notices, fmt.Sprintf("имя окружения %q: не раскрыты подстановки %v", in.JobConfig.Environment, unresolvedEnv))
	}
	if environment != "" {
		// В настоящей джобе с environment: эта переменная есть — её
		// отсутствие видно любым echo внутри контейнера.
		vars["CI_ENVIRONMENT_NAME"] = Variable{Key: "CI_ENVIRONMENT_NAME", Value: environment, Source: SourceConfig}
	}

	// Слой групп, затем слой проекта — оба прохода устойчивы, поэтому
	// внутри каждой группы сохраняется порядок, в котором её вернул
	// провайдер (внешние раньше внутренних).
	applyAPILayer(vars, in.APIVars, provider.ScopeGroup, SourceGroup, environment, &notices)
	applyAPILayer(vars, in.APIVars, provider.ScopeProject, SourceProject, environment, &notices)

	// Слой пайплайна — выше проекта и групп: в самом GitLab переменные,
	// переданные при запуске пайплайна, перекрывают переменные проекта и
	// групп, но остаются ниже локального файла секретов.
	applyAPILayer(vars, in.APIVars, provider.ScopePipeline, SourcePipeline, environment, &notices)

	// Секреты — верхний слой приоритета. Ключ из файла секретов, которого
	// нет ни в одном другом слое (пользователь мог добавить переменную,
	// которую API не показал вовсе), всё равно попадает в окружение.
	// Переменная, помеченная Masked провайдером, не перестаёт быть
	// секретом оттого, что её значение стало известно — здесь она
	// перезаписывается новой Variable с Secret: true. Kind сохраняется:
	// файловая переменная остаётся файловой, даже когда её значение
	// пришло из локального файла (WR-05, материализация — Фаза 3).
	for k, v := range in.Secrets {
		vars[k] = Variable{Key: k, Value: v, Source: SourceLocal, Secret: true, Kind: vars[k].Kind}
	}

	// Сессионные правки — верхний слой из всех (VAR-01). Проверка Editable
	// повторяется здесь, а не доверяется вызывающему: Assemble — доменная
	// граница, и она не должна полагаться на то, что единственный сегодняшний
	// вызывающий (интерфейс) уже отфильтровал класс переменной правильно —
	// примут ли Overrides в будущем другого вызывающего или нет, решение
	// остаётся здесь тоже. Ключ, которого нет в уже собранной карте, или
	// переменная, которой Editable запрещает класс, пропускаются молча:
	// Overrides не заводит новых переменных и не может обойти защиту секретов,
	// файловых и предопределённых переменных.
	for k, v := range in.Overrides {
		existing, ok := vars[k]
		if !ok || !Editable(existing) {
			continue
		}
		existing.Value = v
		vars[k] = existing
	}

	out := make([]Variable, 0, len(vars))
	for _, v := range vars {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })

	// Недостающие переменные: Secret == true и Value == "" означает, что
	// провайдер сообщил о переменной как маскированной, а ни один слой
	// (включая секреты) её не заполнил. out уже отсортирован по Key,
	// поэтому missing строится в том же порядке без дополнительной
	// сортировки.
	var missing []Missing
	for _, v := range out {
		if v.Secret && v.Value == "" {
			missing = append(missing, Missing{Key: v.Key, Origin: v.Origin})
		}
	}

	return Environment{
		Vars:        out,
		Host:        in.Host,
		ProjectPath: in.Job.ProjectPath,
		Notices:     notices,
		SecretsPath: in.SecretsPath,
		Missing:     missing,
	}
}

// applyAPILayer накладывает на vars переменные apiVars с областью
// provider-scope scope, помечая их source и Origin (путь группы,
// проекта или #<id> пайплайна). Переменная подставляется, когда
// scopeMatches(v.EnvironmentScope, environment) истинно; иначе она
// по-прежнему не подставляется, но в notices уходит оговорка, называющая
// ключ, источник, область переменной и имя окружения джобы (прочерк, когда
// окружения нет) — idea §1: утилита не должна подставлять окружение, не
// соответствующее упавшему прогону, но и не должна терять переменную
// молча. Защищённая (Protected) переменная подставляется, но с оговоркой в
// notices — API не говорит, был ли ref прогона защищённым. Маскированные
// переменные попадают в vars с пустым Value и Secret: true — как
// существующие, но без значения.
//
// Проход внутри слоя двухпроходный: сначала накладываются переменные с
// неограниченной областью (unrestrictedScope), затем — с конкретной
// совпавшей. Так при совпадении ключей побеждает более конкретная область,
// а не та, что случайно оказалась позже в ответе API — ровно тот случай,
// когда у проекта заведены и общий DEPLOY_URL, и DEPLOY_URL для preprod/*.
func applyAPILayer(vars map[string]Variable, apiVars []provider.Variable, scope provider.VariableScope, source Source, environment string, notices *[]string) {
	apply := func(v provider.Variable) {
		// Jobs API в v0.1.0 не сообщает, защищён ли ref упавшего прогона,
		// поэтому protected-переменная подставляется, но с честной оговоркой:
		// если джоба шла по незащищённой ветке, в реальном прогоне этой
		// переменной не было (WR-04, idea §1).
		if v.Protected {
			*notices = append(*notices, fmt.Sprintf("%s (%s %s): переменная защищённая (protected) — подставлена, хотя упавший прогон мог идти по незащищённой ветке", v.Key, scope, v.Owner))
		}
		kind := KindEnv
		if v.IsFile {
			kind = KindFile
		}
		vars[v.Key] = Variable{
			Key:    v.Key,
			Value:  v.Value,
			Source: source,
			Origin: v.Owner,
			Secret: v.Masked,
			Kind:   kind,
		}
	}

	for _, v := range apiVars {
		if v.Scope != scope || !unrestrictedScope(v.EnvironmentScope) {
			continue
		}
		apply(v)
	}
	for _, v := range apiVars {
		if v.Scope != scope || unrestrictedScope(v.EnvironmentScope) {
			continue
		}
		envDesc := environment
		if envDesc == "" {
			envDesc = "—"
		}
		if !scopeMatches(v.EnvironmentScope, environment) {
			*notices = append(*notices, fmt.Sprintf("%s (%s %s): область окружения %q не совпадает с окружением джобы %q — переменная не подставлена", v.Key, scope, v.Owner, v.EnvironmentScope, envDesc))
			continue
		}
		apply(v)
	}
}
