package repo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/f3rym/ci-shell/internal/cache"
)

// Sentinel-ошибки этого файла — тот же стиль, что в worktree.go: каждая
// несёт диагностический контекст в тексте через fmt.Errorf("...: %w", ErrX).
var (
	// ErrNoChanges — в воспроизведённом чекауте нет правок: сохранять нечего.
	ErrNoChanges = errors.New("в воспроизведённом чекауте нет правок")
	// ErrNoPatch — сохранённых правок не найдено: пустой каталог,
	// отсутствующий каталог или отсутствие совпадения по джобе — штатная
	// ситуация «нечего переносить», а не поломка машины.
	ErrNoPatch = errors.New("сохранённых правок не найдено")
	// ErrPatchUnsafe — патч трогает пути за пределами репозитория и не
	// применяется вовсе.
	ErrPatchUnsafe = errors.New("патч трогает пути за пределами репозитория")
	// ErrApplyFailed — правки не наложились; патч остаётся файлом на диске.
	ErrApplyFailed = errors.New("правки не наложились")
)

// patchFileSuffix — расширение файла патча.
const patchFileSuffix = ".patch"

// Patch — найденный патч: Path — файл в кэше, JobID и SHA (коммит джобы, он
// же база мерджа) извлечены из имени файла. Отдельного файла метаданных нет
// намеренно: он стал бы вторым местом, где на диске оседают сведения о
// джобе, и его пришлось бы синхронизировать с именем файла патча.
type Patch struct {
	Path  string
	JobID int64
	SHA   string
}

// FileStat — отчёт по одному файлу разницы. Added и Deleted отрицательны
// для двоичного файла, для которого git не считает строки — вызывающий
// печатает прочерк вместо числа.
type FileStat struct {
	Path    string
	Added   int
	Deleted int
}

// patchesDir — единственная формула каталога патчей в проекте:
// cache.Dir("patches", <безопасный хост>, <безопасные сегменты пути
// проекта>), та же, что у зеркала репозитория (safeCachePath в mirror.go).
//
// Хост и проект входят в путь, а не только идентификатор джобы в имя файла,
// потому что каталог патчей общий на машину: плоский каталог отдавал бы
// «последний сохранённый патч» независимо от того, в каком репозитории стоит
// пользователь, а идентификаторы джоб уникальны только внутри одного
// инстанса GitLab — две джобы с одним номером на разных хостах сталкивались
// бы в одном имени файла. Итог в обоих случаях один: трёхсторонний мердж
// диффа чужого проекта в рабочее дерево пользователя после одного «да».
func patchesDir(host, projectPath string) (string, error) {
	safeHost, safeProject, err := safeCachePath(host, projectPath)
	if err != nil {
		return "", err
	}
	return cache.Dir("patches", safeHost, safeProject)
}

// patchName строит имя файла патча вида job-<id джобы>-<короткий sha>.patch.
// Правится только вместе с parsePatchName — иначе сохранённый вчера патч
// перестал бы находиться сегодня.
func patchName(jobID int64, sha string) string {
	return fmt.Sprintf("job-%d-%s%s", jobID, shortSHA(sha), patchFileSuffix)
}

// parsePatchName разбирает имя файла обратно в Patch (без заполненного
// Path — его знает только вызывающий, у которого есть каталог). Любое
// отклонение от формата job-<id>-<sha>.patch — ложный признак: имя не наше,
// пропускаем его молча.
func parsePatchName(name string) (Patch, bool) {
	if !strings.HasSuffix(name, patchFileSuffix) {
		return Patch{}, false
	}
	base := strings.TrimSuffix(name, patchFileSuffix)
	if !strings.HasPrefix(base, "job-") {
		return Patch{}, false
	}
	rest := strings.TrimPrefix(base, "job-")

	sep := strings.LastIndex(rest, "-")
	if sep < 0 {
		return Patch{}, false
	}
	idPart, shaPart := rest[:sep], rest[sep+1:]
	if shaPart == "" {
		return Patch{}, false
	}
	id, err := strconv.ParseInt(idPart, 10, 64)
	if err != nil {
		return Patch{}, false
	}
	return Patch{JobID: id, SHA: shaPart}, true
}

