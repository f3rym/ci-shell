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

// variableResponse — элемент ответа GET /groups/:id/variables,
// GET /projects/:id/variables и GET /projects/:id/pipelines/:id/variables.
// Ответ эндпоинта переменных пайплайна беднее — в нём есть только key,
// value и variable_type, а полей masked, protected и environment_scope
// нет вовсе; нулевые значения этих полей при разборе означают ровно то,
// что нужно: переменная не маскирована и ничем не ограничена. Ровно
// поэтому отдельная структура ответа под пайплайн не заводится.
type variableResponse struct {
	Key              string `json:"key"`
	Value            string `json:"value"`
	Masked           bool   `json:"masked"`
	Protected        bool   `json:"protected"`
	VariableType     string `json:"variable_type"`
	EnvironmentScope string `json:"environment_scope"`
	// Hidden — переменная типа masked_and_hidden (GitLab 17.4+): значение
	// записывается один раз и не читается через API уже никогда, в ответе
	// приходит пустым. Единственный класс, который приходится добирать из
	// локального файла секретов.
	Hidden bool `json:"hidden"`
}

// Variables возвращает переменные проекта и всех родительских групп,
// видимые джобе job, а также переменные, переданные при запуске пайплайна
// job.PipelineID (ручной запуск, trigger-токен). Реализует
// provider.Provider.Variables (GLAB-02).
//
// Опрос идёт от внешнего к внутреннему — сначала группы-предки в порядке от
// самой внешней к самой внутренней, затем сам проект, затем пайплайн —
// чтобы при слиянии в internal/env более близкий источник перекрывал
// дальний. Порядок обхода здесь не приоритет (приоритет задаёт
// env.Assemble), но он держит источники в том же порядке, в каком они
// накладываются. Отказ одного источника (нет доступа, источник не найден)
// не прерывает обход остальных: токен часто имеет область read_api или
// роль ниже Maintainer, при которой настройки переменных закрыты — это
// нормальная ситуация, а не повод обрывать восстановление окружения
// (idea §3.4).
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

	pipelineVars, pipelineNotes, err := c.pipelineVariables(ctx, job)
	if err != nil {
		return provider.VariableSet{}, err
	}
	set.Variables = append(set.Variables, pipelineVars...)
	set.Notes = append(set.Notes, pipelineNotes...)

	return set, nil
}

// pipelineVariables запрашивает переменные, переданные при запуске
// пайплайна job.PipelineID: то, что человек ввёл в форме ручного запуска
// (when: manual) или передал trigger-токеном. Источника может не быть —
// job.PipelineID == 0 означает, что у джобы не известен пайплайн, и вместо
// запроса с нулевым идентификатором (который дал бы невнятный отказ
// сервера) возвращается честная оговорка.
func (c *Client) pipelineVariables(ctx context.Context, job provider.Job) ([]provider.Variable, []string, error) {
	if job.PipelineID == 0 {
		return nil, []string{"пайплайн: у джобы не известен идентификатор пайплайна — переменные пайплайна не запрашивались"}, nil
	}

	path := fmt.Sprintf("/projects/%s/pipelines/%d/variables?per_page=100", url.PathEscape(job.ProjectPath), job.PipelineID)
	owner := fmt.Sprintf("#%d", job.PipelineID)
	return c.fetchVariables(ctx, path, provider.ScopePipeline, owner)
}

// fetchVariables запрашивает переменные одного источника (owner — путь
// группы или проекта) по path и деградирует вместо падения: недоступный
// или отсутствующий источник добавляет оговорку в notes и возвращает
// пустой список без ошибки; любая другая ошибка транспорта возвращается
// как есть, потому что не является ожидаемой деградацией.
//
// Значение берётся из ответа как есть, включая маскированные переменные:
// маскирование в GitLab скрывает значение в логах джобы, а не в API, и
// сервер отдаёт его тому, чья роль это позволяет. Раз ответ пришёл — право
// на значение у токена уже есть, и добывать его повторно через локальный
// файл значило бы заставлять человека вручную переписывать то, что и так
// получено. Masked при этом сохраняется: значение не печатается в выводе.
//
// Исключение единственное — hidden (masked_and_hidden): такое значение
// сервер не отдаёт никому и никогда, для него остаётся файл секретов.
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
		if !r.Hidden {
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
