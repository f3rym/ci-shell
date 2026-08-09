// guide.go — экран-проводник по типичным поломкам (Фаза 11, GUIDE-01…05).
//
// Это переезд словаря, который сегодня живёт в функции explain подкоманд
// (cmd/ci/main.go) — не вторая его реализация. Разница одна и главная: там
// словарь вёл к завершению процесса, здесь — к экрану, на котором человек
// может ответить. Ситуация опознаётся сравнением с sentinel-ошибкой домена
// (errors.Is), а не разбором текста: текст ошибки — это сообщение
// человеку, и строить на нём логику значит сломать её первой же
// переформулировкой.
package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"

	"github.com/f3rym/ci-shell/internal/cache"
	"github.com/f3rym/ci-shell/internal/config"
	"github.com/f3rym/ci-shell/internal/env"
	"github.com/f3rym/ci-shell/internal/joburl"
	"github.com/f3rym/ci-shell/internal/provider"
	"github.com/f3rym/ci-shell/internal/repo"
	"github.com/f3rym/ci-shell/internal/runner"
	"github.com/f3rym/ci-shell/internal/token"
)

// guideKind — форма экрана-проводника.
type guideKind int

const (
	// guideInput — поле ввода.
	guideInput guideKind = iota
	// guideConfirm — вопрос да/нет.
	guideConfirm
	// guideCommands — готовые команды.
	guideCommands
	// guideList — список строк. Форма объявляется в плане 11-01, наполняется
	// планом 11-02 (недостающие переменные и полное окружение).
	guideList
)

// guideWhere — место, где случилась поломка. Одна и та же ошибка означает
// разные экраны в разных местах — отсутствие рабочей копии при разборе
// номера джобы и при переносе правок требует разных слов.
type guideWhere int

const (
	// whereSplash — провалившаяся проверка заставки.
	whereSplash guideWhere = iota
	// whereSession — подготовка воспроизведения.
	whereSession
	// whereApply — перенос правок. Наполняется планом 11-02.
	whereApply
)

// guideAction — действие, которое человек выбирает на экране проводника.
// Перечисление объявлено целиком планом 11-01: половину наполняет план
// 11-02, и второе перечисление действий в проекте появляться не должно.
type guideAction int

const (
	actionNone guideAction = iota
	actionSaveToken
	actionSaveDataDir
	actionSetImage
	actionEditSecrets
	actionPull
	actionRetryPrepare
	actionRetryChecks
	actionRetryApply
)

// guide — описание одного экрана проводника. Описание — данные, а не
// поведение; поведение живёт в guideModel и в обработчиках вызывающих
// экранов (splash.go, job.go).
type guide struct {
	Kind  guideKind
	Where guideWhere

	Title  string
	Reason string

	// Label/Placeholder/Suggested/Hidden — поле ввода (guideInput).
	Label       string
	Placeholder string
	Suggested   string
	Hidden      bool

	// Question — текст вопроса (guideConfirm).
	Question string

	// Commands — готовые команды (guideCommands).
	Commands []string
	// Link — ссылка на страницу настроек хоста, приглушённым стилем под
	// полем/командами.
	Link string

	// List — строки списка (guideList), и приписка под готовыми командами
	// (guideCommands), если она непуста.
	List []string

	// Confirm — действие подтверждения: ⏎ на форме ввода, y на форме
	// вопроса.
	Confirm guideAction
	// Retry — действие повтора: ⏎ на форме готовых команд и на списке.
	Retry guideAction

	// Hint — текст строки подсказки экрана.
	Hint string
}

// tokenSettingsURL — единственная формула адреса страницы токенов хоста:
// три ветви словаря подставляют её сюда, а не хранят три копии, которые
// разъехались бы при первом же изменении адреса на self-hosted инстансе.
const tokenSettingsURL = "https://%s/-/user_settings/personal_access_tokens"

// sshKeysURLFmt — адрес страницы SSH-ключей хоста.
const sshKeysURLFmt = "https://%s/-/user_settings/ssh_keys"

// dockerInstallURL — страница установки Docker.
const dockerInstallURL = "https://docs.docker.com/engine/install/"

// tokenLabel — подпись поля ввода токена, общая для веток «нет токена» и
// «токен отклонён»: обе ветви ведут к одной и той же форме ввода.
func tokenLabel(host string) string {
	return fmt.Sprintf("токен для %s (областей read_api и read_repository достаточно):", host)
}

