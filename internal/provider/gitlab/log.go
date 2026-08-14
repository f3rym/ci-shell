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

	lines, section, sectionLine := cleanTrace(raw, maxLogLines, truncated)
	return provider.Log{Lines: lines, Section: section, SectionLine: sectionLine, Truncated: truncated}, nil
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

// cleanTrace разбирает кусок сырых байт лога raw на строки. Настоящий маркер
// ранера не заканчивается переводом строки — он оформлен как
// "\x1b[0Ksection_start:<ts>:<имя>\r\x1b[0K", то есть заканчивается \r, а
// заканчивается перевод строки только у СЛЕДУЮЩЕГО за ним содержимого
// (первая видимая строка секции, эхо команды и т.п.), которое strings.Split
// по "\n" физически склеивает с маркером в одну "строку" — поэтому сама
// граница \r внутри неё разбирается отдельно (splitMarker), а не
// отбрасывается вместе с маркером: то, что идёт ПОСЛЕ \r — настоящий вывод
// джобы, и его нельзя терять на каждой границе секции (CR-01 обзора
// исполняющей части v1.0.0).
//
// Строка с маркером начала секции (section_start) запоминает имя секции и
// номер строки, с которой эта секция начинается (sectionLine — текущая
// длина уже накопленного среза lines: сам маркер на экран не идёт, поэтому
// именно эта длина и есть индекс первой строки секции); строка с маркером
// конца секции (section_end) саму себя не показывает, но её возможный
// хвост после \r — тоже настоящий вывод и идёт в lines той же формулой.
// Маркер распознаётся ЯКОРНО — HasPrefix после отбрасывания ведущего
// "\x1b[0K", а не Contains где угодно в строке: иначе любая строка вывода
// самой джобы, в которой подстрока "section_start:"/"section_end:" просто
// ВСТРЕЧАЕТСЯ (например, джоба печатает пример строки трассы или тестовый
// фикстур формата лога GitLab CI), подделывала бы Section/SectionLine —
// то самое поле, которое интерфейс подсвечивает как «здесь упал шаг».
//
// Каждая оставшаяся строка проходит через provider.SafeText — лог джобы это
// произвольный вывод чужих команд, и в нём могут быть последовательности,
// перерисовывающие экран и подделывающие строку подсказки; ровно поэтому ни
// одна строка лога не попадает наружу непрочищенной (T-10-03). Первая строка
// отбрасывается, если tailed=true (кусок получен хвостом или урезан пределом
// кольцевого буфера) — она почти наверняка обрезана посередине и показала бы
// человеку огрызок. В результат идут последние maxLines строк — если урезка
// отбросила часть строк сверху, sectionLine сдвигается на то же число и
// становится -1, если ушёл в минус (начало секции уехало за предел урезки).
// Возвращаются строки, имя последней открытой секции и номер её первой
// строки — это и есть «упавший шаг» в терминах GitLab (step_script и его
// соседи); имя секции не переводится, как не переводится статус.
func cleanTrace(raw []byte, maxLines int, tailed bool) ([]string, string, int) {
	rawLines := strings.Split(string(raw), "\n")
	if tailed && len(rawLines) > 1 {
		rawLines = rawLines[1:]
	}

	var lines []string
	section := ""
	sectionLine := -1
	for _, raw := range rawLines {
		body := stripEscClear(raw)
		switch {
		case strings.HasPrefix(body, sectionStartMarker):
			marker, rest := splitMarker(body)
			section = sectionName(marker)
			sectionLine = len(lines)
			if rest != "" {
				lines = append(lines, provider.SafeText(rest))
			}
			continue
		case strings.HasPrefix(body, sectionEndMarker):
			_, rest := splitMarker(body)
			if rest != "" {
				lines = append(lines, provider.SafeText(rest))
			}
			continue
		}
		lines = append(lines, provider.SafeText(strings.TrimRight(raw, "\r")))
	}

	if len(lines) > maxLines {
		dropped := len(lines) - maxLines
		lines = lines[len(lines)-maxLines:]
		if sectionLine >= 0 {
			sectionLine -= dropped
			if sectionLine < 0 {
				sectionLine = -1
			}
		}
	}
	return lines, section, sectionLine
}

// escClear — "стереть до конца строки терминала", которым gitlab-runner
// оборачивает каждый маркер секции слева ("\x1b[0Ksection_start:..."). Не
// часть самого маркера и не часть содержимого — только визуальное
// оформление для терминалов, которые лог читают напрямую.
const escClear = "\x1b[0K"

// stripEscClear отбрасывает один ведущий escClear, если он есть.
func stripEscClear(s string) string {
	return strings.TrimPrefix(s, escClear)
}

// splitMarker делит body (уже без ведущего escClear) на сам маркер и
// настоящее содержимое джобы, склеенное с ним через \r в одну физическую
// "строку" strings.Split-ом по "\n" (см. cleanTrace выше). Если \r в body
// нет — маркер занимает body целиком, хвоста нет. Если есть — то, что идёт
// после \r, тоже может начинаться с собственного escClear ("\r\x1b[0K") —
// он снимается тем же stripEscClear, чтобы в rest не осталось управляющих
// байт, которые provider.SafeText потом всё равно бы вырезал, но раньше
// времени и без всякой пользы здесь.
func splitMarker(body string) (marker, rest string) {
	idx := strings.IndexByte(body, '\r')
	if idx < 0 {
		return body, ""
	}
	return body[:idx], stripEscClear(body[idx+1:])
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
