// Package env собирает доменную модель окружения упавшей джобы:
// предопределённые CI_* переменные и переменные конфигурации пайплайна,
// слитые в единый список с указанием источника каждой переменной.
package env

import (
	"fmt"
	"sort"

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

// Map отдаёт плоскую карту ключ→значение для Фазы 3 (docker run).
func (e Environment) Map() map[string]string {
	m := make(map[string]string, len(e.Vars))
	for _, v := range e.Vars {
		m[v.Key] = v.Value
	}
	return m
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
}

// Assemble собирает окружение джобы, накладывая слои в порядке возрастания
// приоритета — более поздний слой перезаписывает значение и источник
// одноимённой переменной: Predefined(in.Job, in.Host) → in.JobConfig.Variables
// (SourceConfig) → in.APIVars со Scope == ScopeGroup (SourceGroup) →
// in.APIVars со Scope == ScopeProject (SourceProject) → in.APIVars со
// Scope == ScopePipeline (SourcePipeline) → in.Secrets (SourceLocal).
// Порядок повторяет приоритет переменных в самом GitLab, где
// предопределённые — самый нижний слой, переменные проекта перекрывают
// переменные групп, а переменные, переданные при запуске пайплайна
// (ручной запуск, trigger-токен), перекрывают их все; локальный файл
// секретов — верхний слой, потому что он существует именно для значений,
// которые API не отдал, и пользователь ввёл их осознанно вручную (T-02-13).
// Итоговый список отсортирован по Key, чтобы вывод был стабильным между
// запусками.
func Assemble(in Input) Environment {
	vars := make(map[string]Variable)

	for _, v := range Predefined(in.Job, in.Host) {
		vars[v.Key] = v
	}
	for k, v := range in.JobConfig.Variables {
		vars[k] = Variable{Key: k, Value: v, Source: SourceConfig}
	}

	notices := append([]string{}, in.Notices...)

	// Слой групп, затем слой проекта — оба прохода устойчивы, поэтому
	// внутри каждой группы сохраняется порядок, в котором её вернул
	// провайдер (внешние раньше внутренних).
	applyAPILayer(vars, in.APIVars, provider.ScopeGroup, SourceGroup, &notices)
	applyAPILayer(vars, in.APIVars, provider.ScopeProject, SourceProject, &notices)

	// Слой пайплайна — выше проекта и групп: в самом GitLab переменные,
	// переданные при запуске пайплайна, перекрывают переменные проекта и
	// групп, но остаются ниже локального файла секретов.
	applyAPILayer(vars, in.APIVars, provider.ScopePipeline, SourcePipeline, &notices)

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

// applyAPILayer накладывает на vars переменные apiVars с областью scope,
// помечая их source и Origin (путь группы или проекта). Переменная
// с EnvironmentScope, отличным от "*" или пустого, не подставляется — в
// notices уходит строка с её ключом и областью (idea §1: утилита не должна
// подставлять окружение, не соответствующее упавшему прогону). Защищённая
// (Protected) переменная подставляется, но с оговоркой в notices — API не
// говорит, был ли ref прогона защищённым. Маскированные переменные попадают
// в vars с пустым Value и Secret: true — как существующие, но без значения.
func applyAPILayer(vars map[string]Variable, apiVars []provider.Variable, scope provider.VariableScope, source Source, notices *[]string) {
	for _, v := range apiVars {
		if v.Scope != scope {
			continue
		}
		if v.EnvironmentScope != "" && v.EnvironmentScope != "*" {
			*notices = append(*notices, fmt.Sprintf("%s (%s %s): область окружения %q — переменная не подставлена", v.Key, scope, v.Owner, v.EnvironmentScope))
			continue
		}
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
}
