// Package provider описывает единственную точку доступа к CI-системе.
//
// Набор методов интерфейса Provider зафиксирован в idea §6; JobByID добавлен
// для пути «пользователь дал ссылку на конкретную джобу». Пакет содержит
// только доменные типы и ошибки — ни транспорта, ни упоминаний конкретной
// CI-системы здесь быть не должно: реализации живут в подпакетах.
package provider

import (
	"context"
	"errors"
	"time"
)

// Provider — единственная точка доступа к CI-системе. Реализации живут в
// подпакетах; интерфейс не должен меняться при добавлении следующих
// провайдеров (idea §6).
type Provider interface {
	// FindFailedJob ищет последнюю упавшую джобу по репозиторию и ref (idea §3.1, запуск без аргументов).
	FindFailedJob(ctx context.Context, repo, ref string) (Job, error)
	// JobByID возвращает метаданные конкретной джобы по пути проекта и её id.
	JobByID(ctx context.Context, projectPath string, jobID int64) (Job, error)
	// MergedConfig возвращает конфигурацию пайплайна на указанном коммите.
	MergedConfig(ctx context.Context, projectPath, sha string) (Config, error)
	// Artifacts возвращает артефакты предыдущих стадий для джобы.
	Artifacts(ctx context.Context, job Job) ([]Artifact, error)
	// Variables возвращает переменные проекта и группы, видимые джобе.
	Variables(ctx context.Context, job Job) (map[string]string, error)
}

// Job — метаданные джобы CI, полученные через API провайдера.
type Job struct {
	ID            int64
	Name          string
	Stage         string
	Status        string
	FailureReason string
	Ref           string
	// Tag — true, если пайплайн запущен по тегу, а не по ветке. От этого
	// зависит, восстанавливать ли CI_COMMIT_TAG или CI_COMMIT_BRANCH.
	Tag           bool
	CommitSHA     string
	CommitTitle   string
	WebURL        string
	ProjectPath   string
	ProjectID     int64
	PipelineID    int64
	PipelineIID   int64
	RunnerDesc    string
	StartedAt     time.Time
	FinishedAt    time.Time
}

// Config — конфигурация пайплайна целиком: в Фазе 1 это разобранный сырой
// файл конфига пайплайна, начиная с NEXT-02 — уже смёрженный конфиг.
// Форма для потребителей одна и та же.
type Config struct {
	Jobs map[string]JobConfig
	Raw  []byte
}

// JobConfig — конфигурация одной джобы: образ, шаги, переменные.
type JobConfig struct {
	Name         string
	Stage        string
	Image        string
	BeforeScript []string
	Script       []string
	Variables    map[string]string
}

// Artifact — артефакт предыдущей стадии джобы.
type Artifact struct {
	Filename string
	Size     int64
	FileType string
}

// JobByName ищет джобу в конфиге по имени.
func (c Config) JobByName(name string) (JobConfig, bool) {
	jc, ok := c.Jobs[name]
	return jc, ok
}

// Sentinel-ошибки провайдеров. Конкретные реализации оборачивают их через
// fmt.Errorf("...: %w", ErrX), чтобы вызывающий код мог проверять причину
// через errors.Is, не завися от конкретного провайдера.
var (
	ErrNotSupported   = errors.New("метод не поддерживается этим провайдером")
	ErrJobNotFound    = errors.New("джоба не найдена")
	ErrConfigNotFound = errors.New("конфигурация пайплайна не найдена")
	ErrUnauthorized   = errors.New("нет доступа: токен отклонён")
	ErrUnresolvedRefs = errors.New("конфиг использует include/extends/!reference")
)
