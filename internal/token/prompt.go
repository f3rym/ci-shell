package token

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/f3rym/ci-shell/internal/prompt"
)

// Interactive сообщает, подключён ли стандартный ввод к терминалу. Пайп,
// перенаправление из файла и запуск из CI дают ложь — это единственный
// предохранитель от зависшего процесса в неинтерактивной среде, поэтому
// проверка вынесена отдельной экспортированной функцией, а не спрятана
// условием внутри Prompt.
func Interactive() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// Prompt спрашивает токен GitLab для host прямо в терминале: отсутствие
// токена — не ошибка с инструкцией, а вопрос, на который может ответить
// только человек за терминалом. Весь вывод функции — в поток ошибок,
// стандартный вывод здесь не используется вовсе, чтобы значение токена не
// могло уехать в stdout, который пользователь перенаправляет в файл.
func Prompt(host string) (Token, error) {
	fmt.Fprintf(os.Stderr, "токен GitLab для хоста %s не найден\nвведите токен (области read_api достаточно): ", host)

	restoreEcho := disableEcho()
	defer func() {
		restoreEcho()
		// Перевод строки после невидимого ввода — иначе следующая строка
		// вывода приклеится к скрытому вводу пользователя.
		fmt.Fprintln(os.Stderr)
	}()

	// Читатель общий на процесс (internal/prompt): свой на каждый вопрос
	// выбрасывал бы всё, что успел забуферизовать после первой строки, и
	// следующий вопрос читал бы пустоту.
	line, err := prompt.Line()
	secret := strings.TrimSpace(line)
	if err != nil && secret == "" {
		return Token{}, ErrNoToken
	}
	if secret == "" {
		// Пользователь отказался ответить — это не новая поломка, штатный
		// исход того же вопроса, что и раньше.
		return Token{}, ErrNoToken
	}

	path, saveErr := Save(host, secret)
	if saveErr != nil {
		fmt.Fprintf(os.Stderr, "предупреждение: не удалось сохранить токен: %s; используется только в этом прогоне\n", saveErr)
		return Token{Secret: secret, Source: "введён в терминале, не сохранён"}, nil
	}

	return Token{Secret: secret, Source: "введён в терминале, сохранён в " + path}, nil
}

// disableEcho отключает эхо терминала на стандартном вводе шелл-аутом в
// stty и возвращает функцию восстановления. Если stty не найден в PATH или
// отказал (например, Windows-сборка из Makefile), чтение всё равно
// происходит, но перед ним печатается предупреждение — молчаливое эхо
// секрета недопустимо, но и отказ работать тоже (принцип v0.2.0).
func disableEcho() (restore func()) {
	if _, err := exec.LookPath("stty"); err != nil {
		fmt.Fprintln(os.Stderr, "предупреждение: stty не найден, эхо терминала отключить не удалось — вводимое значение будет видно на экране")
		return func() {}
	}

	off := exec.Command("stty", "-echo")
	off.Stdin = os.Stdin
	if err := off.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "предупреждение: не удалось отключить эхо терминала — вводимое значение будет видно на экране")
		return func() {}
	}

	return func() {
		on := exec.Command("stty", "echo")
		on.Stdin = os.Stdin
		_ = on.Run()
	}
}
