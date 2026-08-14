package token

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/f3rym/ci-shell/internal/config"
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
// Каталог берётся у config.Dir() — единственной формулы каталога конфига в
// проекте. Если ни один из configNames не существует на диске,
// возвращается путь к первому имени (config.yml) вместе с ошибкой,
// обёрнутой вокруг os.ErrNotExist.
func configPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", fmt.Errorf("token: %w", err)
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

// readConfig читает и разбирает конфиг-файл токенов, возвращая разобранный
// конфиг, путь, из которого он прочитан, и ошибку. Все пять веток отказа
// прежней fromFile сохранены дословно: отсутствующий путь и неудачный опрос
// файла остаются «токена нет» для вызывающего; файл есть, но не обычный;
// права шире 0600 (ErrInsecurePermissions); неудачное чтение; неудачный
// разбор. Вынесена, чтобы Hosts (internal/token/hosts.go) могла опросить те
// же записи, не читая значение токена.
//
// Права файла проверяются до чтения содержимого: файл, доступный группе
// или остальным (Perm()&0o077 != 0), не читается вовсе — только
// ErrInsecurePermissions с путём и подсказкой chmod (T-01-08).
func readConfig() (fileConfig, string, error) {
	path, err := configPath()
	if err != nil {
		return fileConfig{}, "", ErrNoToken
	}

	info, err := os.Stat(path)
	if err != nil {
		return fileConfig{}, "", ErrNoToken
	}
	if !info.Mode().IsRegular() {
		return fileConfig{}, "", fmt.Errorf("token: %s не является обычным файлом", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fileConfig{}, "", fmt.Errorf(
			"token: %s имеет слишком широкие права (%s), почините: chmod 600 %s: %w",
			path, info.Mode().Perm(), path, ErrInsecurePermissions,
		)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fileConfig{}, "", fmt.Errorf("token: чтение %s: %w", path, err)
	}

	var cfg fileConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fileConfig{}, "", fmt.Errorf("token: разбор %s: %w", path, err)
	}

	return cfg, path, nil
}

// fromFile ищет токен для host в конфиг-файле пользователя. Поиск хоста —
// точное совпадение ключа, без перебора и значений «по умолчанию» (T-01-11).
func fromFile(host string) (Token, error) {
	cfg, path, err := readConfig()
	if err != nil {
		// ErrInsecurePermissions — не «токена нет», а «читать небезопасно»:
		// эта ветка обязана дойти до пользователя как есть (T-01-08).
		if errors.Is(err, ErrInsecurePermissions) {
			return Token{}, err
		}
		return Token{}, ErrNoToken
	}

	// Пользователи иногда записывают ключ хоста со схемой
	// ("https://gitlab.com"), скопировав его прямо из адресной строки —
	// нормализуем ключи конфига к голому хосту перед точным совпадением
	// с host (который уже приходит голым от joburl.Parse).
	entry, ok := hostEntry{}, false
	for key, e := range cfg.Hosts {
		if NormalizeHost(key) == host {
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
// запись для host (ключ нормализуется NormalizeHost) добавляется или
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
	cfg.Hosts[NormalizeHost(host)] = hostEntry{Token: secret}

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

// NormalizeHost приводит ключ хоста к голому виду. Пользователи иногда
// копируют хост со схемой ("https://gitlab.com") прямо из адресной
// строки — конфиг-файл принимает такой ключ наравне с голым хостом.
//
// Экспортирована (Фаза 14, MENU-03): адрес своего инстанса человек вводит
// руками и копирует из адресной строки со схемой и хвостовым слэшем, а этот
// же адрес идёт и в ключ файла токенов, и в адрес страницы настроек —
// приводить его обязана та же единственная формула, что и при чтении файла,
// иначе ключ в файле и ключ в запросе разойдутся на первом же скопированном
// адресе.
// Регистр приводится здесь же (обзор v1.0.0, WR-3): имена в DNS
// регистронезависимы, и адрес, скопированный из адресной строки как
// GitLab.Example.Com, обязан дать тот же ключ файла токенов, что и ссылка на
// джобу с gitlab.example.com. Оба остальных источника хоста в проекте —
// joburl.Parse и repo.parseRemote — приводят регистр у себя; третьей формулы
// не заводится, но и исключения из правила здесь быть не должно.
func NormalizeHost(host string) string {
	h := strings.TrimSuffix(host, "/")
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	return strings.ToLower(h)
}
