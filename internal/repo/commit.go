package repo

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// commit.go — фиксация перенесённого (задача 2, план 09-03). Живёт в
// отдельном файле, а не рядом с переносом правок (patch.go), потому что
// patch.go закреплён гейтами Фазы 7 как принципиально не создающий
// коммитов: обычный режим переноса не коммитит по решению FIX-04, и это
// закрепление должно остаться верным. Фиксация — отдельная возможность
// интерфейса (:commit), вызываемая только по явной команде человека и
// только после явного переноса (Apply) и отдельного подтверждения.

// Sentinel-ошибки этого файла — тот же стиль, что и у остального пакета:
// диагностический контекст несёт текст через fmt.Errorf("...: %w", ErrX).
var (
	// ErrEmptyMessage — сообщение коммита пустое после обрезки пробельных
	// символов.
	ErrEmptyMessage = errors.New("сообщение коммита пустое")
	// ErrNothingToCommit — список путей для фиксации пуст.
	ErrNothingToCommit = errors.New("список путей для коммита пуст")
	// ErrCommitFailed — регистрация или сама фиксация отказали.
	ErrCommitFailed = errors.New("коммит не удался")
)

// Commit фиксирует пути paths в репозитории root сообщением message.
//
// Три вещи прямо. Первое: фиксируются РОВНО пути из отчёта патча — те, что
// уже прошли проверку на абсолютность и восхождение по каталогам
// (CheckPaths в patch.go); захват всей незавершённой работы человека в его
// репозитории недопустим, и разделитель "--" перед списком путей обязателен
// в обеих командах ниже, чтобы путь, похожий на имя ветки, не был истолкован
// как ссылка. Второе: сообщение и каждый путь уходят отдельными элементами
// среза аргументов exec.Command — оболочка не задействована вовсе, и
// подстановка в строку команды здесь физически невозможна. Третье: в файле
// нет ни флага "все отслеживаемые файлы", ни флага дописывания в последний
// коммит, ни флага обхода перехватчиков, ни подкоманды отправки в удалённый
// репозиторий — история человека этой функцией не переписывается и никуда
// не уезжает.
func Commit(ctx context.Context, root, message string, paths []string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return fmt.Errorf("repo: %w", ErrEmptyMessage)
	}
	if len(paths) == 0 {
		return fmt.Errorf("repo: %w", ErrNothingToCommit)
	}

	addArgs := append([]string{"-C", root, "add", "--"}, paths...)
	addCmd := exec.CommandContext(ctx, "git", addArgs...)
	if _, err := runCmd(addCmd); err != nil {
		return fmt.Errorf("repo: git add отказал: %s: %w", firstLine(err.Error()), ErrCommitFailed)
	}

	commitArgs := append([]string{"-C", root, "commit", "-m", message, "--"}, paths...)
	commitCmd := exec.CommandContext(ctx, "git", commitArgs...)
	if _, err := runCmd(commitCmd); err != nil {
		return fmt.Errorf("repo: git commit отказал: %s: %w", firstLine(err.Error()), ErrCommitFailed)
	}

	return nil
}
