// Package artifacts восстанавливает артефакты джоб, от которых зависела
// упавшая джоба, в её рабочий каталог — то самое входное состояние, с
// которым она стартовала в CI (Фаза 21, ART-02…ART-06;
// docs/artifacts-design.md).
//
// Пакет отдельный, а не часть internal/repo: repo объявляет о себе «git и
// файловая система, больше ничего» и не знает про provider вовсе, а
// скачивание архива обязано идти через провайдера. Форма повторяет
// repo.Materialize: Request на вход, единственная точка входа Restore.
//
// Архив здесь — ЧУЖОЙ недоверенный файл. Всё, что связано с распаковкой,
// исходит из этого: см. extract ниже.
package artifacts

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/f3rym/ci-shell/internal/event"
	"github.com/f3rym/ci-shell/internal/provider"
)

// Request — вход восстановления.
type Request struct {
	// Job — упавшая джоба: из неё берутся путь проекта и пайплайн, внутри
	// которого ищутся джобы-источники.
	Job provider.Job
	// Config — конфиг упавшей джобы: dependencies/needs решают, чьи
	// артефакты нужны.
	Config provider.JobConfig
	// Provider — доступ к API того же хоста, что и джоба.
	Provider provider.Provider
	// DestDir — каталог, в который распаковывать. Это тот же каталог, где
	// уже лежит код (Checkout.Dir): настоящий ранер GitLab кладёт артефакты
	// ПОВЕРХ рабочего каталога, а не в отдельный подкаталог.
	DestDir string
	// SizeLimit, ExtractLimit — пределы скачанного и распакованного. Нулевые
	// значения означают умолчания провайдера.
	SizeLimit    int64
	ExtractLimit int64
}

// Result — что получилось.
type Result struct {
	// Jobs — имена джоб, чьи артефакты действительно распакованы.
	Jobs []string
	// Files, Bytes — сколько файлов записано и сколько байт занято.
	Files int
	Bytes int64
	// Notes — честные оговорки: источник не дался, запись архива отвергнута.
	// Возвращаются, а не проглатываются: человек должен знать, что часть
	// входного состояния не восстановлена, — тем же приёмом, что
	// VariableSet.Notes.
	Notes []string
}

// Restore — единственная точка входа. Не возвращает ошибку, когда не дался
// отдельный источник: воспроизведение обязано дойти до шелла, а шелл и есть
// инструмент проверки. Ошибка возвращается только на том, что делает
// восстановление бессмысленным целиком — недоступный каталог назначения.
func Restore(ctx context.Context, req Request, em event.Emitter) (*Result, error) {
	if req.DestDir == "" {
		return nil, errors.New("artifacts: не задан каталог назначения")
	}
	if req.SizeLimit <= 0 {
		req.SizeLimit = provider.DefaultArtifactSizeLimit
	}
	if req.ExtractLimit <= 0 {
		req.ExtractLimit = provider.DefaultArtifactExtractLimit
	}

	sources, reason, err := resolveSources(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		em.Emit(event.ArtifactsSkipped{Reason: reason})
		return &Result{}, nil
	}

	em.Emit(event.ArtifactsFetching{Total: len(sources)})
	res := &Result{}
	for _, src := range sources {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		files, bytes, notes, err := restoreOne(ctx, req, src, res.Bytes, em)
		res.Notes = append(res.Notes, notes...)
		if err != nil {
			// Один источник не дался — остальные продолжают
			// восстанавливаться. Причина уходит и событием, и оговоркой.
			em.Emit(event.ArtifactsUnavailable{Job: src.Name, Reason: err.Error()})
			res.Notes = append(res.Notes, fmt.Sprintf("артефакты джобы %s не восстановлены: %s", src.Name, err))
			continue
		}
		res.Jobs = append(res.Jobs, src.Name)
		res.Files += files
		res.Bytes += bytes
		em.Emit(event.ArtifactsExtracted{Job: src.Name, Files: files})
	}

	em.Emit(event.ArtifactsReady{Files: res.Files, Bytes: res.Bytes})
	return res, nil
}