// tokenConfigPath — путь конфиг-файла токенов известной формулой, а не
// разбором текста ошибки: token.configPath не экспортирован, поэтому здесь
// он вычисляется той же логикой предпочтения имени (config.yml, затем
// config.yaml), что и в internal/token/file.go.
func tokenConfigPath() string {
	dir, err := config.Dir()
	if err != nil {
		return ""
	}
	for _, name := range []string{"config.yml", "config.yaml"} {
		p := filepath.Join(dir, name)
		if _, statErr := os.Stat(p); statErr == nil {
			return p
		}
	}
	return filepath.Join(dir, "config.yml")
}

// retryForWhere выбирает действие повтора по месту поломки: перезапуск
// проверок заставки, перезапуск подготовки воспроизведения или повтор
// переноса правок.
func retryForWhere(where guideWhere) guideAction {
	switch where {
	case whereSplash:
		return actionRetryChecks
	case whereApply:
		return actionRetryApply
	default:
		return actionRetryPrepare
	}
}

// guideFor — единственный словарь распознавания ситуаций в проекте. err —
// сама ошибка домена (не текст); where и ref уточняют экран.
func guideFor(err error, where guideWhere, ref joburl.Ref) (guide, bool) {
	if err == nil {
		return guide{}, false
	}
	// Отмена контекста человеком (Ctrl-C/SIGTERM) — осознанное действие, а
	// не поломка: экрана проводника здесь нет вовсе, тот же приём, что и в
	// словаре подкоманд (cmd/ci/main.go, explain).
	if errors.Is(err, context.Canceled) {
		return guide{}, false
	}

	switch {
	case errors.Is(err, token.ErrNoToken):
		return guide{
			Kind:   guideInput,
			Where:  where,
			Title:  "нет токена",
			Reason: "ci-shell ходит в API GitLab и за кодом коммита от вашего имени — без токена не сделать ни того, ни другого",
			Label:  tokenLabel(ref.Host),
			Hidden: true,
			Link:   fmt.Sprintf(tokenSettingsURL, ref.Host),
			Confirm: actionSaveToken,
			Retry:   retryForWhere(where),
			Hint:    HintTokenNeeded(ref.Host),
		}, true

	case errors.Is(err, provider.ErrUnauthorized):
		return guide{
			Kind:    guideInput,
			Where:   where,
			Title:   "токен отклонён",
			Reason:  fmt.Sprintf("%s ответил отказом: токен просрочен, отозван или у него нет нужных областей", ref.Host),
			Label:   tokenLabel(ref.Host),
			Hidden:  true,
			Link:    fmt.Sprintf(tokenSettingsURL, ref.Host),
			Confirm: actionSaveToken,
			Retry:   retryForWhere(where),
			Hint:    HintTokenRejected(ref.Host),
		}, true

	case errors.Is(err, token.ErrInsecurePermissions):
		// Путь в команду подставляется не разбором текста ошибки, а
		// известной формулой пути конфига (tokenConfigPath) — экран
		// показывает ту же команду, что печатает словарь подкоманд.
		return guide{
			Kind:     guideCommands,
			Where:    where,
			Title:    "файл токенов доступен не только вам",
			Reason:   "права конфиг-файла токенов шире 0600 — читать его небезопасно",
			Commands: []string{fmt.Sprintf("chmod 600 %s", tokenConfigPath())},
			Retry:    retryForWhere(where),
			Hint:     HintFixPerms(),
		}, true

	case errors.Is(err, repo.ErrRepoScope):
		return guide{
			Kind:    guideInput,
			Where:   where,
			Title:   "токену не хватает доступа к коду",
			Reason:  "API отвечает, а git-протокол отказывает: токен видит метаданные джобы, но не видит сам код — это отдельная область доступа",
			Label:   fmt.Sprintf("новый токен для %s (нужна область read_repository):", ref.Host),
			Hidden:  true,
			Link:    fmt.Sprintf(tokenSettingsURL, ref.Host),
			Confirm: actionSaveToken,
			Retry:   retryForWhere(where),
			Hint:    HintTokenScope(ref.Host),
		}, true

	case errors.Is(err, runner.ErrDockerUnavailable):
		return guide{
			Kind:   guideCommands,
			Where:  where,
			Title:  "Docker не найден",
			Reason: "Docker — единственная внешняя зависимость ci-shell: контейнер джобы поднимать больше нечем",
			Commands: []string{
				"docker --version",
				"snap list docker",
			},
			List:  []string{"snap-сборка живёт по своим правилам — если она у вас, следующий экран объяснит, что с этим делать"},
			Link:  dockerInstallURL,
			Retry: retryForWhere(where),
			Hint:  HintDockerMissing(),
		}, true

	case errors.Is(err, runner.ErrDaemonUnavailable):
		return guide{
			Kind:   guideCommands,
			Where:  where,
			Title:  "демон Docker не отвечает",
			Reason: "клиент есть, демон не отвечает — контейнер поднимать некому",
			Commands: []string{
				"systemctl --user start docker",
				"sudo systemctl start docker",
				"docker info",
			},
			Retry: retryForWhere(where),
			Hint:  HintDaemonDown(),
		}, true

	case errors.Is(err, runner.ErrCacheNotVisible):
		suggested := cache.Suggested()
		return guide{
			Kind:     guideConfirm,
			Where:    where,
			Title:    "демон не видит каталог данных",
			Reason:   "профиль безопасности snap-сборки Docker исключает каталоги, имя которых начинается с точки: файл окружения и код джобы лежат как раз в таком каталоге, и демон их не открывает",
			Question: fmt.Sprintf("переключить каталог данных в %s? (y/n)", suggested),
			Suggested: suggested,
			Confirm:   actionSaveDataDir,
			Retry:     retryForWhere(where),
			Hint:      HintCacheHidden(suggested),
		}, true

	case errors.Is(err, cache.ErrUnavailable), errors.Is(err, cache.ErrInvalidDir):
		return guide{
			Kind:        guideInput,
			Where:       where,
			Title:       "не найден каталог для данных",
			Reason:      "утилите нужен постоянный каталог для файла окружения, кода джобы и зеркал репозиториев",
			Label:       "абсолютный путь к каталогу данных:",
			Placeholder: cache.Suggested(),
			Suggested:   cache.Suggested(),
			Confirm:     actionSaveDataDir,
			Retry:       retryForWhere(where),
			Hint:        HintDataDirNeeded(),
		}, true

	case errors.Is(err, repo.ErrSSHKeyRequired):
		return guide{
			Kind:   guideCommands,
			Where:  where,
			Title:  "git просит ключ",
			Reason: "рабочая копия ходит на ваш собственный удалённый репозиторий вашими ключами — токен ci-shell её не обновит",
			Commands: []string{
				`ssh-keygen -t ed25519 -C "<ваша почта>"`,
				"cat ~/.ssh/id_ed25519.pub",
				fmt.Sprintf("ssh -T git@%s", ref.Host),
			},
			Link:  fmt.Sprintf(sshKeysURLFmt, ref.Host),
			Retry: actionPull,
			Hint:  HintSSHKey(ref.Host),
		}, true

	case errors.Is(err, repo.ErrPullFailed):
		return guide{
			Kind:   guideCommands,
			Where:  where,
			Title:  "рабочая копия не обновилась",
			Reason: "git отказался обновлять копию — посмотрите её состояние и повторите (команды — в каталоге рабочей копии)",
			Commands: []string{
				"git status",
				"git pull --ff-only",
			},
			Retry: actionPull,
			Hint:  HintPullFailed(err.Error()),
		}, true

	case errors.Is(err, provider.ErrUnresolvedRefs), errors.Is(err, runner.ErrNoImage):
		return guide{
			Kind:        guideInput,
			Where:       where,
			Title:       "конфиг использует extends",
			Reason:      "образ и шаги этой джобы собраны из другой записи конфига, а раскрытие extends в этой версии не делается: образ можно задать руками — шелл откроется, и контейнер будет тот же",
			Label:       "образ для контейнера (например, python:3.12-alpine):",
			Placeholder: runner.DefaultImage,
			Suggested:   runner.DefaultImage,
			Confirm:     actionSetImage,
			Retry:       retryForWhere(where),
			Hint:        HintUnresolvedConfig(),
		}, true

	case errors.Is(err, repo.ErrNoPatch):
		return guide{
			Kind:   guideCommands,
			Where:  where,
			Title:  "правок пока нет",
			Reason: "перенос берёт правки, сохранённые после починки шага, — сначала почините шаг в контейнере",
			Retry:  actionRetryApply,
			Hint:   HintNoPatchYet(),
		}, true

	case where == whereApply && (errors.Is(err, repo.ErrNotAGitRepo) || errors.Is(err, repo.ErrNoOrigin) || errors.Is(err, repo.ErrRemoteUnparsable)):
		// Строки списка (путь сохранённого патча) оставлены пустыми — их
		// заполняет вызывающий экран (job.go): словарь про сессию ничего не
		// знает и знать не должен.
		return guide{
			Kind:   guideCommands,
			Where:  where,
			Title:  "перенос идёт в рабочую копию",
			Reason: "правки уже сохранены на диск, их некуда наложить: текущий каталог не рабочая копия этого проекта",
			Commands: []string{
				"cd <ваша рабочая копия>",
				fmt.Sprintf("ci apply --job %d", ref.JobID),
			},
			Retry: actionRetryApply,
		}, true
	}

	return guide{}, false
}

