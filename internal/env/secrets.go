package env

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/f3rym/ci-shell/internal/config"
)

// ErrInsecureSecrets означает: файл секретов найден на диске, но его права
// шире 0600, поэтому читать его небезопасно (T-02-08, T-02-12).
var ErrInsecureSecrets = errors.New("права файла секретов шире 0600")

// secretsFile — верхний уровень файла секретов: карта проектов
// (<хост>/<путь проекта>) на карту их переменных.
type secretsFile struct {
	Projects map[string]map[string]string `yaml:"projects"`
}

// Secrets — результат чтения локального файла секретов для одного проекта.
type Secrets struct {
	// Path — фактический путь к файлу секретов: существующий, если он есть
	// на диске, иначе первое по порядку предпочтения имя. Нужен, чтобы
	// сообщение пользователю называло настоящий файл, а не шаблон.
	Path string
	// Values — значения переменных проекта, если файл найден и содержит
	// для него запись. Пустая карта (не nil), если файл найден, но записи
	// для проекта нет.
	Values map[string]string
	// Found — файл секретов существует на диске. Отсутствие файла — не
	// ошибка, а штатная ситуация «пользователь ещё не заполнял».
	Found bool
}

// secretsNames — имена файла секретов в порядке предпочтения: если оба
// существуют, побеждает первый.
var secretsNames = []string{"secrets.yml", "secrets.yaml"}

// SecretsPath возвращает путь к файлу секретов — каталог берётся у
// config.Dir(), той же формулы, что использует token.configPath
// (internal/token/file.go). Если ни одно из secretsNames не существует на
// диске, возвращается путь к первому имени (secrets.yml) без ошибки —
// отсутствие файла секретов не является ошибкой на этом уровне. Экспортирована,
// потому что тот же путь нужен подкоманде ci secrets (cmd/ci/main.go) — вторая
// формула пути в проекте недопустима.
func SecretsPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", fmt.Errorf("env: %w", err)
	}

	first := filepath.Join(dir, secretsNames[0])
	for _, name := range secretsNames {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return first, nil
}

// LoadSecrets читает локальный файл секретов для проекта host+projectPath.
//
// Отсутствие файла — штатная ситуация: возвращается Secrets{Found: false}
// без ошибки, утилита продолжает работать. Права файла проверяются
// строго до чтения содержимого: файл, доступный группе или остальным
// (Perm()&0o077 != 0), не читается вовсе — содержимое не попадает в память
// процесса, возвращается только ErrInsecureSecrets с путём и подсказкой
// chmod (T-02-08, T-02-12). Ключ проекта в файле ищется точным совпадением
// host + "/" + projectPath; если записи для проекта нет, возвращается
// Secrets{Found: true} с пустой картой значений — файл есть, записи нет.
//
// Пакет ничего не выводит на экран: решение о показе (в том числе о том,
// что файл небезопасен или недостающие переменные) принимает вызывающий
// код (cmd/ci, internal/render) — так значение секрета не может утечь из
// низкоуровневого кода мимо единой политики вывода.
func LoadSecrets(host, projectPath string) (Secrets, error) {
	path, err := SecretsPath()
	if err != nil {
		return Secrets{}, err
	}

	// «Файла нет» — только fs.ErrNotExist. Любая другая ошибка stat
	// (EACCES, I/O, повисший automount) — это существующий, но недоступный
	// файл: молча счесть его отсутствующим значило бы предложить
	// пользователю создать файл, который уже есть (WR-03). *PathError
	// содержит только путь, не содержимое — оборачивать безопасно.
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Secrets{Path: path, Found: false}, nil
	}
	if err != nil {
		return Secrets{Path: path}, fmt.Errorf("env: файл секретов %s недоступен: %w", path, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		// Path заполнен даже в ошибке: содержимое не прочитано (риск
		// закрыт), но вызывающий код всё равно должен уметь сослаться на
		// путь файла и в предупреждении, и в отчёте о недостающих
		// переменных.
		return Secrets{Path: path}, fmt.Errorf(
			"env: файл секретов %s имеет слишком широкие права (%s), почините: chmod 600 %s: %w",
			path, info.Mode().Perm(), path, ErrInsecureSecrets,
		)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Secrets{Path: path}, fmt.Errorf("env: чтение файла секретов %s: %w", path, err)
	}

	var f secretsFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		// Текст ошибки yaml.v3 не пробрасывается: ошибки типов встраивают в
		// сообщение значение узла (например, `cannot unmarshal !!int `1234``),
		// а значения в этом файле — секреты. Наружу уходят только имя файла
		// и номера строк (WR-02).
		return Secrets{Path: path}, fmt.Errorf(
			"env: файл секретов %s не разобран как YAML%s; текст ошибки скрыт — он может содержать значения секретов; проверьте синтаксис и кавычки вокруг значений",
			path, yamlErrorLines(err),
		)
	}

	values := f.Projects[host+"/"+projectPath]

	return Secrets{Path: path, Values: values, Found: true}, nil
}