// restoreOne тянет и распаковывает артефакты одной джобы-источника.
func restoreOne(ctx context.Context, req Request, src provider.Job, already int64, em event.Emitter) (int, int64, []string, error) {
	body, declared, err := req.Provider.ArtifactsArchive(ctx, req.Job.ProjectPath, src.ID)
	if err != nil {
		return 0, 0, nil, err
	}
	defer body.Close()

	// Первая из двух проверок предела: объявленная длина. Отказ здесь не
	// стоит ни одного прочитанного байта.
	if declared > 0 && declared > req.SizeLimit {
		return 0, 0, nil, fmt.Errorf("архив %d МиБ больше предела %d МиБ", declared>>20, req.SizeLimit>>20)
	}
	em.Emit(event.ArtifactDownloading{Job: src.Name, Bytes: maxInt64(declared, 0)})

	// Вторая проверка: фактически прочитанное. Content-Length может
	// отсутствовать или врать, поэтому читается на байт больше предела —
	// если этот лишний байт пришёл, предел превышен.
	tmp, err := os.CreateTemp("", "ci-shell-artifacts-*.zip")
	if err != nil {
		return 0, 0, nil, fmt.Errorf("временный файл: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	n, err := io.Copy(tmp, io.LimitReader(body, req.SizeLimit+1))
	if err != nil {
		return 0, 0, nil, fmt.Errorf("скачивание: %w", err)
	}
	if n > req.SizeLimit {
		// Усечённый zip — просто битый архив, а не частичный результат:
		// пытаться распаковать его значило бы выдать человеку невнятную
		// ошибку разбора вместо честного «больше предела».
		return 0, 0, nil, fmt.Errorf("архив больше предела %d МиБ, скачано %d МиБ — пропускаю", req.SizeLimit>>20, n>>20)
	}

	zr, err := zip.NewReader(tmp, n)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("чтение архива: %w", err)
	}

	files, written, notes := extract(zr, req.DestDir, req.ExtractLimit-already)
	return files, written, notes, nil
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// safeMode — права, с которыми записывается извлечённый файл.
//
// Бит исполняемости СОХРАНЯЕТСЯ: бинарь, собранный предыдущей стадией,
// обязан остаться исполняемым в следующей — ради этого артефакты и
// восстанавливаются. Групповая и прочая запись снимается, даже если архив
// её просил: чужой архив не вправе раздавать права на файлы в каталоге
// человека. setuid/setgid/sticky не переносятся вовсе.
func safeMode(m os.FileMode) os.FileMode {
	return m.Perm() &^ 0o022
}

// destPath проверяет, что запись архива останется ВНУТРИ dest, и возвращает
// её абсолютный путь. Второе значение false означает «запись отвергнута».
//
// Проверка идёт по очищенному пути (filepath.Clean схлопывает .. до того,
// как путь попадёт в файловую систему) и по отношению к dest через
// filepath.Rel: результат, начинающийся с "..", означает выход наружу.
// Именно это и есть zip slip — запись вида ../../etc/passwd.
func destPath(dest, name string) (string, bool) {
	// Нулевой байт в имени обрывает строку в системных вызовах: имя
	// "safe.txt\x00/../../etc/passwd" в Go пройдёт проверку целиком, а ядро
	// увидит только первую часть. Такие имена не чинятся, а отвергаются.
	if strings.ContainsRune(name, 0) {
		return "", false
	}
	// Абсолютный путь внутри архива — тот же выход наружу, только без "..".
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
		return "", false
	}
	full := filepath.Join(dest, filepath.Clean(name))
	rel, err := filepath.Rel(dest, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return full, true
}

// extract распаковывает архив в dest, не доверяя ему ни в чём.
//
// Отвергнутая запись пропускается с оговоркой, а не роняет весь архив: одна
// вредоносная или битая запись не должна лишать человека остальных
// легитимных файлов той же джобы.
func extract(zr *zip.Reader, dest string, budget int64) (int, int64, []string) {
	var (
		files   int
		written int64
		notes   []string
	)
	for _, f := range zr.File {
		if budget <= 0 {
			notes = append(notes, "распаковка остановлена: суммарный размер достиг предела")
			break
		}

		// Символические ссылки из чужого архива не создаются НИКОГДА.
		// Причина шире одной ссылки: сначала ссылка, указывающая наружу,
		// затем обычный файл с тем же именем — и защита от zip slip
		// обойдена, потому что путь второй записи формально остаётся
		// внутри каталога. Отказ от всего класса закрывает и этот обход.
		if f.Mode()&os.ModeSymlink != 0 {
			notes = append(notes, fmt.Sprintf("пропущена символическая ссылка %q — из чужого архива они не создаются", f.Name))
			continue
		}

		full, ok := destPath(dest, f.Name)
		if !ok {
			notes = append(notes, fmt.Sprintf("пропущена запись %q — она указывает за пределы рабочего каталога", f.Name))
			continue
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(full, 0o755); err != nil {
				notes = append(notes, fmt.Sprintf("каталог %q: %s", f.Name, err))
			}
			continue
		}

		n, err := extractFile(f, full, budget)
		written += n
		budget -= n
		if err != nil {
			notes = append(notes, fmt.Sprintf("файл %q: %s", f.Name, err))
			continue
		}
		files++
	}
	return files, written, notes
}

// extractFile пишет одну запись, не давая ей превысить budget.
//
// Предел считается по фактически записанному, а НЕ по полю
// UncompressedSize64 из заголовка записи: заголовок подделывается отдельно
// от самих сжатых данных, и доверять ему в решении «сколько писать» —
// значит не иметь защиты от архивной бомбы вовсе.
func extractFile(f *zip.File, full string, budget int64) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return 0, err
	}
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	out, err := os.OpenFile(full, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, safeMode(f.Mode()))
	if err != nil {
		return 0, err
	}
	defer out.Close()

	n, err := io.Copy(out, io.LimitReader(rc, budget+1))
	if err != nil {
		return n, err
	}
	if n > budget {
		// Запись не влезла в остаток бюджета — недописанный файл убирается:
		// оставить обрезанный кусок значило бы подсунуть джобе испорченный
		// вход, который она примет за настоящий.
		os.Remove(full)
		return 0, errors.New("превышен суммарный предел распакованного размера")
	}
	return n, nil
}