// guideForMissing — экран недостающих значений маскированных переменных
// (Фаза 11, GUIDE-03). Не часть словаря по ошибке (job.go зовёт её
// напрямую, когда готовая сессия несёт непустой список недостающих
// переменных — это не отказ, а честное состояние собранного окружения).
func guideForMissing(missing []env.Missing, secretsPath string) guide {
	list := make([]string, 0, len(missing)+1)
	for _, m := range missing {
		origin := m.Origin
		if origin == "" {
			origin = "—"
		}
		list = append(list, fmt.Sprintf("%s — известна от %s", m.Key, origin))
	}
	list = append(list, fmt.Sprintf("⏎ откроет файл секретов %s в редакторе", secretsPath))

	return guide{
		Kind:   guideList,
		Where:  whereSession,
		Title:  "не хватает значений переменных",
		Reason: "GitLab не отдаёт значения переменных типа masked_and_hidden ни одному API — их знает только тот, кто их заводил",
		List:   list,
		Retry:  actionEditSecrets,
		Hint:   HintSecretsMissing(len(missing)),
	}
}

// guideModel — модель экрана проводника: описание, поле ввода Bubbles,
// строка ошибки последнего ответа, тема, раскладка клавиш, ширина и высота.
type guideModel struct {
	Guide guide
	Input textinput.Model
	Err   string

	Theme Theme
	Keys  KeyMap

	Width, Height int
}

