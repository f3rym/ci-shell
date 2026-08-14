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

// ValidProjectPath проверяет путь проекта на пригодность к дальнейшему
// употреблению: непустые сегменты, ни одного "." или "..", только латинские
// буквы, цифры, точка, дефис, подчёркивание и косая черта как разделитель.
// Ровно этот набор GitLab и разрешает в пути группы и проекта.
//
// Проверка живёт на границе (здесь и в repo.parseRemote — двух местах, где
// путь проекта вообще попадает в программу), а не у каждого потребителя,
// потому что дальше эта строка расходится по слишком многим адресам:
// url.URL.Path зеркала (url.URL.String не убирает сегменты "..", так что
// "a/../../x" уехал бы в запрос как есть), имя ссылки refs/ci-shell в
// зеркале, значение origin внутри контейнера, путь каталога в кэше. Валидный
// на входе путь снимает вопрос во всех них сразу.
func ValidProjectPath(p string) bool {
	if p == "" {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		for _, r := range seg {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
				r == '.', r == '-', r == '_':
			default:
				return false
			}
		}
	}
	return true
}

// ValidHost проверяет хост на пригодность к дальнейшему употреблению: тот же
// принцип, что и у ValidProjectPath выше — только символы, которые GitLab
// реально допускает в доменном имени CI-сервера (буквы, цифры, точка,
// дефис, подчёркивание, необязательный ":порт" из цифр в конце). net/url.Parse
// пропускает в host сырыми куда больше символов, чем это — включая RFC 3986
// sub-delims ('&', '!', '*', одинарную кавычку и другие), значимые как
// индикаторы начала YAML-скаляра (якорь, тег, ссылка, кавычка). Второй
// рубеж защиты после сериализации ключа сериализатором в
// internal/env/secrets.go (CR-1 обзора безопасности v1.0.0): ни один хост,
// отклоняющийся от типичного DNS-имени, вообще не доходит до сборки Ref.
func ValidHost(h string) bool {
	host, port, hasPort := strings.Cut(h, ":")
	if host == "" {
		return false
	}
	if hasPort {
		if port == "" {
			return false
		}
		for _, r := range port {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

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

	if !ValidHost(u.Host) {
		return Ref{}, ErrNotAJobURL
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
	if !ValidProjectPath(projectPath) {
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

// JobNumber распознаёт аргумент, состоящий только из номера джобы: GitLab
// показывает номер именно с ведущей решёткой, и пользователь копирует его
// как есть, поэтому один ведущий "#" срезается перед разбором. Отрицательное,
// нулевое значение или непустой хвост после цифр — false. Ошибка не
// возвращается: «это не номер» — штатная ветка вызывающего кода (тогда
// пробуется Parse), а не поломка.
func JobNumber(raw string) (int64, bool) {
	s := strings.TrimPrefix(raw, "#")
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// FromParts собирает Ref из уже известных частей — хоста и пути проекта,
// выведенных из git remote origin, и номера джобы. WebURL строится в той же
// форме, что и Parse (https, разделитель "/-/jobs/"), чтобы форма ссылки
// оставалась в одном пакете и не расползалась по cmd/ci.
func FromParts(host, projectPath string, jobID int64) Ref {
	return Ref{
		Host:        host,
		ProjectPath: projectPath,
		JobID:       jobID,
		WebURL:      "https://" + host + "/" + projectPath + "/-/jobs/" + strconv.FormatInt(jobID, 10),
	}
}
