// Package runner поднимает контейнер образа джобы и даёт пользователю
// интерактивный шелл внутри него.
//
// Доступ к Docker — шелл-аут в CLI через os/exec. Клиентская библиотека
// Docker намеренно не подключается: она утянула бы в go.mod десятки
// зависимостей ради того же, что делает бинарь docker, который у
// пользователя уже есть и уже настроен на нужный демон.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/f3rym/ci-shell/internal/render"
)

// Sentinel-ошибки пакета.
var (
	// ErrDockerUnavailable — бинарь docker не найден в PATH.
	ErrDockerUnavailable = errors.New("docker не найден в PATH")
	// ErrDaemonUnavailable — демон Docker не отвечает.
	ErrDaemonUnavailable = errors.New("демон Docker не отвечает")
	// ErrNoImage — у джобы нет образа (shell-executor или образ по
	// умолчанию ранера, вне видимости API).
	ErrNoImage = errors.New("у джобы нет образа")
	// ErrImageUnavailable — образ не удалось стянуть.
	ErrImageUnavailable = errors.New("образ недоступен")
	// ErrNoShell — в образе нет ни bash, ни sh.
	ErrNoShell = errors.New("в образе нет интерактивной оболочки")
	// ErrContainerFailed — контейнер не поднялся.
	ErrContainerFailed = errors.New("контейнер не поднялся")
)

// containerCLI — имя бинаря контейнерного рантайма. По умолчанию docker;
// подменяется на этапе сборки (например, на Apple `container` для macOS):
//
//	go build -ldflags "-X github.com/f3rym/ci-shell/internal/runner.containerCLI=container"
//
// CLI обязан быть docker-совместимым по используемым подкомандам:
// version/image inspect/pull/create/start/exec/rm.
var containerCLI = "docker"

// Docker — точка доступа к локальному демону через docker-совместимый CLI.
type Docker struct {
	bin      string
	progress render.Progress
}

// New ищет контейнерный CLI в PATH. Демон при этом не дёргается: проверка
// PATH мгновенная, а «CLI установлен» и «демон отвечает» — разные поломки с
// разными подсказками (см. Ping).
func New(p render.Progress) (*Docker, error) {
	bin, err := exec.LookPath(containerCLI)
	if err != nil {
		return nil, fmt.Errorf("runner: %s не найден в PATH: %w", containerCLI, ErrDockerUnavailable)
	}
	return &Docker{bin: bin, progress: p}, nil
}

// Ping проверяет, что демон Docker отвечает.
func (d *Docker) Ping(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, d.bin, "version", "--format", "{{.Server.Version}}")
	if _, err := runCaptured(cmd); err != nil {
		return fmt.Errorf("runner: демон Docker не отвечает: %s: %w", err, ErrDaemonUnavailable)
	}
	return nil
}

// EnsureImage проверяет наличие образа image локально и тянет его при
// необходимости. Уже загруженный образ повторно не тянется — это и есть
// кэширование между запусками из idea §7: тринадцатигигабайтный образ
// тянется один раз.
func (d *Docker) EnsureImage(ctx context.Context, image string) error {
	if image == "" {
		return fmt.Errorf("runner: у джобы нет образа: %w", ErrNoImage)
	}

	inspectCmd := exec.CommandContext(ctx, d.bin, "image", "inspect", image)
	if _, err := runCaptured(inspectCmd); err == nil {
		d.progress.Stage("образ %s уже есть локально", image)
		return nil
	}

	d.progress.Stage("тяну образ %s…", image)
	pullCmd := exec.CommandContext(ctx, d.bin, "pull", image)
	pullCmd.Stdout = d.progress.W
	pullCmd.Stderr = d.progress.W
	if err := pullCmd.Run(); err != nil {
		// Прерванная Ctrl-C тяга — не «образ недоступен» с советом про
		// docker login, а отмена: возвращаем её раньше доменной ошибки.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("runner: не удалось стянуть образ %s: %w", image, ErrImageUnavailable)
	}
	return nil
}

// Spec — параметры подъёма контейнера.
type Spec struct {
	Image     string
	SourceDir string
	WorkDir   string
	Env       map[string]string
	JobID     int64
}

// Container — поднятый контейнер джобы.
type Container struct {
	ID string

	d       *Docker
	workDir string
	shell   string
}

// holdCommand удерживает контейнер запущенным, пока пользователь работает
// внутри него. "sleep infinity" отсутствует в busybox старых сборок — при
// отказе выполняется бесконечный цикл посекундных пауз.
const holdCommand = "sleep infinity || while :; do sleep 1; done"

