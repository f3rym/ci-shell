// Package cache вычисляет единственную точку постоянного каталога
// временных файлов утилиты — ~/.cache/ci-shell (или $XDG_CACHE_HOME/ci-shell).
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
)

// ErrUnavailable — ни XDG_CACHE_HOME, ни HOME не заданы: каталог кэша
// вычислить невозможно.
var ErrUnavailable = errors.New("не удалось определить каталог кэша")

// Dir возвращает путь к подкаталогу parts внутри постоянного каталога кэша
// утилиты и создаёт его с правами 0700, если он ещё не существует.
//
// База берётся из XDG_CACHE_HOME, если она непуста, иначе из HOME с
// подкаталогом .cache; дальше — подкаталог ci-shell и переданные parts.
func Dir(parts ...string) (string, error) {
	var base string
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		base = xdg
	} else if home := os.Getenv("HOME"); home != "" {
		base = filepath.Join(home, ".cache")
	} else {
		return "", fmt.Errorf("cache: не заданы ни XDG_CACHE_HOME, ни HOME: %w", ErrUnavailable)
	}

	elems := append([]string{base, "ci-shell"}, parts...)
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
// чекаутов внутри этого же каталога кэша (Dir("checkouts")).
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