// EnsureSecretsFile создаёт файл секретов, уже заполненный именами
// недостающих переменных missing, если файла ещё нет на диске.
//
// Существующий файл не читается и не переписывается вовсе: возвращается его
// путь и ложный признак создания. В этом файле живут комментарии
// пользователя, значения других проектов и его собственный порядок ключей —
// переписывание превратило бы удобство в потерю данных, а недостающие ключи
// и так печатаются готовым к вставке фрагментом (missingReport в
// internal/render/env.go).
//
// Пустой host или пустой projectPath (случай подкоманды ci secrets на
// чистой машине, где проект ещё не известен) — вместо настоящего ключа
// проекта пишется закомментированный пример той же формы: файл остаётся
// валидным YAML, а пользователь видит формат.
//
// Содержимое собирается текстом построчно, а не сериализацией структуры —
// только текстовая сборка позволяет положить в файл шапку с пояснением и
// оставить её там навсегда. Запись атомарна тем же приёмом, что у токенов и
// настроек (internal/token/file.go, internal/config/config.go): временный
// файл через os.CreateTemp в том же каталоге, явный os.Chmod на 0o600
// (маска процесса иначе оставила бы файл читаемым всей машине), запись,
// закрытие, os.Rename поверх целевого пути; при любой ошибке до
// переименования временный файл удаляется.
//
// В файл не может попасть ни одно значение: единственный вход, несущий
// сведения о переменных, — срез Missing, у которого поля под значение нет по
// устройству типа.
//
// Пакет по-прежнему ничего не выводит на экран.
func EnsureSecretsFile(host, projectPath string, missing []Missing) (path string, created bool, err error) {
	path, err = SecretsPath()
	if err != nil {
		return "", false, err
	}

	if _, statErr := os.Stat(path); statErr == nil {
		return path, false, nil
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return path, false, fmt.Errorf("env: файл секретов %s недоступен: %w", path, statErr)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false, fmt.Errorf("env: не удалось создать каталог %s: %w", dir, err)
	}

	var b strings.Builder
	b.WriteString("# файл секретов ci-shell.\n")
	b.WriteString("# здесь хранятся значения переменных, которые GitLab никогда не отдаёт\n")
	b.WriteString("# через API (тип masked_and_hidden — пишутся один раз и не читаются никем).\n")
	b.WriteString("# права файла обязаны быть 0600 — иначе утилита отказывается его читать.\n")
	b.WriteString("# открыть файл в редакторе: ci secrets\n")
	b.WriteString("projects:\n")

	if host == "" || projectPath == "" {
		b.WriteString("  # <хост>/<путь проекта>:\n")
		b.WriteString("  #   ПЕРЕМЕННАЯ: \"<значение>\"\n")
	} else {
		fmt.Fprintf(&b, "  %s/%s:\n", host, projectPath)
		for _, m := range missing {
			fmt.Fprintf(&b, "    %s: \"\"\n", m.Key)
		}
	}

	tmp, err := os.CreateTemp(dir, ".secrets-*.yml.tmp")
	if err != nil {
		return "", false, fmt.Errorf("env: не удалось создать временный файл секретов: %w", err)
	}
	tmpPath := tmp.Name()
	// Право читаться не должно зависеть от umask процесса — Chmod делает
	// намерение явным в коде (тот же приём, что в token.Save/config.Save).
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", false, fmt.Errorf("env: не удалось выставить права 0600 на временный файл секретов: %w", err)
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", false, fmt.Errorf("env: не удалось записать временный файл секретов: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", false, fmt.Errorf("env: не удалось закрыть временный файл секретов: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return "", false, fmt.Errorf("env: не удалось сохранить файл секретов %s: %w", path, err)
	}

	return path, true, nil
}

// yamlLineRe находит в тексте ошибки yaml.v3 упоминания «line N» — из всей
// ошибки пользователю можно показывать только номера строк: остальной текст
// может содержать значения узлов.
var yamlLineRe = regexp.MustCompile(`line (\d+)`)

// yamlErrorLines извлекает из ошибки yaml.v3 номера строк и форматирует их
// суффиксом вида " (строки: 3, 7)". Пустая строка, если номеров в ошибке
// нет. Сам текст ошибки никогда не возвращается (WR-02).
func yamlErrorLines(err error) string {
	matches := yamlLineRe.FindAllStringSubmatch(err.Error(), -1)
	if len(matches) == 0 {
		return ""
	}
	lines := make([]string, 0, len(matches))
	for _, m := range matches {
		lines = append(lines, m[1])
	}
	return " (строки: " + strings.Join(lines, ", ") + ")"
}
