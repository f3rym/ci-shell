// pull.go — обновление рабочей копии человека (Фаза 11, GUIDE-04).
//
// Рабочая копия человека ходит на его собственный удалённый репозиторий его
// собственными учётными данными. Наш токен сюда не подмешивается ни в каком
// виде — он выдан для нашего зеркала и для API, и вписывать его в чужой
// remote означало бы отправить секрет неизвестно куда. Отсюда и отдельный
// файл: механика доставки токена до git (auth.go) к этой функции отношения
// не имеет.
package repo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Sentinel-ошибки обновления рабочей копии.
var (
	// ErrPullFailed — обновление не удалось по прочей причине.
	ErrPullFailed = errors.New("не удалось обновить рабочую копию")
	// ErrSSHKeyRequired — git просит ключ доступа.
	ErrSSHKeyRequired = errors.New("git просит ключ доступа")
)

// Pull обновляет рабочую копию в каталоге root быстрой перемоткой вперёд.
//
// Окружение дочернего процесса — окружение утилиты плюс выключенный диалог
// учётных данных и неинтерактивный режим ssh: без них отказ по ключу
// превращается в вопрос пароля посреди полноэкранного интерфейса, где
// отвечать на него некому и нечем — процесс просто повиснет.
//
// Возвращает первую строку вывода git (её показывает экран) и ошибку.
func Pull(ctx context.Context, root string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "pull", "--ff-only")
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes",
	)

	out, err := runCmd(cmd)
	if err != nil {
		if keyRefused(err.Error()) {
			return "", fmt.Errorf("repo: %s: %w", firstLine(err.Error()), ErrSSHKeyRequired)
		}
		return "", fmt.Errorf("repo: %s: %w", firstLine(err.Error()), ErrPullFailed)
	}
	return firstLine(out), nil
}

// keyRefused распознаёт формы отказа по ключу: отказ по публичному ключу,
// непроверенный ключ хоста, невозможность прочитать удалённый репозиторий,
// запрос пароля. Сравнение — в нижнем регистре. Тексты разбираются здесь и
// превращаются в sentinel-ошибку — интерфейс текстов не разбирает.
func keyRefused(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "publickey") ||
		strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "host key verification failed") ||
		strings.Contains(lower, "could not read from remote repository") ||
		strings.Contains(lower, "password:")
}
