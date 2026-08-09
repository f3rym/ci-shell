package gitlab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/f3rym/ci-shell/internal/joburl"
	"github.com/f3rym/ci-shell/internal/provider"
)

const (
	// maxLogBytes — предел чтения лога в память (BROW-04, T-10-09).
	maxLogBytes = 4 * 1024 * 1024 // 4 МиБ
	// maxLogLines — предел числа строк, попадающих в ответ.
	maxLogLines = 500
	// sectionStartMarker, sectionEndMarker — строковые маркеры, которыми
	// ранер GitLab размечает начало и конец раздела лога.
	sectionStartMarker = "section_start:"
	sectionEndMarker   = "section_end:"
)

// JobLog возвращает лог джобы jobID проекта projectPath — по умолчанию
// хвостом заданного размера (opts.TailBytes), а не целиком (BROW-04, D-02).
// Путь строится из проверенного и экранированного пути проекта и номера
// джобы.
//
// Если в опциях задан размер хвоста, к запросу добавляется заголовок
// диапазона с отрицательным смещением — «последние N байт»: это и есть
// исполнение решения D-02, при котором мегабайты не едут по сети вовсе.
func (c *Client) JobLog(ctx context.Context, projectPath string, jobID int64, opts provider.LogOptions) (provider.Log, error) {
	if !joburl.ValidProjectPath(projectPath) {
		return provider.Log{}, fmt.Errorf("gitlab: путь проекта %q недопустим: %w", projectPath, provider.ErrLogUnavailable)
	}

	path := fmt.Sprintf("/projects/%s/jobs/%d/trace", url.PathEscape(projectPath), jobID)

	header := http.Header{}
	if opts.TailBytes > 0 {
		header.Set("Range", fmt.Sprintf("bytes=-%d", opts.TailBytes))
	}

	resp, err := c.request(ctx, http.MethodGet, path, header)
	if err != nil {
		if errors.Is(err, errStatusNotFound) {
			// У джобы может не быть лога вовсе (не запускалась, лог протух
			// по политике хранения) — это не поломка, а нормальная ветка.
			return provider.Log{}, fmt.Errorf("gitlab: лог джобы %s#%d на хосте %s недоступен: %w", projectPath, jobID, c.host, provider.ErrLogUnavailable)
		}
		return provider.Log{}, err
	}
	defer resp.Body.Close()

	var (
		raw []byte
		// truncated=true, когда показан не весь лог — это и есть значение
		// provider.Log.Truncated ниже.
		truncated bool
	)

	if resp.StatusCode == http.StatusPartialContent {
		// Сервер понял диапазон — читается ровно то, что пришло,
		// ограниченным читателем.
		raw, err = io.ReadAll(io.LimitReader(resp.Body, maxLogBytes))
		if err != nil {
			return provider.Log{}, fmt.Errorf("gitlab: чтение хвоста лога джобы %s#%d: %s", projectPath, jobID, c.redact(err.Error()))
		}
		truncated = partialTruncated(resp.Header.Get("Content-Range"))
	} else {
		// Сервер диапазон проигнорировал (обычный успех) — тело читается
		// потоком через кольцевой буфер readTail, который держит в памяти
		// только последние keep байт и прекращает чтение на maxLogBytes:
		// поддержка диапазона на этом эндпоинте не гарантирована, а
		// прочитать весь лог в память «раз уж всё равно пришёл» — это и
		// есть неограниченный буфер, которого быть не должно.
		keep := opts.TailBytes
		if keep <= 0 || keep > maxLogBytes {
			keep = maxLogBytes
		}
		raw, truncated, err = readTail(resp.Body, keep, maxLogBytes)
		if err != nil {
			return provider.Log{}, fmt.Errorf("gitlab: чтение лога джобы %s#%d: %s", projectPath, jobID, c.redact(err.Error()))
		}
	}

	lines, section := cleanTrace(raw, maxLogLines, truncated)
	return provider.Log{Lines: lines, Section: section, Truncated: truncated}, nil
}

