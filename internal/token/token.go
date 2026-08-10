// Package token резолвит токен GitLab, которым утилита аутентифицируется
// в API.
package token

import (
	"errors"
	"fmt"
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

// ErrInsecurePermissions означает: конфиг-файл с токенами найден, но его
// права шире 0600, поэтому читать его небезопасно. Это не то же самое,
// что «токена нет» — Resolve не должен подменять эту ошибку на ErrNoToken.
var ErrInsecurePermissions = errors.New("права конфиг-файла токенов шире 0600")

// Resolve находит токен GitLab для указанного хоста.
//
// Порядок приоритета: переменная окружения CI_SHELL_TOKEN, затем
// GITLAB_TOKEN, затем конфиг-файл по хосту. Первое непустое значение
// выигрывает.
func Resolve(host string) (Token, error) {
	for _, name := range envVars {
		if v := os.Getenv(name); v != "" {
			warnEnvTokenHostMismatch(name, host)
			return Token{
				Secret: v,
				Source: "переменная окружения " + name,
			}, nil
		}
	}

	return fromFileWrapped(host)
}

// warnEnvTokenHostMismatch warns on stderr when an environment token is
// about to be sent to a host it is not scoped to. An env token is
// host-agnostic while the host comes straight from the pasted URL, so a
// look-alike host would silently receive a live credential (WR-02).
// The expected host is GITLAB_HOST when set (glab semantics), otherwise
// gitlab.com. This deliberately warns instead of failing: self-hosted
// users with only an env token exported must keep working.
func warnEnvTokenHostMismatch(name, host string) {
	expected := NormalizeHost(os.Getenv("GITLAB_HOST"))
	if expected == "" {
		expected = "gitlab.com"
	}
	if host == expected {
		return
	}
	fmt.Fprintf(os.Stderr,
		"предупреждение: токен из переменной окружения %s будет отправлен на хост %s из ссылки (ожидался %s); "+
			"чтобы привязать токен к хосту, задайте GITLAB_HOST или перенесите токен в конфиг-файл\n",
		name, host, expected,
	)
}

// fromFileWrapped достаёт токен из конфиг-файла и оборачивает ошибки для
// пользователя.
func fromFileWrapped(host string) (Token, error) {
	tok, err := fromFile(host)
	if err != nil {
		// ErrInsecurePermissions означает «файл есть, но читать его
		// небезопасно» — эта ошибка не заменяется на ErrNoToken, иначе
		// пользователь не узнает, что делать (T-01-08).
		if errors.Is(err, ErrInsecurePermissions) {
			return Token{}, err
		}
		// Оборачиваем ErrNoToken названием запрошенного хоста: errors.Is
		// продолжает находить сам сентинел через %w, но пользователь сразу
		// видит, для какого хоста токен не нашёлся — важно, когда в
		// конфиге есть запись для другого хоста и токен не подставился.
		return Token{}, fmt.Errorf("токен для хоста %s не найден: %w", host, ErrNoToken)
	}
	return tok, nil
}
