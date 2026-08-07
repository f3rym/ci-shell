package env

import (
	"strconv"
	"strings"
	"time"

	"github.com/f3rym/ci-shell/internal/provider"
)

// Predefined восстанавливает предопределённые CI_* переменные из метаданных
// джобы job и хоста host, на котором она выполнялась. Значения строятся
// исключительно из полей job и параметра host — окружение текущего процесса
// не читается ни при каких условиях: иначе утилита подсунула бы переменные
// машины пользователя вместо переменных самой джобы (T-02-07).
//
// Переменная с пустым восстановленным значением в список не попадает:
// лучше отсутствие переменной, чем ложное пустое значение, которое в
// контейнере затрёт настоящее.
func Predefined(job provider.Job, host string) []Variable {
	var vars []Variable
	add := func(key, value string) {
		if value == "" {
			return
		}
		vars = append(vars, Variable{Key: key, Value: value, Source: SourcePredefined})
	}

	add("CI", "true")
	add("GITLAB_CI", "true")
	add("CI_JOB_ID", formatIfPositive(job.ID))
	add("CI_JOB_NAME", job.Name)
	add("CI_JOB_STAGE", job.Stage)
	add("CI_JOB_STATUS", job.Status)
	add("CI_JOB_URL", job.WebURL)
	if !job.StartedAt.IsZero() {
		add("CI_JOB_STARTED_AT", job.StartedAt.Format(time.RFC3339))
	}
	add("CI_PIPELINE_ID", formatIfPositive(job.PipelineID))
	add("CI_PIPELINE_IID", formatIfPositive(job.PipelineIID))
	add("CI_COMMIT_SHA", job.CommitSHA)
	add("CI_COMMIT_SHORT_SHA", shortSHA(job.CommitSHA))
	add("CI_COMMIT_REF_NAME", job.Ref)
	add("CI_COMMIT_REF_SLUG", slugify(job.Ref))
	add("CI_COMMIT_TITLE", job.CommitTitle)

	// Ветвление по job.Tag: утилита не утверждает того, чего не знает
	// (idea §7) — тег и ветка взаимоисключающие для одного и того же ref.
	if job.Tag {
		add("CI_COMMIT_TAG", job.Ref)
	} else {
		add("CI_COMMIT_BRANCH", job.Ref)
	}

	add("CI_PROJECT_ID", formatIfPositive(job.ProjectID))
	add("CI_PROJECT_PATH", job.ProjectPath)
	add("CI_PROJECT_NAME", projectName(job.ProjectPath))
	add("CI_PROJECT_NAMESPACE", projectNamespace(job.ProjectPath))
	if job.ProjectPath != "" {
		add("CI_PROJECT_DIR", "/builds/"+job.ProjectPath)
	}
	if host != "" {
		add("CI_SERVER_HOST", host)
		add("CI_SERVER_URL", "https://"+host)
		add("CI_API_V4_URL", "https://"+host+"/api/v4")
		if job.ProjectPath != "" {
			add("CI_PROJECT_URL", "https://"+host+"/"+job.ProjectPath)
		}
	}
	add("CI_SERVER_PORT", "443")
	add("CI_SERVER_PROTOCOL", "https")
	add("CI_RUNNER_DESCRIPTION", job.RunnerDesc)

	return vars
}

// formatIfPositive форматирует id-подобное поле, только если оно
// положительное: нулевое значение означает «GitLab не прислал», а не
// «id равен нулю» — таких id в GitLab не бывает.
func formatIfPositive(n int64) string {
	if n <= 0 {
		return ""
	}
	return strconv.FormatInt(n, 10)
}

// shortSHA возвращает первые 8 символов sha. Более короткие или пустые
// значения возвращаются как есть.
func shortSHA(sha string) string {
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}

// slugify превращает произвольную строку в слаг по правилам GitLab для
// CI_COMMIT_REF_SLUG: нижний регистр, любой не-алфанумерический символ
// заменяется на дефис, дефисы по краям обрезаются, максимум 63 символа.
func slugify(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 63 {
		slug = strings.TrimRight(slug[:63], "-")
	}
	return slug
}

// projectName возвращает последний сегмент пути проекта
// (CI_PROJECT_NAME).
func projectName(projectPath string) string {
	if idx := strings.LastIndex(projectPath, "/"); idx >= 0 {
		return projectPath[idx+1:]
	}
	return projectPath
}

// projectNamespace возвращает путь проекта без последнего сегмента
// (CI_PROJECT_NAMESPACE).
func projectNamespace(projectPath string) string {
	if idx := strings.LastIndex(projectPath, "/"); idx >= 0 {
		return projectPath[:idx]
	}
	return ""
}
