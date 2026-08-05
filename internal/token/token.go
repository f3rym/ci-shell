// Package token резолвит токен GitLab, которым утилита аутентифицируется
// в API.
package token

import (
	"errors"
	"os"
)

// Token несёт значение токена и человекочитаемое описание источника.
// Source безопасно печатать; значение — нет.
type Token struct {
	Secret string
	Source string // "переменная окружения CI_SHELL_TOKEN"
}

// String возвращает только описание источника, чтобы токен не утёк через
// %v и логи.
func (t Token) String() string {
	return t.Source
}

// envVars — переменные окружения, из которых читается токен, в порядке
// приоритета: первое непустое значение выигрывает.
var envVars = []string{"CI_SHELL_TOKEN", "GITLAB_TOKEN"}

var ErrNoToken = errors.New("токен GitLab не найден")

// Resolve находит токен GitLab для указанного хоста.
//
// Параметр host уже принимается в сигнатуре, потому что план 01-02 добавит
// поиск по хосту в конфиг-файле; на этой задаче он не используется для
// выбора значения — читаются только переменные окружения.
func Resolve(host string) (Token, error) {
	for _, name := range envVars {
		if v := os.Getenv(name); v != "" {
			return Token{
				Secret: v,
				Source: "переменная окружения " + name,
			}, nil
		}
	}
	return Token{}, ErrNoToken
}
