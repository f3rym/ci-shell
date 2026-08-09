// Package cache вычисляет единственную точку постоянного каталога
// временных файлов утилиты — ~/.cache/ci-shell (или $XDG_CACHE_HOME/ci-shell),
// либо каталог данных, выбранный человеком (settings.cache_dir, Фаза 11,
// GUIDE-02).
//
// Файл окружения и worktree переезжают сюда из системного временного
// каталога: snap-сборка Docker не видит хостовый /tmp, и docker create
// --env-file /tmp/... отказывает с «no such file» (idea-0.2.0 §3). Каталог
// постоянный, а не системный временный, поэтому содержимое (файл окружения
// с секретами, чекаут кода) не предназначено другим пользователям машины —
// создаётся с правами 0700.
package cache

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/f3rym/ci-shell/internal/config"
)

// ErrUnavailable — ни XDG_CACHE_HOME, ни HOME не заданы: каталог кэша
// вычислить невозможно.
var ErrUnavailable = errors.New("не удалось определить каталог кэша")

// ErrInvalidDir — введённый или сохранённый каталог данных не годится.
var ErrInvalidDir = errors.New("каталог данных не годится")

// baseMu/baseSet/baseVal/baseErr — однократно вычисленная база каталога
// данных (Base ниже). Reload сбрасывает вычисление — им пользуется ровно
// один вызывающий: экран проводника, только что записавший новый каталог в
// настройки и обязанный повторить подготовку уже с ним.
//
// Состояние закрыто мьютексом, а не sync.Once: сброс пересозданием Once
// (baseOnce = sync.Once{}) — гонка сам по себе, а Base зовётся из горутины
// подготовки сессии в тот же момент, когда экран проводника сбрасывает
// вычисление из горутины Bubble Tea (WR-04 обзора v0.3.0).
var (
	baseMu  sync.Mutex
	baseSet bool
	baseVal string
	baseErr error
)

// ValidDir — единственная проверка пути каталога данных в проекте.
// Требования: непустой, абсолютный, нормализованный (filepath.Clean
// возвращает его же), без двоеточия (оно пересобирает аргумент
// монтирования контейнерного CLI — та же причина, что и в
// internal/runner/docker.go), без управляющих символов (ломают и разбор
// аргумента, и любое сообщение о нём) и последний сегмент не начинается с
// точки (ровно та поломка, которую чинит эта фаза — snap-профиль
// безопасности Docker не видит скрытые каталоги, и предлагать её же
// обратно нельзя).
func ValidDir(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("cache: каталог данных не задан: %w", ErrInvalidDir)
	case !filepath.IsAbs(p):
		return fmt.Errorf("cache: каталог данных %q не абсолютный: %w", p, ErrInvalidDir)
	case filepath.Clean(p) != p:
		return fmt.Errorf("cache: каталог данных %q не нормализован: %w", p, ErrInvalidDir)
	case strings.Contains(p, ":"):
		return fmt.Errorf("cache: каталог данных %q содержит двоеточие: %w", p, ErrInvalidDir)
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return fmt.Errorf("cache: каталог данных %q содержит управляющий символ: %w", p, ErrInvalidDir)
		}
	}
	if strings.HasPrefix(filepath.Base(p), ".") {
		return fmt.Errorf("cache: каталог данных %q скрыт — демон Docker snap-сборки такие не видит: %w", p, ErrInvalidDir)
	}
	return nil
}

// computeBaseLocked вычисляет базу каталога данных ровно один раз (см.
// Base): сохранённая настройка, если она непуста и прошла ValidDir, иначе
// XDG_CACHE_HOME, иначе HOME с подкаталогом .cache — в обоих последних
// случаях к базе дописывается подкаталог ci-shell (Base ниже). Вызывающий
// обязан держать baseMu.
func computeBaseLocked() {
	settings, _, err := config.Load()
	if err != nil {
		baseErr = err
		return
	}
	if settings.CacheDir != "" {
		// Непустая, но негодная настройка даёт отказ, а не молчаливый откат
		// к умолчанию: молчаливый откат означал бы, что человек выбрал
		// каталог, а утилита пишет в другой.
		if err := ValidDir(settings.CacheDir); err != nil {
			baseErr = err
			return
		}
		baseVal = settings.CacheDir
		return
	}
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		baseVal = filepath.Join(xdg, "ci-shell")
		return
	}
	if home := os.Getenv("HOME"); home != "" {
		baseVal = filepath.Join(home, ".cache", "ci-shell")
		return
	}
	baseErr = fmt.Errorf("cache: не заданы ни XDG_CACHE_HOME, ни HOME: %w", ErrUnavailable)
}

// Base возвращает базу каталога данных утилиты, вычисленную однократно и
// запомненную (sync.Once). Когда база пришла из настройки, подкаталог с
// именем утилиты к ней не добавляется — человек уже назвал каталог целиком;
// когда она вычислена формулой по умолчанию, подкаталог ci-shell
// добавляется, как и раньше.
func Base() (string, error) {
	baseMu.Lock()
	defer baseMu.Unlock()
	if !baseSet {
		computeBaseLocked()
		baseSet = true
	}
	return baseVal, baseErr
}

// Reload сбрасывает однократное вычисление Base. Нужен ровно одному
// вызывающему: экрану проводника, только что записавшему новый каталог
// данных в настройки.
func Reload() {
	baseMu.Lock()
	defer baseMu.Unlock()
	baseSet, baseVal, baseErr = false, "", nil
}

// Suggested — предлагаемый нескрытый каталог данных: HOME с подкаталогом
// ci-shell-data, пустая строка при пустом HOME. Путь предлагается нескрытым
// намеренно — именно скрытый каталог не видит профиль безопасности
// snap-сборки Docker.
func Suggested() string {
	home := os.Getenv("HOME")
	if home == "" {
		return ""
	}
	return filepath.Join(home, "ci-shell-data")
}

// Dir возвращает путь к подкаталогу parts внутри каталога данных утилиты
// (см. Base) и создаёт его с правами 0700, если он ещё не существует.
func Dir(parts ...string) (string, error) {
	base, err := Base()
	if err != nil {
		return "", err
	}

	elems := append([]string{base}, parts...)
	dir := filepath.Join(elems...)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("cache: не удалось создать каталог %s: %w", dir, err)
	}
	return dir, nil
}

// CheckoutBase — неинтерактивное решение о каталоге чекаута кода джобы,
// общее у обычного режима (cmd/ci) и консольного интерфейса
// (internal/ui/session.go): непустая настройка пользователя configured
// раскрывается (ведущий "~" — через HOME, путь приводится к абсолютному,
// каталог создаётся с правами 0700); пустая настройка даёт подкаталог
// чекаутов внутри каталога данных (Dir("checkouts")).
//
// Третий источник обычного режима — вопрос в терминале, когда настройки нет
// вовсе, — сюда не переезжает: посреди альтернативного экрана интерфейса
// вопрос в терминале испортил бы кадр и перехватил бы клавиши, а обычный
// режим продолжает задавать его сам (cmd/ci/main.go, checkoutBase). Пакет
// при этом остаётся нижним уровнем: настройка приходит параметром, читает
// её вызывающий, а не CheckoutBase.
func CheckoutBase(configured string) (string, error) {
	if configured == "" {
		return Dir("checkouts")
	}

	dir := configured
	if home := os.Getenv("HOME"); home != "" && strings.HasPrefix(dir, "~") {
		dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("cache: не удалось привести каталог чекаутов %s к абсолютному пути: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", fmt.Errorf("cache: не удалось создать каталог чекаутов %s: %w", abs, err)
	}
	return abs, nil
}
