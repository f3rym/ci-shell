package runner

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/f3rym/ci-shell/internal/textwidth"
)

// ErrRetryInstall — не удалось положить команду retry в контейнер.
var ErrRetryInstall = errors.New("не удалось положить команду retry в контейнер")

// RetryPath — единственное место, где задан путь исполняемого файла
// retry внутри контейнера: и запись, и смена прав, и текст подсказки
// пользователю берут его отсюда.
const RetryPath = "/usr/local/bin/retry"

// shellQuote оборачивает s в одинарные кавычки для дословной подстановки в
// POSIX-скрипт: каждая встреченная одинарная кавычка заменяется
// последовательностью «закрыть кавычку, экранированная кавычка, открыть
// кавычку». Применяется только к описательным строкам (секция и обрезанная
// команда для печати, рабочий каталог) — команды шагов через shellQuote не
// проходят никогда, они уходят в тело скрипта дословно тем же приёмом, что
// уже использует buildScript (steps.go).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shortCommand обрезает s до n ячеек терминала для описательной строки
// внутри скрипта. Делегат единственной меры ширины проекта (internal/textwidth,
// Фаза 12, POL-02) — своей копии обрезки здесь больше нет.
func shortCommand(s string, n int) string {
	return textwidth.Truncate(s, n)
}

// BuildRetryScript собирает содержимое исполняемого файла retry — чистая
// функция без ввода-вывода. Значения переменных окружения в её параметры не
// входят вовсе: карта окружения не является ни аргументом, ни полем ни
// одной структуры этого файла. Единственное значение из окружения,
// попадающее в скрипт, — рабочий каталог workDir, и он уже стоит аргументом
// создания контейнера.
//
// Без аргументов скрипт перезапускает шаг с номером failedIndex (считая от
// единицы); с --rest прогоняет шаги после него, останавливаясь на первой
// ошибке (set -e внутри тела функции — иначе остановка распространилась бы
// на весь скрипт и оборвала его до печати итогового сообщения). Любой иной
// аргумент — краткая справка и код возврата 2. failedIndex вне диапазона
// [1, len(steps)] значит «ни один шаг не упал» — обе функции сообщают, что
// перезапускать нечего, и возвращают ноль; упавший последний шаг —
// __ci_retry_rest сообщает, что после упавшего шагов нет.
func BuildRetryScript(steps []Step, failedIndex int, workDir, shell string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "#!/usr/bin/env %s\n", shell)
	b.WriteString("# Файл создан ci-shell.\n")
	b.WriteString("# retry без аргументов перезапускает шаг джобы, упавший в CI.\n")
	b.WriteString("# retry --rest прогоняет шаги, идущие после упавшего, с остановкой на первой ошибке.\n\n")

	fmt.Fprintf(&b, "cd %s || exit 1\n\n", shellQuote(workDir))

	hasFailed := failedIndex >= 1 && failedIndex <= len(steps)

	// set -e и здесь, а не только в __ci_retry_rest: оригинальный прогон
	// исполняет каждый шаг одной сессией с set -e (см. steps.go), поэтому шаг
	// из нескольких команд (блочный скаляр YAML или "a; b") падает там на
	// первой же ненулевой. Без set -e в этой подоболочке кодом возврата шага
	// становится код его ПОСЛЕДНЕЙ команды: шаг, у которого падает первая
	// команда, а последняя проходит, печатал бы «✓ шаг теперь проходит (в CI
	// он падал)» — ровно тот ложный зелёный сигнал, которого петля фикса не
	// имеет права подавать.
	b.WriteString("__ci_retry_failed() (\n")
	b.WriteString("    set -e\n")
	if !hasFailed {
		b.WriteString("    echo 'перезапускать нечего: ни один шаг не упал'\n")
		b.WriteString("    exit 0\n")
	} else {
		b.WriteString(steps[failedIndex-1].Command)
		b.WriteString("\n")
	}
	b.WriteString(")\n\n")

	b.WriteString("__ci_retry_rest() (\n")
	b.WriteString("    set -e\n")
	switch {
	case !hasFailed:
		b.WriteString("    echo 'перезапускать нечего: ни один шаг не упал'\n")
	case failedIndex == len(steps):
		b.WriteString("    echo 'после упавшего шагов нет'\n")
	default:
		total := len(steps)
		for i := failedIndex + 1; i <= total; i++ {
			s := steps[i-1]
			fmt.Fprintf(&b, "    printf '[%d/%d] %%s: %%s\\n' %s %s\n",
				i, total, shellQuote(s.Section), shellQuote(shortCommand(s.Command, 60)))
			b.WriteString("    " + s.Command + "\n")
		}
	}
	b.WriteString(")\n\n")

	b.WriteString("__ci_retry_hint() {\n")
	b.WriteString("    echo\n")
	b.WriteString("    echo '  дальше: retry --rest    — прогнать оставшиеся шаги, тоже против грязного состояния'\n")
	b.WriteString("    echo '          exit            — выйти; ci-shell предложит чистый прогон в свежем контейнере — единственную честную проверку «в CI будет зелено»'\n")
	b.WriteString("    echo '  файлы правятся в своей IDE на хосте: каталог смонтирован, редактор внутри образа не нужен'\n")
	b.WriteString("}\n\n")

	desc := "шаг не найден"
	if hasFailed {
		desc = fmt.Sprintf("%s: %s", steps[failedIndex-1].Section, shortCommand(steps[failedIndex-1].Command, 60))
	}

	b.WriteString("case \"$1\" in\n")
	b.WriteString("    \"\")\n")
	b.WriteString("        __ci_retry_failed; code=$?\n")
	fmt.Fprintf(&b, "        desc=%s\n", shellQuote(desc))
	b.WriteString("        ;;\n")
	b.WriteString("    --rest)\n")
	b.WriteString("        __ci_retry_rest; code=$?\n")
	b.WriteString("        desc='оставшиеся шаги'\n")
	b.WriteString("        ;;\n")
	b.WriteString("    *)\n")
	b.WriteString("        echo 'использование: retry | retry --rest' >&2\n")
	b.WriteString("        exit 2\n")
	b.WriteString("        ;;\n")
	b.WriteString("esac\n\n")

	b.WriteString("if [ \"$code\" -eq 0 ]; then\n")
	b.WriteString("    echo \"✓ шаг «$desc» теперь проходит (в CI он падал)\"\n")
	b.WriteString("    __ci_retry_hint\n")
	b.WriteString("else\n")
	b.WriteString("    echo \"шаг «$desc» всё ещё падает с кодом $code — правьте файлы в своей IDE на хосте\"\n")
	b.WriteString("fi\n\n")

	b.WriteString("exit \"$code\"\n")

	return b.String()
}