// Capture снимает правки воспроизведённого чекаута checkoutDir в файл в
// каталоге патчей проекта host/projectPath и возвращает найденный патч и
// список изменённых путей — вызывающему они нужны, чтобы сразу назвать
// пользователю, что именно сохранено.
func Capture(ctx context.Context, checkoutDir, host, projectPath string, jobID int64, sha string) (Patch, []string, error) {
	// Регистрация ещё не отслеживаемых файлов намерением добавить: без неё
	// новый файл, созданный пользователем при починке (новый тест, новый
	// конфиг), в разницу не попал бы вовсе. Отказ этой команды не считается
	// поломкой — разница по отслеживаемым файлам всё равно снимается ниже.
	// Правила игнорирования репозитория соблюдаются самим git, поэтому
	// каталоги сборки в патч не едут; индекс, в который пишет команда,
	// принадлежит именно этому чекауту (у worktree он свой) — репозиторий
	// пользователя не мутируется.
	addCmd := exec.CommandContext(ctx, "git", "-C", checkoutDir, "add", "--intent-to-add", ".")
	_, _ = runCmd(addCmd)

	// Разница снимается относительно HEAD, а не индекса: голый git diff
	// сравнивает рабочее дерево с индексом, поэтому всё, что в этом чекауте
	// уже застейджено — шагом джобы, вызвавшим git add (автофиксеры
	// линтеров, генераторы, pre-commit-обвязка), или самим пользователем во
	// время починки, — в разницу не попадало бы вовсе. Capture при этом
	// возвращала успех, вызывающий печатал «правки сохранены», а
	// непереносимый чекаут удалялся сразу после — потерянное было уже не
	// восстановить. git add --intent-to-add выше по-прежнему нужен: без него
	// в разницу не попадут ещё не отслеживаемые файлы.
	diffCmd := exec.CommandContext(ctx, "git", "-C", checkoutDir, "diff", "--binary", "HEAD")
	diffOut, err := runCmd(diffCmd)
	if err != nil {
		return Patch{}, nil, fmt.Errorf("repo: не удалось снять разницу чекаута: %s: %w", firstLine(err.Error()), err)
	}
	if strings.TrimSpace(diffOut) == "" {
		return Patch{}, nil, fmt.Errorf("repo: %w", ErrNoChanges)
	}

	// Та же база сравнения, что у самой разницы выше: список файлов,
	// названный пользователю, обязан совпадать с содержимым патча.
	namesCmd := exec.CommandContext(ctx, "git", "-C", checkoutDir, "diff", "--name-only", "HEAD")
	namesOut, err := runCmd(namesCmd)
	if err != nil {
		return Patch{}, nil, fmt.Errorf("repo: не удалось получить список изменённых файлов: %s: %w", firstLine(err.Error()), err)
	}
	var paths []string
	for _, line := range strings.Split(namesOut, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			paths = append(paths, trimmed)
		}
	}

	dir, err := patchesDir(host, projectPath)
	if err != nil {
		return Patch{}, nil, err
	}
	target := filepath.Join(dir, patchName(jobID, sha))

	// Атомарная запись тем же приёмом, что файл токенов и файл настроек:
	// временный файл в том же каталоге, явные права 0o600, запись, закрытие,
	// os.Rename поверх целевого пути; при любой ошибке до переименования
	// временный файл удаляется. Права выставляются явно, а не оставляются
	// маске процесса: пользователь мог править файл, в котором лежит
	// секрет, и тогда секрет окажется в теле патча — это единственная
	// причина, по которой файл разницы вообще требует режима 0600.
	tmp, err := os.CreateTemp(dir, ".patch-*.tmp")
	if err != nil {
		return Patch{}, nil, fmt.Errorf("repo: не удалось создать временный файл патча: %w", err)
	}
	tmpPath := tmp.Name()
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return Patch{}, nil, fmt.Errorf("repo: не удалось выставить права 0600 на временный файл патча: %w", err)
	}
	if _, err := tmp.WriteString(diffOut); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return Patch{}, nil, fmt.Errorf("repo: не удалось записать временный файл патча: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return Patch{}, nil, fmt.Errorf("repo: не удалось закрыть временный файл патча: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		os.Remove(tmpPath)
		return Patch{}, nil, fmt.Errorf("repo: не удалось сохранить патч %s: %w", target, err)
	}

	return Patch{Path: target, JobID: jobID, SHA: sha}, paths, nil
}

