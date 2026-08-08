package token

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// fileConfig — верхний уровень конфиг-файла с токенами: карта хостов на
// их запись.
type fileConfig struct {
	Hosts map[string]hostEntry `yaml:"hosts"`
}

// hostEntry — запись одного хоста в конфиг-файле.
type hostEntry struct {
	Token string `yaml:"token"`
}

// configNames — имена файла конфига в порядке предпочтения: если оба
// существуют, побеждает первый.
var configNames = []string{"config.yml", "config.yaml"}

// configPath возвращает путь к конфиг-файлу токенов.
//
// Каталог: $XDG_CONFIG_HOME/ci-shell, если XDG_CONFIG_HOME непуст,
// иначе $HOME/.config/ci-shell. Если ни один из configNames не существует
// на диске, возвращается путь к первому имени (config.yml) вместе с
// ошибкой, обёрнутой вокруг os.ErrNotExist.
func configPath() (string, error) {
	var dir string
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		dir = filepath.Join(xdg, "ci-shell")
	} else {
		home := os.Getenv("HOME")
		if home == "" {
			return "", fmt.Errorf("token: не удалось определить каталог конфига: не заданы ни XDG_CONFIG_HOME, ни HOME")
		}
		dir = filepath.Join(home, ".config", "ci-shell")
	}

	first := filepath.Join(dir, configNames[0])
	for _, name := range configNames {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return first, fmt.Errorf("token: конфиг-файл не найден: %w", os.ErrNotExist)
}

// fromFile ищет токен для host в конфиг-файле пользователя.
//
// Права файла проверяются до чтения содержимого: файл, доступный группе
// или остальным (Perm()&0o077 != 0), не читается вовсе — только
// ErrInsecurePermissions с путём и подсказкой chmod (T-01-08).
// Поиск хоста — точное совпадение ключа, без перебора и значений
// «по умолчанию» (T-01-11).
func fromFile(host string) (Token, error) {
	path, err := configPath()
	if err != nil {
		return Token{}, ErrNoToken
	}

	info, err := os.Stat(path)
	if err != nil {
		return Token{}, ErrNoToken
	}
	if !info.Mode().IsRegular() {
		return Token{}, fmt.Errorf("token: %s не является обычным файлом", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Token{}, fmt.Errorf(
			"token: %s имеет слишком широкие права (%s), почините: chmod 600 %s: %w",
			path, info.Mode().Perm(), path, ErrInsecurePermissions,
		)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Token{}, fmt.Errorf("token: чтение %s: %w", path, err)
	}

	var cfg fileConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Token{}, fmt.Errorf("token: разбор %s: %w", path, err)
	}

	// Пользователи иногда записывают ключ хоста со схемой
	// ("https://gitlab.com"), скопировав его прямо из адресной строки —
	// нормализуем ключи конфига к голому хосту перед точным совпадением
	// с host (который уже приходит голым от joburl.Parse).
	entry, ok := hostEntry{}, false
	for key, e := range cfg.Hosts {
		if normalizeHost(key) == host {
			entry, ok = e, true
			break
		}
	}
	if !ok || entry.Token == "" {
		return Token{}, ErrNoToken
	}

	return Token{
		Secret: entry.Token,
		Source: fmt.Sprintf("конфиг-файл %s, хост %s", path, host),
	}, nil
}

// Save записывает secret для host в конфиг-файл токенов, создавая каталог и
// файл при необходимости, и возвращает путь сохранённого файла.
//
// Путь берётся у configPath() — той же формулы, что использует fromFile;
// вторая формула пути в проекте не появляется. Отсутствие файла
// (os.ErrNotExist) не препятствие: путь всё равно валиден. Существующий
// файл читается и разбирается, чтобы записи других хостов сохранились;
// запись для host (ключ нормализуется normalizeHost) добавляется или
// перезаписывается. Файл с небезопасными правами не читается — чинить
// права за пользователя молча нельзя (T-01-08 в той же логике, что и
// fromFile).
//
// Запись атомарна: временный файл создаётся в том же каталоге с явными
// правами 0o600, в него сериализуется YAML, затем os.Rename поверх целевого
// пути — прерывание посередине не уничтожает уже сохранённые токены других
// хостов. При любой ошибке до переименования временный файл удаляется.
func Save(host, secret string) (string, error) {
	if secret == "" {
		return "", fmt.Errorf("token: пустой секрет не сохраняется")
	}

	path, err := configPath()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("token: не удалось создать каталог конфига %s: %w", dir, err)
	}

	var cfg fileConfig
	if info, statErr := os.Stat(path); statErr == nil {
		if info.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf(
				"token: %s имеет слишком широкие права (%s), почините: chmod 600 %s: %w",
				path, info.Mode().Perm(), path, ErrInsecurePermissions,
			)
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", fmt.Errorf("token: чтение %s: %w", path, readErr)
		}
		if unmarshalErr := yaml.Unmarshal(data, &cfg); unmarshalErr != nil {
			return "", fmt.Errorf("token: разбор %s: %w", path, unmarshalErr)
		}
	}
	if cfg.Hosts == nil {
		cfg.Hosts = make(map[string]hostEntry)
	}
	cfg.Hosts[normalizeHost(host)] = hostEntry{Token: secret}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("token: сериализация конфига: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".config-*.yml.tmp")
	if err != nil {
		return "", fmt.Errorf("token: не удалось создать временный файл конфига: %w", err)
	}
	tmpPath := tmp.Name()
	// Право читаться не должно зависеть от umask процесса — Chmod делает
	// намерение явным в коде (тот же приём, что в runner.writeEnvFile).
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("token: не удалось выставить права 0600 на временный файл конфига: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("token: не удалось записать временный файл конфига: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("token: не удалось закрыть временный файл конфига: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("token: не удалось сохранить конфиг %s: %w", path, err)
	}

	return path, nil
}

// normalizeHost приводит ключ хоста к голому виду. Пользователи иногда
// копируют хост со схемой ("https://gitlab.com") прямо из адресной
// строки — конфиг-файл принимает такой ключ наравне с голым хостом.
func normalizeHost(host string) string {
	h := strings.TrimSuffix(host, "/")
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	return h
}