// newGuideModel строит модель экрана проводника: для формы ввода создаёт
// поле, выставляет подпись-заполнитель и предлагаемое значение как
// начальное, а при скрытом вводе переводит поле в режим скрытого эха. Токен
// на экране не эхоится и на экране не остаётся — тот же принцип, по
// которому вопрос в терминале гасит эхо, только средствами поля ввода.
func newGuideModel(g guide, t Theme, keys KeyMap) guideModel {
	m := guideModel{Guide: g, Theme: t, Keys: keys}
	if g.Kind == guideInput {
		ti := textinput.New()
		ti.Placeholder = g.Placeholder
		if g.Suggested != "" {
			ti.SetValue(g.Suggested)
		}
		if g.Hidden {
			ti.EchoMode = textinput.EchoPassword
		}
		ti.Focus()
		m.Input = ti
	}
	return m
}

// guideMsg — открыть экран проводника описанием g.
type guideMsg struct {
	Guide guide
}

// guideDoneMsg — человек ответил: Action — что делать, Value — введённое
// или предложенное значение (пусто для форм без значения).
type guideDoneMsg struct {
	Action guideAction
	Value  string
}

// guideDismissMsg — человек вернулся назад: возврат на предыдущий экран, а
// не выход.
type guideDismissMsg struct{}

