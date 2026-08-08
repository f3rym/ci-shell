// Package config хранит настройки пользователя, не относящиеся к
// секретам: единственная запись сейчас — каталог для чекаутов кода джобы
// (REPO-04).
//
// Файл настроек — отдельный файл, а не соседство с токенами в config.yml:
// файл токенов принадлежит пакету token, у него своя политика прав и свой
// формат, и подмешивать в него настройки означало бы завести второй путь
// чтения секретов. Полей под секреты в Settings нет и появляться не
// должно: на этом стоит решение не проверять права файла при чтении —
// защищать в нём нечего, а отказ за права на файл без секретов был бы
// новым трением на ровном месте.
//
// Dir — единственная формула каталога конфига в проекте: token.configPath
// и env.secretsPath берут каталог у неё, а не наоборот. Пакет config — сам
// нижний уровень и не зависит ни от одного другого внутреннего пакета
// проекта.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ErrUnavailable — ни XDG_CONFIG_HOME, ни HOME не заданы: каталог конфига
// вычислить невозможно.
var ErrUnavailable = errors.New("не удалось определить каталог конфига")

// Dir возвращает каталог конфига утилиты: непустой XDG_CONFIG_HOME даёт
// <он>/ci-shell, иначе HOME даёт <HOME>/.config/ci-shell. Каталог здесь не
// создаётся: чтение настроек и вычисление пути не должны оставлять следов
// на машине — каталог создаёт только Save.
func Dir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "ci-shell"), nil
	}
	home := os.Getenv("HOME")
	if home == "" {
		return "", fmt.Errorf("config: не заданы ни XDG_CONFIG_HOME, ни HOME: %w", ErrUnavailable)
	}
	return filepath.Join(home, ".config", "ci-shell"), nil
}

// Path возвращает путь файла настроек: settings.yml в каталоге от Dir().
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.yml"), nil
}

// Settings — настройки пользователя. Полей под секреты здесь нет и
// появляться не должно.
type Settings struct {
	// CheckoutDir — каталог, внутри которого создаются чекауты кода джобы.
	// Пустое значение означает «пользователь ещё не выбрал»: политику
	// выбора по умолчанию знает вызывающий код (cmd/ci), а не этот пакет.
	CheckoutDir string `yaml:"checkout_dir"`
}

// Load читает файл настроек и возвращает разобранные настройки вместе с
// путём — путь нужен вызывающему, чтобы назвать его пользователю.
// Отсутствие файла — штатная ситуация: возвращаются нулевые настройки, путь
// и отсутствие ошибки. Ошибка разбора YAML возвращается наружу с путём:
// молча игнорировать испорченный файл нельзя, иначе сохранённый выбор
// пользователя тихо перестанет работать. Права файла не проверяются:
// защищать в файле без секретов нечего.
func Load() (Settings, string, error) {
	path, err := Path()
	if err != nil {
		return Settings{}, "", err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Settings{}, path, nil
		}
		return Settings{}, path, fmt.Errorf("config: чтение %s: %w", path, err)
	}

	var s Settings
	if err := yaml.Unmarshal(data, &s); err != nil {
		return Settings{}, path, fmt.Errorf("config: разбор %s: %w", path, err)
	}
	return s, path, nil
}

// Save записывает s в файл настроек, создавая каталог 0700 при
// необходимости, и возвращает фактический путь.
//
// Запись атомарна тем же приёмом, что token.Save: временный файл в том же
// каталоге, явный Chmod 0600 (не полагаться на umask процесса),
// сериализация YAML, затем os.Rename поверх целевого пути; при любой
// ошибке до переименования временный файл удаляется.
func Save(s Settings) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("config: не удалось создать каталог конфига %s: %w", dir, err)
	}

	path := filepath.Join(dir, "settings.yml")

	data, err := yaml.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("config: сериализация настроек: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".settings-*.yml.tmp")
	if err != nil {
		return "", fmt.Errorf("config: не удалось создать временный файл настроек: %w", err)
	}
	tmpPath := tmp.Name()
	// Право читаться не должно зависеть от umask процесса — Chmod делает
	// намерение явным в коде (тот же приём, что в token.Save).
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("config: не удалось выставить права 0600 на временный файл настроек: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("config: не удалось записать временный файл настроек: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("config: не удалось закрыть временный файл настроек: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("config: не удалось сохранить настройки %s: %w", path, err)
	}

	return path, nil
}
