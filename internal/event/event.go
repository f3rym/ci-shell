// Package event объявляет замкнутый словарь типизированных событий,
// которыми доменные пакеты (runner, repo, env, provider) сообщают о своём
// прогрессе. Событие — это данные о том, что произошло: какой образ, какой
// шаг из скольких, какой код выхода — а не готовая для человека строка.
// Текст собирает подписчик (internal/render.Printer), потому что
// подписчиков будет минимум два (печать сейчас, интерфейс в Фазе 9) и текст
// у них разный.
//
// Пакет не импортирует ни одного другого пакета проекта — только
// стандартную библиотеку. Это не стилистика: пакет, не знающий доменных
// типов (env.Variable и его поля Value/Secret), физически не может получить
// поле со значением переменной окружения, и это единственный структурный
// предохранитель, который переживёт любого будущего подписчика. Ни одно
// событие не несёт значение переменной окружения ни в каком виде — решение
// «показывать значение или нет» принимается в одном-единственном месте
// проекта (displayValue в internal/render) и не должно дублироваться:
// подписчиком может оказаться логгер.
package event

// Event — интерфейс с единственным неэкспортированным методом-меткой.
// Множество событий закрыто пакетом: посторонний тип в канал не проскочит,
// а подписчик разбирает поток переключателем по типу — ровно та форма,
// которую ждёт модель Elm-архитектуры в Фазе 9. Каждый тип ниже реализует
// эту метку значением (не указателем), чтобы события копировались и
// передавались как значения.
type Event interface {
	isEvent()
}

// RunKind — вид прогона шагов джобы. Тип живёт здесь, а не в runner, потому
// что его несёт поле события, а runner зависит от событий, не наоборот.
type RunKind int

const (
	// RunFirst — воспроизведение упавшего прогона.
	RunFirst RunKind = iota
	// RunClean — чистый прогон в свежем контейнере.
	RunClean
)

// PreflightStarted — началась дешёвая проверка Docker и воспроизводимости.
type PreflightStarted struct{}

func (PreflightStarted) isEvent() {}

// ImageLocal — образ уже лежит локально, тянуть нечего.
type ImageLocal struct {
	Image string
}

func (ImageLocal) isEvent() {}

// ImagePulling — началась тяга образа.
type ImagePulling struct {
	Image string
}

func (ImagePulling) isEvent() {}

// ContainerStarting — контейнер поднимается.
type ContainerStarting struct {
	Image string
}

func (ContainerStarting) isEvent() {}

// ContainerReady — контейнер поднялся. ID — полный идентификатор, сокращает
// подписчик.
type ContainerReady struct {
	ID      string
	WorkDir string
}

func (ContainerReady) isEvent() {}

// CodePreparing — началась подготовка кода на коммите. SHA — полный,
// сокращает подписчик.
type CodePreparing struct {
	SHA string
}

func (CodePreparing) isEvent() {}

// CodeReady — код на коммите лежит в каталоге.
type CodeReady struct {
	SHA string
	Dir string
}

func (CodeReady) isEvent() {}

// CommitFetching — коммит тянется с хоста в зеркало.
type CommitFetching struct {
	SHA  string
	Host string
}

func (CommitFetching) isEvent() {}

// CommitFetched — коммит скачан в зеркало.
type CommitFetched struct {
	SHA string
}

func (CommitFetched) isEvent() {}

// StepStarted — пошёл шаг Index из Total. Command — целиком, обрезает
// подписчик.
type StepStarted struct {
	Index   int
	Total   int
	Section string
	Command string
}

func (StepStarted) isEvent() {}

// StepsSummary — обход шагов закончен: сколько выполнено и остановился ли
// он перед упавшим.
type StepsSummary struct {
	Executed int
	Total    int
	Stopped  bool
}

func (StepsSummary) isEvent() {}

// StepsFailed — шаг Index из Total упал с кодом ExitCode. Kind различает,
// после какого прогона (RunFirst/RunClean) это случилось: пользователю в
// каждом случае говорят разное, а данные — одни и те же.
type StepsFailed struct {
	Kind     RunKind
	Index    int
	Total    int
	Section  string
	Command  string
	ExitCode int
}

func (StepsFailed) isEvent() {}

// StepsPassed — все Total шагов прошли.
type StepsPassed struct {
	Kind  RunKind
	Total int
}

func (StepsPassed) isEvent() {}

// CleanRunStarting — поднимается свежий контейнер для чистого прогона.
type CleanRunStarting struct {
	Image string
}

func (CleanRunStarting) isEvent() {}

// ContainerRemoveFailed — контейнер не снялся. Reason — текст ошибки, ID —
// полный идентификатор.
type ContainerRemoveFailed struct {
	Reason string
	ID     string
}

func (ContainerRemoveFailed) isEvent() {}

// OwnerRestoreFailed — не удалось вернуть владельца файлам рабочего
// каталога.
type OwnerRestoreFailed struct {
	Reason string
}

func (OwnerRestoreFailed) isEvent() {}

// ShellOpening — терминал уходит внутрь контейнера.
type ShellOpening struct {
	WorkDir string
}

func (ShellOpening) isEvent() {}

// CleanupStarted — началась уборка контейнера и временного чекаута.
type CleanupStarted struct{}

func (CleanupStarted) isEvent() {}

// Артефакты предыдущих стадий (Фаза 21, ART-02…ART-06). Вехи дискретные,
// без побайтового прогресса: тем же приёмом, что ImagePulling/ImageLocal,
// которые тоже не несут процента закачки, хотя образы обычно крупнее
// артефактов.

// ArtifactsFetching — начато восстановление артефактов, Total — сколько
// джоб-источников резолвнуто по dependencies/needs упавшей джобы.
type ArtifactsFetching struct {
	Total int
}

func (ArtifactsFetching) isEvent() {}

// ArtifactDownloading — тянется архив одной джобы-источника. Bytes —
// объявленная длина или 0, если сервер её не прислал.
type ArtifactDownloading struct {
	Job   string
	Bytes int64
}

func (ArtifactDownloading) isEvent() {}

// ArtifactsExtracted — архив одной джобы распакован.
type ArtifactsExtracted struct {
	Job   string
	Files int
}

func (ArtifactsExtracted) isEvent() {}

// ArtifactsReady — итог по всем источникам.
type ArtifactsReady struct {
	Files int
	Bytes int64
}

func (ArtifactsReady) isEvent() {}

// ArtifactsSkipped — восстанавливать нечего: у джобы dependencies: [] либо
// зависимостей нет вовсе. Не поломка, а честное «нечего делать».
type ArtifactsSkipped struct {
	Reason string
}

func (ArtifactsSkipped) isEvent() {}

// ArtifactsUnavailable — один источник не дался (истёк срок, нет прав,
// запись архива отвергнута защитой распаковки). Восстановление остальных
// продолжается: отказ из-за архива недельной давности не должен лишать
// человека шелла.
type ArtifactsUnavailable struct {
	Job    string
	Reason string
}

func (ArtifactsUnavailable) isEvent() {}
