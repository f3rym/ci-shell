// apply.go — экран переноса правок (задача 2, план 09-03, FIXUI-03): показ
// списка изменённых файлов после A/:A, перенос трёхсторонним мерджем после
// явного согласия и фиксация перенесённого командой :commit с полем ввода
// сообщения. Механика самого переноса (снятие патча, разбор отчёта, проверка
// путей, наложение трёхсторонним мерджем) не переизобретена — она целиком в
// internal/repo/patch.go (Фаза 7) и вызывается сессией
// (internal/ui/session.go: CaptureCmd/ApplyCmd/CommitCmd). Этот файл
// объявляет состояние стадий и рисует то, что видит человек; сама
// диспетчеризация клавиш и подключение к сессии — internal/ui/job.go, тем
// же приёмом, что и у строки команды (internal/ui/command.go).
package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"

	"github.com/f3rym/ci-shell/internal/repo"
)

// applyStage — стадия экрана переноса правок, по порядку прохождения
// петли: показ списка и ожидание согласия на перенос, ввод сообщения
// коммита, подтверждение фиксации. applyIdle — панель закрыта: правки ещё
// не сняты, либо перенос/фиксация уже прошли и человек продолжает чинить
// дальше.
type applyStage int

const (
	applyIdle applyStage = iota
	applyConfirm
	applyMessage
	applyCommitConfirm
)

// applyState — состояние экрана переноса правок: стадия, снятый патч,
// отчёт по изменённым файлам (repo.FileStat — тот же тип, что и у обычного
// режима переноса), расхождение базы (repo.AheadCount) и поле ввода
// сообщения коммита.
type applyState struct {
	stage applyStage

	patch repo.Patch
	stats []repo.FileStat

	ahead      int
	aheadKnown bool

	msgInput textinput.Model
	// err — последняя ошибка активной стадии (пустое сообщение коммита на
	// applyMessage) — показывается строкой над полем ввода, тем же приёмом,
	// что и commandBar.err (internal/ui/command.go).
	err string
}

// newApplyState строит состояние сразу после успешного captureDoneMsg:
// стадия — согласие на перенос, панель со списком уже готова к показу
// (idea-0.3.0 §2 требует согласия до любой записи в чужой репозиторий).
func newApplyState(patch repo.Patch, stats []repo.FileStat, ahead int, aheadKnown bool) applyState {
	return applyState{stage: applyConfirm, patch: patch, stats: stats, ahead: ahead, aheadKnown: aheadKnown}
}

// newMessageInput строит поле ввода сообщения коммита — тот же приём поля
// ввода Bubbles, что и у строки команды (command.go) и у заставки
// (splash.go): Focus() сразу; ввод эхоится обычным образом, сообщение
// коммита не секрет.
func newMessageInput() textinput.Model {
	ti := textinput.New()
	ti.Prompt = "сообщение коммита: "
	ti.Focus()
	return ti
}

// canApply истинно, когда коммит джобы найден в репозитории человека и есть
// база для трёхстороннего мерджа — иначе перенос не предлагается (только
// список), а согласие на стадии applyConfirm ничего не запускает.
func (a applyState) canApply() bool {
	return a.aheadKnown
}

// filesPanel рисует список изменённых файлов: заголовок с числом файлов,
// строка про расхождение базы (HEAD ушёл вперёд коммита джобы) или
// предупреждение про отсутствие самой базы для мерджа (коммита джобы в
// репозитории нет — перенос тогда не предлагается вовсе, только список),
// затем по строке на файл — путь и числа добавленных/удалённых строк,
// прочерк для двоичного файла. Переименование печатается обеими сторонами
// через стрелку: человек должен видеть ровно те пути, что проверены
// CheckPaths и будут записаны, а не компактную форму git с фигурными
// скобками.
func (a applyState) filesPanel(t Theme) string {
	var b strings.Builder
	b.WriteString(t.PanelTitle.Render(fmt.Sprintf("ИЗМЕНЁННЫЕ ФАЙЛЫ (%d)", len(a.stats))))
	b.WriteString("\n")

	switch {
	case !a.aheadKnown:
		b.WriteString(t.Warning.Render(fmt.Sprintf(
			"коммита джобы %s нет в этом репозитории — базы для трёхстороннего мерджа нет", a.patch.SHA)))
		b.WriteString("\n")
	case a.ahead > 0:
		b.WriteString(t.Muted.Render(fmt.Sprintf(
			"ваш HEAD ушёл на %d коммитов вперёд коммита джобы — накладываю трёхсторонним мерджем", a.ahead)))
		b.WriteString("\n")
	}

	for _, s := range a.stats {
		name := s.Path
		if s.From != "" {
			name = s.From + " → " + s.Path
		}
		added, deleted := "-", "-"
		if s.Added >= 0 {
			added = fmt.Sprintf("+%d", s.Added)
		}
		if s.Deleted >= 0 {
			deleted = fmt.Sprintf("-%d", s.Deleted)
		}
		b.WriteString(fmt.Sprintf("  %s   %s %s\n", name, added, deleted))
	}
	return strings.TrimRight(b.String(), "\n")
}

// messageView рисует поле ввода сообщения коммита (стадия applyMessage):
// последняя ошибка (пустое сообщение) — строкой над полем, тем же приёмом,
// что и у строки команды.
func (a applyState) messageView(t Theme) string {
	var b strings.Builder
	if a.err != "" {
		b.WriteString(RenderHint(t, a.err))
		b.WriteString("\n")
	}
	b.WriteString(a.msgInput.View())
	return b.String()
}

// commitConfirmPrompt — вопрос стадии applyCommitConfirm: число файлов и
// корень репозитория человека, куда уйдёт запись, — оба явно, потому что
// запись в чужой репозиторий без ответа человека недопустима (тот же
// предохранитель, что и у переноса).
func (a applyState) commitConfirmPrompt(root string) string {
	return fmt.Sprintf("зафиксировать %d файлов в %s? (y/n)", len(a.stats), root)
}

// discardedNotice — текст после отказа от переноса (клавиша отказа или
// возврат на стадии applyConfirm): патч остаётся файлом на диске, и его
// путь называется явно — человек должен иметь возможность наложить его
// руками (тот же контракт, что у обычного режима переноса, cmd/ci/main.go).
func (a applyState) discardedNotice() string {
	return fmt.Sprintf("не применено, патч остался файлом: %s", a.patch.Path)
}

// emptyMessageNotice — отказ стадии applyMessage: сообщение коммита пустое
// после обрезки пробельных символов, поле остаётся открытым.
func emptyMessageNotice() string {
	return "сообщение коммита не может быть пустым"
}