// InstallRetry кладёт исполняемый файл RetryPath внутрь c: каталог
// назначения создаётся при необходимости, содержимое собирает
// BuildRetryScript и пишет WriteFile, права выставляются 0755 — скрипт
// зовёт сам пользователь из своей же оболочки внутри контейнера, а
// секрета в нём нет по построению. Рабочий каталог и оболочка берутся
// прямо у контейнера (поля пакета видны изнутри пакета), поэтому
// рассинхронизация с реальным контейнером невозможна. Любая неудача
// оборачивается в ErrRetryInstall с первой строкой исходной ошибки.
func InstallRetry(ctx context.Context, c *Container, steps []Step, failedIndex int) error {
	script := BuildRetryScript(steps, failedIndex, c.workDir, c.shell)

	dir := path.Dir(RetryPath)
	if err := c.MkdirAll(ctx, dir); err != nil {
		return fmt.Errorf("%s: %w", firstLine(err.Error()), ErrRetryInstall)
	}
	if err := c.WriteFile(ctx, RetryPath, script); err != nil {
		return fmt.Errorf("%s: %w", firstLine(err.Error()), ErrRetryInstall)
	}
	if err := c.Chmod(ctx, "0755", RetryPath); err != nil {
		return fmt.Errorf("%s: %w", firstLine(err.Error()), ErrRetryInstall)
	}
	return nil
}
