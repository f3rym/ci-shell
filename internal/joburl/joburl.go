// Package joburl разбирает ссылку на джобу GitLab, взятую пользователем из
// браузера, в структурированную ссылку Ref{Host, ProjectPath, JobID}.
package joburl

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// Ref — разобранная ссылка на джобу GitLab.
type Ref struct {
	Host        string // gitlab.com
	ProjectPath string // group/subgroup/project
	JobID       int64  // 355442655
	WebURL      string // нормализованная https-ссылка
}

// separators — разделитель пути перед идентификатором джобы. Современный
// GitLab использует "/-/jobs/", устаревшая форма "/-/builds/" тоже
// принимается для совместимости со старыми ссылками.
var separators = []string{"/-/jobs/", "/-/builds/"}

var (
	ErrNotAJobURL     = errors.New("ссылка не похожа на ссылку на джобу GitLab")
	ErrInsecureScheme = errors.New("поддерживается только https")
)

// Parse разбирает произвольную строку со ссылкой на джобу GitLab.
func Parse(raw string) (Ref, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || (u.Host == "" && u.Scheme != "https") {
		// Пользователь мог вставить ссылку без схемы (скопировал из адресной
		// строки без "https://"). Подставляем схему и разбираем заново.
		// A scheme-less URL with a port ("host.com:8443/...") parses with
		// the host mistaken for the scheme and an empty Host — treat
		// "scheme but no host" as the scheme-less case too (WR-06).
		u, err = url.Parse("https://" + raw)
		if err != nil {
			return Ref{}, ErrNotAJobURL
		}
	}

	if u.Scheme != "https" {
		return Ref{}, ErrInsecureScheme
	}

	var left, right string
	found := false
	for _, sep := range separators {
		if idx := strings.Index(u.Path, sep); idx >= 0 {
			left = u.Path[:idx]
			right = u.Path[idx+len(sep):]
			found = true
			break
		}
	}
	if !found {
		return Ref{}, ErrNotAJobURL
	}

	projectPath := strings.Trim(left, "/")
	if projectPath == "" {
		return Ref{}, ErrNotAJobURL
	}

	// Хвост может содержать дополнительные сегменты пути (например,
	// "/retry"), поэтому берём только первый сегмент как id джобы.
	tail := strings.SplitN(right, "/", 2)[0]
	jobID, err := strconv.ParseInt(tail, 10, 64)
	if err != nil || jobID <= 0 {
		return Ref{}, ErrNotAJobURL
	}

	return Ref{
		Host:        u.Host,
		ProjectPath: projectPath,
		JobID:       jobID,
		WebURL:      "https://" + u.Host + u.Path,
	}, nil
}
