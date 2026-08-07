package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/f3rym/ci-shell/internal/provider"
)

// variableResponse — элемент ответа GET /groups/:id/variables и
// GET /projects/:id/variables.
type variableResponse struct {
	Key              string `json:"key"`
	Value            string `json:"value"`
	Masked           bool   `json:"masked"`
	Protected        bool   `json:"protected"`
	VariableType     string `json:"variable_type"`
	EnvironmentScope string `json:"environment_scope"`
}

// Variables возвращает переменные проекта и всех родительских групп,
// видимые джобе job. Реализует provider.Provider.Variables (GLAB-02).
//
// Опрос идёт от внешнего к внутреннему — сначала группы-предки в порядке от
// самой внешней к самой внутренней, затем сам проект — чтобы при слиянии в
// internal/env более близкий источник перекрывал дальний. Отказ одного
// источника (нет доступа, источник не найден) не прерывает обход остальных:
// токен часто имеет область read_api или роль ниже Maintainer, при которой
// настройки переменных закрыты — это нормальная ситуация, а не повод
// обрывать восстановление окружения (idea §3.4).
func (c *Client) Variables(ctx context.Context, job provider.Job) (provider.VariableSet, error) {
	var set provider.VariableSet

	for _, g := range groupPaths(job.ProjectPath) {
		path := fmt.Sprintf("/groups/%s/variables?per_page=100", url.PathEscape(g))
		vars, notes, err := c.fetchVariables(ctx, path, provider.ScopeGroup, g)
		if err != nil {
			return provider.VariableSet{}, err
		}
		set.Variables = append(set.Variables, vars...)
		set.Notes = append(set.Notes, notes...)
	}

	path := fmt.Sprintf("/projects/%s/variables?per_page=100", url.PathEscape(job.ProjectPath))
	vars, notes, err := c.fetchVariables(ctx, path, provider.ScopeProject, job.ProjectPath)
	if err != nil {
		return provider.VariableSet{}, err
	}
	set.Variables = append(set.Variables, vars...)
	set.Notes = append(set.Notes, notes...)

	return set, nil
}

// fetchVariables запрашивает переменные одного источника (owner — путь
// группы или проекта) по path и деградирует вместо падения: недоступный
// или отсутствующий источник добавляет оговорку в notes и возвращает
// пустой список без ошибки; любая другая ошибка транспорта возвращается
// как есть, потому что не является ожидаемой деградацией.
//
// Маскированные значения не забираются: если ответ помечает переменную
// masked: true, в provider.Variable кладётся ключ и Masked: true, а поле
// Value остаётся пустым, даже если в теле ответа что-то пришло (T-02-02).
func (c *Client) fetchVariables(ctx context.Context, path string, scope provider.VariableScope, owner string) ([]provider.Variable, []string, error) {
	body, err := c.do(ctx, http.MethodGet, path)
	if err != nil {
		switch {
		case errors.Is(err, errStatusNotFound):
			return nil, []string{fmt.Sprintf("%s %s: источник переменных не найден", scope, owner)}, nil
		case errors.Is(err, provider.ErrUnauthorized):
			return nil, []string{fmt.Sprintf("%s %s: нет доступа к переменным", scope, owner)}, nil
		default:
			return nil, nil, err
		}
	}

	var raw []variableResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, nil, fmt.Errorf("gitlab: разбор переменных источника %s %s: %w", scope, owner, err)
	}

	vars := make([]provider.Variable, 0, len(raw))
	for _, r := range raw {
		v := provider.Variable{
			Key:               r.Key,
			Masked:            r.Masked,
			Protected:         r.Protected,
			IsFile:            r.VariableType == "file",
			EnvironmentScope:  r.EnvironmentScope,
			Scope:             scope,
			Owner:             owner,
		}
		if !r.Masked {
			v.Value = r.Value
		}
		vars = append(vars, v)
	}

	var notes []string
	if len(raw) == 100 {
		notes = append(notes, fmt.Sprintf("%s %s: источник вернул 100 переменных, часть могла не поместиться (постраничный обход вне v0.1.0)", scope, owner))
	}

	return vars, notes, nil
}

// groupPaths возвращает пути всех родительских групп проекта projectPath,
// от самой внешней к самой внутренней: для "a/b/c/project" — это
// ["a", "a/b", "a/b/c"]. Для проекта без групп в пути (без "/") возвращает
// пустой срез.
func groupPaths(projectPath string) []string {
	segments := strings.Split(projectPath, "/")
	if len(segments) <= 1 {
		return nil
	}
	// Последний сегмент — сам проект, остальные — предки-группы.
	segments = segments[:len(segments)-1]

	paths := make([]string, 0, len(segments))
	for i := range segments {
		paths = append(paths, strings.Join(segments[:i+1], "/"))
	}
	return paths
}