// Start поднимает контейнер образа spec.Image с примонтированным
// spec.SourceDir в spec.WorkDir и окружением spec.Env, переданным
// исключительно файлом --env-file. Передача значений отдельными флагами
// окружения docker запрещена: аргументы процесса видны любому пользователю
// машины в выводе ps, и это прямая утечка секретов, собранных Фазой 2.
func (d *Docker) Start(ctx context.Context, spec Spec) (*Container, []string, error) {
	envPath, skipped, err := writeEnvFile(spec.Env)
	if err != nil {
		return nil, nil, err
	}
	// Файл окружения нужен только на момент создания контейнера: docker
	// копирует переменные в конфигурацию контейнера, дальше файл — только
	// лишний носитель секретов на диске.
	defer os.Remove(envPath)

	mount := spec.SourceDir + ":" + spec.WorkDir
	label := fmt.Sprintf("ci-shell.job=%d", spec.JobID)

	// Создание и старт разнесены намеренно: docker create надёжно печатает
	// id ещё до попытки старта, поэтому контейнер, не сумевший стартовать
	// (типовой случай — в образе нет sh для переопределённого entrypoint),
	// не остаётся на машине в состоянии Created — обе ветки отказа ниже
	// добивают его по захваченному id. Одиночный docker run -d id при
	// отказе старта не гарантирует, и контейнер утекал бы навсегда:
	// defer c.Remove регистрируется вызывающим только после успеха Start.
	createCmd := exec.CommandContext(ctx, d.bin,
		"create",
		"--env-file", envPath,
		"-v", mount,
		"-w", spec.WorkDir,
		"--label", label,
		"--entrypoint", "sh",
		spec.Image,
		"-c", holdCommand,
	)
	out, err := runCaptured(createCmd)
	id := strings.TrimSpace(out)
	if err != nil || id == "" {
		if id != "" {
			// Демон мог создать контейнер и при ненулевом коде выхода.
			rm := exec.CommandContext(context.Background(), d.bin, "rm", "-f", id)
			_, _ = runCaptured(rm)
		}
		return nil, nil, fmt.Errorf("runner: docker create отказал: %s: %w", err, ErrContainerFailed)
	}

	d.progress.Stage("поднимаю контейнер из образа %s…", spec.Image)
	startCmd := exec.CommandContext(ctx, d.bin, "start", id)
	if _, err := runCaptured(startCmd); err != nil {
		// Фоновый контекст: созданный контейнер убирается и тогда, когда
		// старт сорвала отмена основного контекста.
		rm := exec.CommandContext(context.Background(), d.bin, "rm", "-f", id)
		_, _ = runCaptured(rm)
		return nil, nil, fmt.Errorf("runner: контейнер %s не стартовал: %s: %w", shortID(id), err, ErrContainerFailed)
	}
	d.progress.Stage("контейнер %s готов, рабочий каталог %s", shortID(id), spec.WorkDir)

	return &Container{ID: id, d: d, workDir: spec.WorkDir}, skipped, nil
}

// DetectShell выбирает оболочку контейнера пробой: bash предпочтителен —
// docker-executor GitLab исполняет скрипт джобы bash-ем, когда тот есть в
// образе, — sh остаётся фолбэком. Найденная оболочка запоминается в
// контейнере и используется и для шагов (Exec), и для интерактива (Shell):
// шаги с башизмами ([[ ]], массивы, set -o pipefail, source) не должны
// падать там, где CI их исполнял без ошибок. Вызывается до прогона шагов.
func (c *Container) DetectShell(ctx context.Context) error {
	for _, candidate := range []string{"bash", "sh"} {
		probe := exec.CommandContext(ctx, c.d.bin, "exec", c.ID, "sh", "-c", "command -v "+candidate)
		if _, err := runCaptured(probe); err == nil {
			c.shell = candidate
			return nil
		}
		// Проба, убитая отменой контекста, не означает отсутствие оболочки
		// в образе — иначе Ctrl-C давал бы ложный вердикт ErrNoShell «джоба
		// не воспроизводится этой утилитой».
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return fmt.Errorf("runner: в образе нет ни bash, ни sh: %w", ErrNoShell)
}

// Exec запускает command внутри контейнера через docker exec оболочкой,
// выбранной DetectShell (до пробы — sh), направляя потоки команды в out и
// errOut. Возвращает код выхода команды. Ошибка возвращается только на
// сбой самого docker: ненулевой код шага — это не поломка утилиты, а ровно
// тот результат, ради которого её запустили.
func (c *Container) Exec(ctx context.Context, command string, out, errOut io.Writer) (int, error) {
	shell := c.shell
	if shell == "" {
		shell = "sh"
	}
	cmd := exec.CommandContext(ctx, c.d.bin, "exec", "-w", c.workDir, c.ID, shell, "-c", command)
	cmd.Stdout = out
	cmd.Stderr = errOut
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	// Отмена контекста (Ctrl-C) убивает docker exec сигналом, и ExitCode()
	// вернул бы -1 — «шаг упал с кодом -1» вместо честного прерывания.
	// Отмена проверяется раньше разбора кода выхода и уходит наверх ошибкой:
	// прогон шагов останавливается, очистка отрабатывает.
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return -1, fmt.Errorf("runner: docker exec отказал: %w", err)
}

// Shell запускает оболочку, выбранную DetectShell, интерактивно, подключая
// терминал пользователя напрямую к docker exec -it. Если проба ещё не
// выполнялась — выполняет её сам. Ненулевой код выхода оболочки не
// считается ошибкой — пользователь вышел как счёл нужным.
func (c *Container) Shell(ctx context.Context) error {
	if c.shell == "" {
		if err := c.DetectShell(ctx); err != nil {
			return err
		}
	}

	cmd := exec.CommandContext(ctx, c.d.bin, "exec", "-it", "-w", c.workDir, c.ID, c.shell)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Терминал разруливает сам docker CLI — утилита в этот обмен не
	// вмешивается; код выхода оболочки не проверяется намеренно.
	_ = cmd.Run()
	return nil
}

// Remove удаляет контейнер (docker rm -f), заглушая вывод. Вызывается из
// defer при разворачивании.
func (c *Container) Remove(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, c.d.bin, "rm", "-f", c.ID)
	_, err := runCaptured(cmd)
	if err != nil {
		return fmt.Errorf("runner: не удалось удалить контейнер %s: %w", shortID(c.ID), err)
	}
	return nil
}

// runCaptured запускает уже собранную команду cmd и возвращает stdout. При
// ненулевом коде возврата — ошибку с первой строкой stderr.
func runCaptured(cmd *exec.Cmd) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := firstLine(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), errors.New(msg)
	}
	return stdout.String(), nil
}

// firstLine возвращает первую непустую строку s.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// shortID обрезает id контейнера до двенадцати символов для печати.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