// readTail читает r в кольцевой буфер, держащий в памяти только последние
// keep байт, и не читает больше limit байт всего — так поддержка диапазона
// на эндпоинте необязательна, а память под лог всё равно ограничена
// (T-10-09). Второе возвращаемое значение — true, если источник оказался
// длиннее keep (часть более ранних байт была отброшена).
func readTail(r io.Reader, keep, limit int) ([]byte, bool, error) {
	if keep <= 0 {
		keep = 1
	}
	if limit < keep {
		limit = keep
	}

	// Читаем на один байт больше предела: если источник длиннее limit, этот
	// лишний байт покажет, что чтение остановлено принудительно, а не
	// естественным концом потока.
	limited := io.LimitReader(r, int64(limit)+1)
	var tail []byte
	var total int
	chunk := make([]byte, 32*1024)

	for {
		n, err := limited.Read(chunk)
		if n > 0 {
			total += n
			tail = append(tail, chunk[:n]...)
			if len(tail) > keep {
				tail = tail[len(tail)-keep:]
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, err
		}
	}

	return tail, total > keep, nil
}

// partialTruncated решает, действительно ли ответ 206 Partial Content
// обрезал часть лога, по заголовку Content-Range ("bytes start-end/total",
// RFC 7233). start > 0 означает, что перед возвращённым диапазоном были
// ещё байты — хвост не весь лог. Заголовок отсутствует или не разбирается —
// считаем усечённым: это безопасное предположение по умолчанию.
func partialTruncated(contentRange string) bool {
	spec := strings.TrimPrefix(contentRange, "bytes ")
	dash := strings.Index(spec, "-")
	if dash <= 0 {
		return true
	}
	start, err := strconv.Atoi(spec[:dash])
	if err != nil {
		return true
	}
	return start > 0
}

// cleanTrace разбирает кусок сырых байт лога raw на строки. Строка с
// маркером начала секции (section_start) запоминает имя секции и сама на
// экран не идёт, строка с маркером конца секции (section_end) просто
// отбрасывается; каждая оставшаяся строка проходит через provider.SafeText
// — лог джобы это произвольный вывод чужих команд, и в нём могут быть
// последовательности, перерисовывающие экран и подделывающие строку
// подсказки; ровно поэтому ни одна строка лога не попадает наружу
// непрочищенной (T-10-03). Первая строка отбрасывается, если tailed=true
// (кусок получен хвостом или урезан пределом кольцевого буфера) — она
// почти наверняка обрезана посередине и показала бы человеку огрызок. В
// результат идут последние maxLines строк. Возвращаются строки и имя
// последней открытой секции — это и есть «упавший шаг» в терминах GitLab
// (step_script и его соседи); имя секции не переводится, как не
// переводится статус.
func cleanTrace(raw []byte, maxLines int, tailed bool) ([]string, string) {
	rawLines := strings.Split(string(raw), "\n")
	if tailed && len(rawLines) > 1 {
		rawLines = rawLines[1:]
	}

	var lines []string
	section := ""
	for _, raw := range rawLines {
		line := strings.TrimRight(raw, "\r")
		switch {
		case strings.Contains(line, sectionStartMarker):
			section = sectionName(line)
			continue
		case strings.Contains(line, sectionEndMarker):
			continue
		}
		lines = append(lines, provider.SafeText(line))
	}

	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines, section
}

// sectionName вытаскивает имя секции из строки-маркера ранера вида
// "section_start:<unix-время>:<имя>[<опции>]<CR><ESC>...".
func sectionName(line string) string {
	idx := strings.Index(line, sectionStartMarker)
	if idx < 0 {
		return ""
	}
	rest := line[idx+len(sectionStartMarker):]
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) < 2 {
		return ""
	}
	name := parts[1]
	for i, r := range name {
		if r == '\r' || r == '\x1b' || r == '[' {
			name = name[:i]
			break
		}
	}
	return name
}