// LatestPatch возвращает самый свежий по времени изменения патч в каталоге
// патчей проекта host/projectPath. Патчи других проектов и других хостов
// лежат в других каталогах и в выборку не попадают вовсе.
func LatestPatch(host, projectPath string) (Patch, error) {
	dir, err := patchesDir(host, projectPath)
	if err != nil {
		return Patch{}, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Patch{}, fmt.Errorf("repo: %w", ErrNoPatch)
	}

	var best Patch
	var bestTime time.Time
	found := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		p, ok := parsePatchName(entry.Name())
		if !ok {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		if !found || info.ModTime().After(bestTime) {
			p.Path = filepath.Join(dir, entry.Name())
			best = p
			bestTime = info.ModTime()
			found = true
		}
	}
	if !found {
		return Patch{}, fmt.Errorf("repo: %w", ErrNoPatch)
	}
	return best, nil
}

// PatchFor возвращает патч, сохранённый для джобы jobID проекта
// host/projectPath. Идентификатор джобы уникален только внутри одного
// инстанса GitLab, поэтому хост и проект — обязательная часть выборки, а не
// уточнение.
func PatchFor(host, projectPath string, jobID int64) (Patch, error) {
	dir, err := patchesDir(host, projectPath)
	if err != nil {
		return Patch{}, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Patch{}, fmt.Errorf("repo: %w", ErrNoPatch)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		p, ok := parsePatchName(entry.Name())
		if !ok || p.JobID != jobID {
			continue
		}
		p.Path = filepath.Join(dir, entry.Name())
		return p, nil
	}
	return Patch{}, fmt.Errorf("repo: %w", ErrNoPatch)
}

// numstatValue разбирает одно числовое поле вывода --numstat: git печатает
// "-" для двоичного файла вместо числа строк.
func numstatValue(field string) int {
	if field == "-" {
		return -1
	}
	n, err := strconv.Atoi(field)
	if err != nil {
		return -1
	}
	return n
}

// PatchStat разбирает патч patchPath в список FileStat относительно root.
// Команда только читает: ни рабочее дерево, ни индекс она не трогает,
// поэтому её можно звать до подтверждения пользователя.
func PatchStat(ctx context.Context, root, patchPath string) ([]FileStat, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "apply", "--numstat", patchPath)
	out, err := runCmd(cmd)
	if err != nil {
		return nil, fmt.Errorf("repo: не удалось разобрать патч %s: %s: %w", patchPath, firstLine(err.Error()), ErrApplyFailed)
	}

	var stats []FileStat
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		stats = append(stats, FileStat{
			Path:    fields[2],
			Added:   numstatValue(fields[0]),
			Deleted: numstatValue(fields[1]),
		})
	}
	return stats, nil
}

// CheckPaths — единственный предохранитель от записи за пределы
// репозитория: путь отвергается, если он абсолютный или если среди его
// сегментов встречается переход на уровень вверх. Проверка своя, а не
// «git разберётся»: git и так откажется писать наружу, но отказ должен быть
// нашим и понятным, и он обязан случиться до того, как в рабочее дерево
// пользователя ляжет хоть один байт.
func CheckPaths(stats []FileStat) error {
	for _, s := range stats {
		if filepath.IsAbs(s.Path) {
			return fmt.Errorf("repo: путь %s абсолютный: %w", s.Path, ErrPatchUnsafe)
		}
		for _, seg := range strings.Split(s.Path, "/") {
			if seg == ".." {
				return fmt.Errorf("repo: путь %s выходит за пределы репозитория: %w", s.Path, ErrPatchUnsafe)
			}
		}
	}
	return nil
}

// Apply накладывает patchPath на root трёхсторонним мерджем. Больше ни
// одного флага: то, что снимает запрет на пути за пределами репозитория, не
// используется никогда. Ничего не фиксируется в истории — подкоманда git,
// создающая новую запись истории, здесь не вызывается вовсе. Отказ
// оборачивается в ErrApplyFailed с первой строкой сообщения git и путём
// патча — при конфликте пользователь должен получить и причину, и файл, из
// которого можно продолжить руками.
func Apply(ctx context.Context, root, patchPath string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "apply", "--3way", patchPath)
	if _, err := runCmd(cmd); err != nil {
		return fmt.Errorf("repo: %s: патч остался файлом %s: %w", firstLine(err.Error()), patchPath, ErrApplyFailed)
	}
	return nil
}

// AheadCount возвращает число коммитов от sha до HEAD в root. Второе
// значение ложно, когда команда отказала: коммита джобы в этом репозитории
// нет, и говорить про «HEAD ушёл на N вперёд» нечего — ошибка здесь не
// диагноз, а ветка, ровно как hasCommit в mirror.go.
func AheadCount(ctx context.Context, root, sha string) (int, bool) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "rev-list", "--count", sha+"..HEAD")
	out, err := runCmd(cmd)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, false
	}
	return n, true
}
