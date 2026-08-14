// sources.go — правило отбора: чьи артефакты нужны упавшей джобе
// (docs/artifacts-design.md §2). Отдельный файл, потому что это правило
// GitLab, а не наша механика: его читают и сверяют с документацией
// отдельно от кода скачивания и распаковки.
package artifacts

import (
	"context"
	"fmt"

	"github.com/f3rym/ci-shell/internal/provider"
)

// resolveSources возвращает джобы, чьи артефакты нужно восстановить, и —
// если восстанавливать нечего — причину, которую покажут человеку.
//
// Правило, по убыванию приоритета:
//
//   - needs: задан → берутся только перечисленные в нём джобы, у которых
//     artifacts не выключен; стадии при этом не важны вовсе (DAG);
//   - dependencies: [] (ключ есть, список пуст) → не берётся ничего;
//   - dependencies: [job…] → только перечисленные;
//   - ключа dependencies нет вовсе → все джобы БОЛЕЕ РАННИХ стадий.
//
// Последняя ветка — не «всё подряд»: у GitLab это и есть поведение по
// умолчанию, и повторять его обязательно, иначе воспроизведение молча
// недодаст джобе того, что у неё было в CI.
func resolveSources(ctx context.Context, req Request) ([]provider.Job, string, error) {
	// needs сильнее dependencies: когда он задан, GitLab строит граф по
	// нему, а стадии перестают определять порядок.
	if len(req.Config.Needs) > 0 {
		var names []string
		for _, n := range req.Config.Needs {
			if n.Artifacts && n.Job != "" {
				names = append(names, n.Job)
			}
		}
		if len(names) == 0 {
			return nil, "needs задан, но артефакты в нём выключены", nil
		}
		return byName(ctx, req, names)
	}

	// Ключ есть, список пуст — человек прямо сказал «ничего не брать».
	// Именно ради этой ветки разбор сохраняет различие nil и пустого среза.
	if req.Config.Dependencies != nil && len(req.Config.Dependencies) == 0 {
		return nil, "в конфиге джобы указано dependencies: [] — артефакты не восстанавливаются", nil
	}

	if len(req.Config.Dependencies) > 0 {
		return byName(ctx, req, req.Config.Dependencies)
	}

	return earlierStages(ctx, req)
}

// byName находит джобы пайплайна по именам. Не найденное имя не обрывает
// отбор: пайплайн мог измениться с момента падения, и терять из-за одного
// имени артефакты остальных незачем.
func byName(ctx context.Context, req Request, names []string) ([]provider.Job, string, error) {
	all, err := pipelineJobs(ctx, req)
	if err != nil {
		return nil, "", err
	}
	byJobName := make(map[string]provider.Job, len(all))
	for _, j := range all {
		// Первая встреченная выигрывает: у джоб parallel:/матрицы имена
		// различаются суффиксом вида "job 1/3", и точного совпадения с
		// базовым именем у них не будет — такие случаи вне охвата фазы и
		// честно пропускаются, а не угадываются.
		if _, ok := byJobName[j.Name]; !ok {
			byJobName[j.Name] = j
		}
	}
	var out []provider.Job
	for _, n := range names {
		if j, ok := byJobName[n]; ok {
			out = append(out, j)
		}
	}
	if len(out) == 0 {
		return nil, "джобы, названные в dependencies/needs, в пайплайне не найдены", nil
	}
	return out, "", nil
}

// earlierStages возвращает джобы стадий, идущих РАНЬШЕ стадии упавшей
// джобы. Порядок стадий берётся из самого пайплайна — из порядка, в котором
// GitLab отдаёт джобы: отдельного запроса за списком стадий у API нет, а
// выдуманный список («build, test, deploy») врал бы на любом пайплайне с
// своими именами стадий.
func earlierStages(ctx context.Context, req Request) ([]provider.Job, string, error) {
	all, err := pipelineJobs(ctx, req)
	if err != nil {
		return nil, "", err
	}

	order := make([]string, 0, 8)
	seen := make(map[string]bool, 8)
	for _, j := range all {
		if !seen[j.Stage] {
			seen[j.Stage] = true
			order = append(order, j.Stage)
		}
	}
	mine := -1
	for i, s := range order {
		if s == req.Job.Stage {
			mine = i
			break
		}
	}
	if mine <= 0 {
		// Стадия упавшей джобы первая или не опознана — раньше неё ничего
		// нет, и брать нечего.
		return nil, "у джобы нет более ранних стадий — восстанавливать нечего", nil
	}
	earlier := make(map[string]bool, mine)
	for _, s := range order[:mine] {
		earlier[s] = true
	}

	var out []provider.Job
	for _, j := range all {
		if earlier[j.Stage] {
			out = append(out, j)
		}
	}
	if len(out) == 0 {
		return nil, "на более ранних стадиях джоб не нашлось", nil
	}
	return out, "", nil
}

// pipelineJobs обходит все страницы джоб пайплайна упавшей джобы. Отдельный
// метод «найти джобу по имени» не заводится: постраничный обход уже есть, и
// второй метод с тем же результатом был бы копией.
func pipelineJobs(ctx context.Context, req Request) ([]provider.Job, error) {
	var (
		out    []provider.Job
		cursor provider.Cursor
	)
	for {
		page, err := req.Provider.PipelineJobs(ctx, req.Job.ProjectPath, req.Job.PipelineID, provider.PageRequest{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("список джоб пайплайна: %w", err)
		}
		out = append(out, page.Items...)
		if page.Next == "" {
			return out, nil
		}
		cursor = page.Next
	}
}
