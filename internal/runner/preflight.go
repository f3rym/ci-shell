package runner

import (
	"context"
	"errors"
	"fmt"
)

// Preflight выполняет только дешёвые проверки воспроизводимости джобы: демон
// Docker отвечает, и у джобы вообще указан образ. Никаких тяжёлых действий —
// ни загрузки образа, ни создания каталогов: пользователь с невоспроизводимой
// джобой или без запущенного Docker должен получить ответ за секунду, а не
// после долгого ожидания (idea §7).
func Preflight(ctx context.Context, d *Docker, image string) error {
	if err := d.Ping(ctx); err != nil {
		return err
	}
	if image == "" {
		return fmt.Errorf("runner: у джобы нет образа: %w", ErrNoImage)
	}
	return nil
}

// Unreproducible истинна для ошибок, означающих, что эту джобу утилита не
// воспроизведёт в принципе (ErrNoImage, ErrNoShell), в отличие от «на этой
// машине сейчас не готово окружение» (ErrDockerUnavailable,
// ErrDaemonUnavailable, ErrImageUnavailable). Различие нужно, чтобы в первом
// случае не советовать «повторите попытку», а честно сказать, что дело не в
// машине пользователя.
func Unreproducible(err error) bool {
	return errors.Is(err, ErrNoImage) || errors.Is(err, ErrNoShell)
}
