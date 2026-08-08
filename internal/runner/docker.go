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
	"path/filepath"
	"strings"

	"github.com/f3rym/ci-shell/internal/event"
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
	bin string
	// em — переносчик типизированных событий: тяга образа, подъём и
	// готовность контейнера уходят структурами, а не строками.
	em event.Emitter
	// raw — сквозной писатель для потоков дочерних процессов, которые мы не
	// разбираем и не превращаем в события: полоса прогресса `docker pull`
	// не наша, мы её не формируем и не парсим по строке на событие
	// (тринадцатигигабайтный образ не заваливает подписчика).
	raw io.Writer
}

// New ищет контейнерный CLI в PATH. Демон при этом не дёргается: проверка
// PATH мгновенная, а «CLI установлен» и «демон отвечает» — разные поломки с
// разными подсказками (см. Ping).
func New(em event.Emitter, raw io.Writer) (*Docker, error) {
	bin, err := exec.LookPath(containerCLI)
	if err != nil {
		return nil, fmt.Errorf("runner: %s не найден в PATH: %w", containerCLI, ErrDockerUnavailable)
	}
	return &Docker{bin: bin, em: em, raw: raw}, nil
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
		d.em.Emit(event.ImageLocal{Image: image})
		return nil
	}

	d.em.Emit(event.ImagePulling{Image: image})
	pullCmd := exec.CommandContext(ctx, d.bin, "pull", image)
	pullCmd.Stdout = d.raw
	pullCmd.Stderr = d.raw
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
	// FileVarsDir — каталог на хосте с материализованными файловыми
	// переменными (internal/runner/filevars.go). Пусто, когда
	// материализовывать было нечего — тогда второе монтирование не
	// добавляется вовсе.
	FileVarsDir string
	// FileVarsMount — каталог внутри контейнера, куда монтируется
	// FileVarsDir: сосед рабочего каталога, а не подкаталог кода — каталог с
	// кодом это git-чекаут, из которого Фаза 7 будет забирать правки
	// пользователя, и секрет, положенный туда, показался бы в состоянии
	// репозитория и мог уехать в коммит.
	FileVarsMount string
}

// Container — поднятый контейнер джобы.
type Container struct {
	ID string

	d       *Docker
	workDir string
	shell   string
	// removed отражает, что контейнер уже снят: Remove идемпотентен,
	// потому что с Фазы 7 контейнер снимается явным вызовом до старта
	// чистого прогона, а отложенный вызов у исходного вызывающего остаётся
	// на месте — без идемпотентности второй вызов печатал бы пользователю
	// предупреждение о невозможности снять уже снятый контейнер и советовал
	// бы добить его руками, хотя добивать уже нечего.
	removed bool
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

	// Аргумент монтирования собирается конкатенацией через двоеточие, потому
	// что такова форма флага -v контейнерного CLI. Значит двоеточие внутри
	// любой из половин пересобирает аргумент во что-то другое: рабочий
	// каталог "/builds/p:ro" сделал бы монтирование доступным только для
	// чтения (и правки пользователя не попали бы на хост вовсе), а каталог
	// "a:b" на хосте сорвал бы запуск. Пустая половина даёт монтирование в
	// корень и рабочий каталог "". Оба значения приходят снаружи —
	// SourceDir из настройки checkout_dir или текущего каталога,
	// WorkDir из CI_PROJECT_DIR, то есть из переменной проекта по API, —
	// поэтому проверяются здесь, у самой сборки аргумента, а не только у
	// источника (см. env.validWorkDir).
	if err := checkHostPath("каталог кода на хосте", spec.SourceDir); err != nil {
		return nil, nil, err
	}
	if err := checkContainerPath("рабочий каталог в контейнере", spec.WorkDir); err != nil {
		return nil, nil, err
	}
	if spec.FileVarsDir != "" && spec.FileVarsMount != "" {
		if err := checkHostPath("каталог файловых переменных на хосте", spec.FileVarsDir); err != nil {
			return nil, nil, err
		}
		if err := checkContainerPath("точка монтирования файловых переменных", spec.FileVarsMount); err != nil {
			return nil, nil, err
		}
	}

	mount := spec.SourceDir + ":" + spec.WorkDir
	label := fmt.Sprintf("ci-shell.job=%d", spec.JobID)

	// Создание и старт разнесены намеренно: docker create надёжно печатает
	// id ещё до попытки старта, поэтому контейнер, не сумевший стартовать
	// (типовой случай — в образе нет sh для переопределённого entrypoint),
	// не остаётся на машине в состоянии Created — обе ветки отказа ниже
	// добивают его по захваченному id. Одиночный docker run -d id при
	// отказе старта не гарантирует, и контейнер утекал бы навсегда:
	// defer c.Remove регистрируется вызывающим только после успеха Start.
	// Аргумент монтирования файловых переменных необязательный (пусто, когда
	// материализовывать было нечего), поэтому собирается в отдельный срез, а
	// не вписывается прямо в вызов, как остальные.
	args := []string{
		"create",
		"--env-file", envPath,
		"-v", mount,
	}
	if spec.FileVarsDir != "" && spec.FileVarsMount != "" {
		args = append(args, "-v", spec.FileVarsDir+":"+spec.FileVarsMount)
	}
	args = append(args,
		"-w", spec.WorkDir,
		"--label", label,
		"--entrypoint", "sh",
		spec.Image,
		"-c", holdCommand,
	)
	createCmd := exec.CommandContext(ctx, d.bin, args...)
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

	d.em.Emit(event.ContainerStarting{Image: spec.Image})
	startCmd := exec.CommandContext(ctx, d.bin, "start", id)
	if _, err := runCaptured(startCmd); err != nil {
		// Фоновый контекст: созданный контейнер убирается и тогда, когда
		// старт сорвала отмена основного контекста.
		rm := exec.CommandContext(context.Background(), d.bin, "rm", "-f", id)
		_, _ = runCaptured(rm)
		return nil, nil, fmt.Errorf("runner: контейнер %s не стартовал: %s: %w", shortID(id), err, ErrContainerFailed)
	}
	d.em.Emit(event.ContainerReady{ID: id, WorkDir: spec.WorkDir})

	return &Container{ID: id, d: d, workDir: spec.WorkDir}, skipped, nil
}

