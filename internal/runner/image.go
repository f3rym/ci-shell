package runner

// DefaultImage — образ, подставляемый утилитой, когда в конфиге джобы нет
// image: и пользователь не задал --image. Лёгкий официальный образ, а не
// образ семейства act (гигабайты на первый запуск) — первое впечатление от
// «одной команды» не должно быть пятиминутным ожиданием (idea §10). Факт
// подстановки всё равно уезжает в баннер (render.Banner), а несогласный
// пользователь получает флаг --image.
const DefaultImage = "ubuntu:22.04"

// ImageSource — источник итогового образа контейнера. Значения — русский
// текст, который печатается пользователю как есть (тот же приём, что у
// env.Source).
type ImageSource string

const (
	// ImageSourceConfig — образ взят из image: конфига джобы.
	ImageSourceConfig ImageSource = "конфиг джобы"
	// ImageSourceFlag — образ задан флагом --image.
	ImageSourceFlag ImageSource = "флаг --image"
	// ImageSourceDefault — образ подставлен утилитой (DefaultImage).
	ImageSourceDefault ImageSource = "дефолт утилиты"
)

// ImageChoice — результат выбора образа контейнера.
type ImageChoice struct {
	// Ref — образ, который реально пойдёт в контейнер. Не бывает пустым.
	Ref string
	// Source — источник Ref.
	Source ImageSource
	// Configured — образ, который был в конфиге джобы. Пусто, если его там
	// не было. Нужно только для честной строки баннера «заменён флагом: X
	// вместо Y».
	Configured string
}

// ResolveImage выбирает образ контейнера по приоритету: непустой override
// (флаг пользователя --image) → непустой configured (образ из конфига
// джобы) → DefaultImage. Ref результата не бывает пустым — на этом
// инварианте держится весь CLI-07. Функция ничего не печатает и не ходит
// наружу: чистое решение по двум строкам.
func ResolveImage(configured, override string) ImageChoice {
	if override != "" {
		return ImageChoice{Ref: override, Source: ImageSourceFlag, Configured: configured}
	}
	if configured != "" {
		return ImageChoice{Ref: configured, Source: ImageSourceConfig, Configured: configured}
	}
	return ImageChoice{Ref: DefaultImage, Source: ImageSourceDefault, Configured: configured}
}
