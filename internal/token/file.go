package token

import (
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

// normalizeHost приводит ключ хоста к голому виду. Пользователи иногда
// копируют хост со схемой ("https://gitlab.com") прямо из адресной
// строки — конфиг-файл принимает такой ключ наравне с голым хостом.
func normalizeHost(host string) string {
	h := strings.TrimSuffix(host, "/")
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	return h
}
