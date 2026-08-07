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

// secretsPath возвращает путь к файлу секретов — по образцу
// token.configPath (internal/token/file.go): каталог из XDG_CONFIG_HOME,
// иначе $HOME/.config, подкаталог ci-shell. Если ни одно из secretsNames
// не существует на диске, возвращается путь к первому имени
// (secrets.yml) без ошибки — отсутствие файла секретов не является
// ошибкой на этом уровне.
func secretsPath() (string, error) {
	var dir string
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		dir = filepath.Join(xdg, "ci-shell")
	} else {
		home := os.Getenv("HOME")
		if home == "" {
			return "", fmt.Errorf("env: не удалось определить каталог конфига: не заданы ни XDG_CONFIG_HOME, ни HOME")
		}
		dir = filepath.Join(home, ".config", "ci-shell")
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
	path, err := secretsPath()
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