// Update — ни одна ветвь этого метода не завершает программу: это и есть
// требование фазы, а не деталь.
func (m guideModel) Update(msg tea.Msg) (guideModel, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		if m.Guide.Kind == guideInput {
			var cmd tea.Cmd
			m.Input, cmd = m.Input.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	switch m.Guide.Kind {
	case guideInput:
		return m.updateInputKey(keyMsg)
	case guideConfirm:
		return m.updateConfirmKey(keyMsg)
	default:
		return m.updateRetryKey(keyMsg)
	}
}

// saveDataDir — точечное обновление настроек и сброс однократного
// вычисления базы каталога данных (cache.Base): сохранение каталога данных
// идёт этой единственной операцией, соседние поля настроек не затираются.
func saveDataDir(value string) error {
	if err := cache.ValidDir(value); err != nil {
		return err
	}
	if _, err := config.Update(func(s *config.Settings) { s.CacheDir = value }); err != nil {
		return err
	}
	cache.Reload()
	return nil
}

// updateInputKey — привязка открытия подтверждает введённое: пустое
// значение не подтверждается, строка ошибки становится «значение не может
// быть пустым», и экран остаётся открытым. Действие сохранения каталога
// данных проверяется единственной доменной проверкой прямо здесь — второй
// проверки пути в файле экрана нет. Привязка возврата — guideDismissMsg.
func (m guideModel) updateInputKey(msg tea.KeyPressMsg) (guideModel, tea.Cmd) {
	switch {
	case msg.String() == "enter":
		value := strings.TrimSpace(m.Input.Value())
		if value == "" {
			m.Err = "значение не может быть пустым"
			return m, nil
		}
		action := m.Guide.Confirm
		if action == actionSaveDataDir {
			if err := saveDataDir(value); err != nil {
				m.Err = err.Error()
				return m, nil
			}
		}
		// Значение живёт в поле до этого момента и не дольше: модель экрана
		// живёт до конца сессии, а значение токена не должно жить дольше
		// одного подтверждения.
		m.Input.SetValue("")
		return m, func() tea.Msg { return guideDoneMsg{Action: action, Value: value} }
	case key.Matches(msg, m.Keys.Back):
		return m, func() tea.Msg { return guideDismissMsg{} }
	}
	var cmd tea.Cmd
	m.Input, cmd = m.Input.Update(msg)
	return m, cmd
}

// updateConfirmKey — согласие (y) даёт guideDoneMsg с действием
// подтверждения и предлагаемым значением, отказ (n) — guideDismissMsg.
func (m guideModel) updateConfirmKey(msg tea.KeyPressMsg) (guideModel, tea.Cmd) {
	switch msg.String() {
	case "y":
		action, value := m.Guide.Confirm, m.Guide.Suggested
		if action == actionSaveDataDir {
			if err := saveDataDir(value); err != nil {
				m.Err = err.Error()
				return m, nil
			}
		}
		return m, func() tea.Msg { return guideDoneMsg{Action: action, Value: value} }
	case "n":
		return m, func() tea.Msg { return guideDismissMsg{} }
	}
	if key.Matches(msg, m.Keys.Back) {
		return m, func() tea.Msg { return guideDismissMsg{} }
	}
	return m, nil
}

// updateRetryKey — привязка открытия на форме готовых команд и на списке
// возвращает guideDoneMsg с действием повтора: ⏎ на таком экране означает
// «я сделал, повторите».
func (m guideModel) updateRetryKey(msg tea.KeyPressMsg) (guideModel, tea.Cmd) {
	switch {
	case msg.String() == "enter":
		action := m.Guide.Retry
		return m, func() tea.Msg { return guideDoneMsg{Action: action} }
	case key.Matches(msg, m.Keys.Back):
		return m, func() tea.Msg { return guideDismissMsg{} }
	}
	return m, nil
}

// hintText — строка подсказки экрана проводника.
func (m guideModel) hintText() string {
	return m.Guide.Hint
}

// View рисует заголовок жирным, причину обычным текстом с переносом по
// доступной ширине, тело по форме и строку ошибки последнего ответа цветом
// отказа, если она непуста. Ширина всех обрезаемых строк считается функцией
// обрезки темы (Truncate) — ручного выравнивания форматным глаголом с
// фиксированной шириной здесь нет.
func (m guideModel) View(width, height int) string {
	if width <= 0 {
		width = MinWidth - 2*OuterMargin
	}

	var b strings.Builder
	b.WriteString(m.Theme.ScreenTitle.Render(m.Guide.Title))
	b.WriteString("\n\n")
	if m.Guide.Reason != "" {
		b.WriteString(lipgloss.NewStyle().Width(width).Render(m.Guide.Reason))
		b.WriteString("\n\n")
	}

	switch m.Guide.Kind {
	case guideInput:
		if m.Guide.Label != "" {
			b.WriteString(m.Theme.Text.Render(m.Guide.Label))
			b.WriteString("\n")
		}
		b.WriteString(m.Input.View())
		if m.Guide.Link != "" {
			b.WriteString("\n")
			b.WriteString(m.Theme.Muted.Render(m.Guide.Link))
		}
	case guideConfirm:
		b.WriteString(m.Theme.Text.Render(m.Guide.Question))
	case guideCommands:
		for _, c := range m.Guide.Commands {
			b.WriteString("  " + m.Theme.Text.Render(c))
			b.WriteString("\n")
		}
		for _, l := range m.Guide.List {
			b.WriteString(m.Theme.Muted.Render(Truncate(l, width)))
			b.WriteString("\n")
		}
		if m.Guide.Link != "" {
			b.WriteString(m.Theme.Muted.Render(m.Guide.Link))
		}
	case guideList:
		for _, l := range m.Guide.List {
			b.WriteString(Truncate(l, width))
			b.WriteString("\n")
		}
	}

	out := strings.TrimRight(b.String(), "\n")
	if m.Err != "" {
		out += "\n\n" + m.Theme.Danger.Render(m.Err)
	}
	return out
}
