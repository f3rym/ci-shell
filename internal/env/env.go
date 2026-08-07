// Package env собирает доменную модель окружения упавшей джобы:
// предопределённые CI_* переменные и переменные конфигурации пайплайна,
// слитые в единый список с указанием источника каждой переменной.
package env

import (
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
}

// Environment — собранное окружение упавшей джобы.
type Environment struct {
	Vars []Variable
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
}

// Assemble собирает окружение джобы, накладывая слои в порядке возрастания
// приоритета — более поздний слой перезаписывает значение и источник
// одноимённой переменной: сначала Predefined(in.Job, in.Host), затем
// in.JobConfig.Variables с источником SourceConfig. Порядок повторяет
// приоритет переменных в самом GitLab, где предопределённые — самый нижний
// слой. Итоговый список отсортирован по Key, чтобы вывод был стабильным
// между запусками.
func Assemble(in Input) Environment {
	vars := make(map[string]Variable)

	for _, v := range Predefined(in.Job, in.Host) {
		vars[v.Key] = v
	}
	for k, v := range in.JobConfig.Variables {
		vars[k] = Variable{Key: k, Value: v, Source: SourceConfig}
	}

	out := make([]Variable, 0, len(vars))
	for _, v := range vars {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })

	return Environment{Vars: out}
}