// checkControl отвергает путь с управляющим символом: такой путь ломает и
// разбор аргумента, и любое сообщение о нём.
func checkControl(what, p string) error {
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("runner: %s %q содержит управляющий символ: %w", what, p, ErrContainerFailed)
		}
	}
	return nil
}

// checkContainerPath отвергает путь внутри контейнера, который нельзя
// безопасно подставить в аргумент монтирования вида "<источник>:<цель>":
// пустой, относительный, с двоеточием или с управляющим символом. Файловая
// система контейнера всегда POSIX, поэтому абсолютность проверяется ведущей
// косой чертой, а не filepath.IsAbs сборки хоста. what — роль пути в
// сообщении пользователю.
func checkContainerPath(what, p string) error {
	switch {
	case p == "":
		return fmt.Errorf("runner: %s не задан: %w", what, ErrContainerFailed)
	case !strings.HasPrefix(p, "/"):
		return fmt.Errorf("runner: %s %q не абсолютный: %w", what, p, ErrContainerFailed)
	case strings.ContainsRune(p, ':'):
		return fmt.Errorf("runner: %s %q содержит двоеточие — оно разделяет части аргумента монтирования: %w", what, p, ErrContainerFailed)
	}
	return checkControl(what, p)
}

// checkHostPath — то же для пути на хосте, где абсолютность зависит от
// платформы сборки. Двоеточие буквы диска Windows ("C:\...") допускается
// как часть формы абсолютного пути и из проверки исключается — любое другое
// двоеточие пересобрало бы аргумент монтирования.
func checkHostPath(what, p string) error {
	if p == "" {
		return fmt.Errorf("runner: %s не задан: %w", what, ErrContainerFailed)
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("runner: %s %q не абсолютный: %w", what, p, ErrContainerFailed)
	}
	rest := p
	if len(p) > 1 && p[1] == ':' {
		rest = p[2:]
	}
	if strings.ContainsRune(rest, ':') {
		return fmt.Errorf("runner: %s %q содержит двоеточие — оно разделяет части аргумента монтирования: %w", what, p, ErrContainerFailed)
	}
	return checkControl(what, p)
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

// ShellCommand собирает команду интерактивного шелла контейнера как
// значение, не запуская её: контейнерный CLI, подкоманда выполнения, флаги
// интерактивности и рабочего каталога, идентификатор контейнера и найденная
// оболочка — каждый отдельным элементом среза аргументов, оболочка на хосте
// не задействована. Если проба оболочки ещё не выполнялась, метод выполняет
// её сам, как раньше делал Shell.
//
// Стандартные потоки метод НЕ назначает намеренно: команду забирает механизм
// передачи терминала Bubble Tea (internal/ui), и он сам подключает к ней
// терминал пользователя — назначенные заранее потоки отняли бы у него это
// право. Обычный режим (Shell ниже) назначает их сам, сразу после сборки.
func (c *Container) ShellCommand(ctx context.Context) (*exec.Cmd, error) {
	if c.shell == "" {
		if err := c.DetectShell(ctx); err != nil {
			return nil, err
		}
	}
	return exec.CommandContext(ctx, c.d.bin, "exec", "-it", "-w", c.workDir, c.ID, c.shell), nil
}

// Shell запускает оболочку интерактивно, подключая терминал пользователя
// напрямую к команде, собранной ShellCommand. Ненулевой код выхода оболочки
// не считается ошибкой — пользователь вышел как счёл нужным.
func (c *Container) Shell(ctx context.Context) error {
	cmd, err := c.ShellCommand(ctx)
	if err != nil {
		return err
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Терминал разруливает сам docker CLI — утилита в этот обмен не
	// вмешивается. Ненулевой код выхода самой оболочки игнорируется
	// намеренно: пользователь вышел как счёл нужным. А вот случай, когда
	// docker exec не запустился вовсе (нет терминала, контейнер умер между
	// стартом и exec, демон перезапустился), кодом выхода не описывается и
	// молчать о нём нельзя: пользователь не получил бы шелла, не увидел бы
	// ошибки, и утилита пошла бы дальше печатать «правок в чекауте нет» и
	// убирать за собой, как будто сессия состоялась. Различает эти два случая
	// тип ошибки: *exec.ExitError — это код выхода, всё остальное — сорванный
	// запуск.
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return fmt.Errorf("runner: не удалось открыть шелл в контейнере %s: %w", shortID(c.ID), err)
		}
	}
	return nil
}

