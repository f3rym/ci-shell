// command.go — строка команды: двоеточие открывает набор команд, которые не
// поместились в горячие клавиши (idea-0.3.0 §2). Как в редакторах и в k9s,
// две системы управления обслуживает один и тот же обработчик — клавиша и
// команда суть два входа в одно действие, а не два действия. Этот файл
// отвечает только за разбор и поле ввода; сама диспетчеризация разобранной
// команды к обработчику (startRun, перенос правок, подмена образа,
// произвольная команда в контейнере) живёт в internal/ui/job.go, где один
// переключатель встречает и клавишу, и команду.
package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
)

// knownCommands — реестр команд, доступных в этой фазе (idea-0.3.0 §2):
// единственное место в проекте, где список команд собран целиком и виден
// одним взглядом.
//
// "log" — полный лог джобы под курсором (Фаза 10, BROW-04, D-02):
// осмысленна на экране списка джоб (internal/ui/jobs.go), где есть джоба
// под курсором; на экране джобы её место занимает панель лога локального
// прогона (план 09-02), и там она не нужна. Разбор команд остаётся
// единственным в проекте — второй горячей клавиши под полный лог не
// вводится.
var knownCommands = []string{"R", "rest", "clean", "A", "commit", "image", "log", "q", "!"}

// deferredCommands — имена, которые контракт уже называет (idea-0.3.0 §2),
// но которые принадлежат Фазе 11 (:pull — обновление рабочей копии, :env —
// полное окружение, :secrets — редактор файла секретов). Отказ обязан
// называть их честно, а не притворяться, что таких команд не существует.
var deferredCommands = []string{"pull", "env", "secrets"}

// commandBar — строка команды экрана джобы: поле ввода Bubbles, признак
// активности и текст последней ошибки разбора. Ошибка показывается строкой
// над подсказкой (internal/ui/job.go) и не закрывает поле — человек может
// поправить набранное и подтвердить снова.
type commandBar struct {
	input  textinput.Model
	active bool
	err    string
}

// newCommandBar строит поле ввода строки команды с приглашением ":" — ввод
// эхоится обычным образом, потому что команда не секрет.
func newCommandBar() commandBar {
	ti := textinput.New()
	ti.Prompt = ":"
	ti.Focus()
	return commandBar{input: ti}
}

// requiresArg сообщает, нужен ли аргумент команде name: :image (заменить
// образ на что?) и :! (выполнить что?) без аргумента бессмысленны.
func (commandBar) requiresArg(name string) bool {
	return name == "image" || name == "!"
}

// commandMsg — разобранная команда: имя и аргумент одной строкой.
// Обработчик экрана джобы (internal/ui/job.go) встречает её тем же
// переключателем, что и горячие клавиши — командный режим и клавиша это два
// входа в одно действие.
type commandMsg struct {
	Name string
	Arg  string
}

// commandErrMsg — понятный отказ разбора строки команды. parseCommand ниже
// синхронна (чистая функция без ввода-вывода), поэтому отказ сегодня
// разбирается на месте, без прохождения через отдельное сообщение
// Bubble Tea; тип объявлен по контракту плана как часть словаря сообщений
// командного режима — на случай, если разбор когда-нибудь перестанет быть
// синхронным, второе место объявления ошибки не понадобится.
type commandErrMsg struct {
	Reason string
}

// parseCommand — единственное место разбора строки команды. raw — то, что
// набрано ПОСЛЕ приглашения ":" (само приглашение не часть значения поля
// ввода).
func parseCommand(raw string) (commandMsg, error) {
	if strings.TrimSpace(raw) == "" {
		return commandMsg{}, fmt.Errorf("пустая команда")
	}

	// Строка, начинающаяся с восклицательного знака, — команда для оболочки
	// внутри контейнера (T-09-21): аргументом идёт ВЕСЬ остаток строки как
	// есть, без разбиения на поля и без единого преобразования — любая
	// нормализация здесь означала бы, что человек выполнил не то, что
	// набрал.
	if strings.HasPrefix(raw, "!") {
		arg := strings.TrimPrefix(raw, "!")
		var cb commandBar
		if cb.requiresArg("!") && arg == "" {
			return commandMsg{}, fmt.Errorf("команде :%s нужен аргумент", "!")
		}
		return commandMsg{Name: "!", Arg: arg}, nil
	}

	name, rest, found := strings.Cut(raw, " ")
	if !found {
		name, rest = raw, ""
	}
	arg := strings.TrimSpace(rest)

	for _, d := range deferredCommands {
		if name == d {
			return commandMsg{}, fmt.Errorf("команда :%s пока не реализована", name)
		}
	}

	known := false
	for _, k := range knownCommands {
		if name == k {
			known = true
			break
		}
	}
	if !known {
		return commandMsg{}, fmt.Errorf("неизвестная команда: %s", name)
	}

	var cb commandBar
	if cb.requiresArg(name) && arg == "" {
		return commandMsg{}, fmt.Errorf("команде :%s нужен аргумент", name)
	}

	return commandMsg{Name: name, Arg: arg}, nil
}