// MkdirAll создаёt каталог dir внутри контейнера рекурсивно (docker exec с
// mkdir -p). Путь идёт отдельным элементом среза аргументов — оболочка
// внутри контейнера здесь не задействована вовсе.
func (c *Container) MkdirAll(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, c.d.bin, "exec", c.ID, "mkdir", "-p", dir)
	if _, err := runCaptured(cmd); err != nil {
		return fmt.Errorf("runner: не удалось создать каталог %s в контейнере: %s", dir, err)
	}
	return nil
}

// WriteFile пишет content в файл path внутри контейнера через tee: путь
// идёт отдельным элементом среза аргументов exec, а содержимое —
// стандартным вводом дочернего процесса (cmd.Stdin), а не аргументом.
// Причина: аргументы дочерних процессов контейнерного CLI видны в таблице
// процессов машины любому её пользователю, а оболочка с перенаправлением
// (sh -c "cat > path") потребовала бы склейки content в строку команды и
// вернула бы ровно ту же утечку. Стандартный вывод tee отправляется в
// io.Discard — иначе tee продублировал бы записанное содержимое в
// терминал пользователя.
func (c *Container) WriteFile(ctx context.Context, path, content string) error {
	cmd := exec.CommandContext(ctx, c.d.bin, "exec", "-i", c.ID, "tee", path)
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := firstLine(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("runner: не удалось записать файл %s в контейнере: %s", path, msg)
	}
	return nil
}

// Chmod меняет права файла path внутри контейнера. Режим и путь — отдельные
// элементы среза аргументов exec.
func (c *Container) Chmod(ctx context.Context, mode, path string) error {
	cmd := exec.CommandContext(ctx, c.d.bin, "exec", c.ID, "chmod", mode, path)
	if _, err := runCaptured(cmd); err != nil {
		return fmt.Errorf("runner: не удалось сменить права %s файлу %s в контейнере: %s", mode, path, err)
	}
	return nil
}

// Chown рекурсивно возвращает владельца owner (в форме "<uid>:<gid>")
// каталогу path внутри контейнера. Владелец и путь — отдельные элементы
// среза аргументов exec, ровно как в MkdirAll и Chmod: оболочка внутри
// контейнера не задействована вовсе. Это не стилистика, а граница доверия —
// path приходит из CI_PROJECT_DIR, то есть из переменной проекта, группы
// или файла секретов, и склейка его в строку для sh -c означала бы
// исполнение произвольной команды под root в каталоге, смонтированном с
// хоста (при --here — прямо в рабочей копии пользователя).
func (c *Container) Chown(ctx context.Context, owner, path string) error {
	cmd := exec.CommandContext(ctx, c.d.bin, "exec", c.ID, "chown", "-R", owner, path)
	if _, err := runCaptured(cmd); err != nil {
		return fmt.Errorf("runner: не удалось вернуть владельца %s каталогу %s в контейнере: %s", owner, path, err)
	}
	return nil
}

// Remove удаляет контейнер (docker rm -f), заглушая вывод. Вызывается из
// defer при разворачивании. Идемпотентен: повторный вызов после успешного
// снятия ничего не делает и не возвращает ошибку — с Фазы 7 контейнер
// снимается явным вызовом до старта чистого прогона, а отложенное снятие
// у исходного вызывающего остаётся зарегистрированным и срабатывает позже.
func (c *Container) Remove(ctx context.Context) error {
	if c.removed {
		return nil
	}
	cmd := exec.CommandContext(ctx, c.d.bin, "rm", "-f", c.ID)
	_, err := runCaptured(cmd)
	if err != nil {
		return fmt.Errorf("runner: не удалось удалить контейнер %s: %w", shortID(c.ID), err)
	}
	c.removed = true
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
