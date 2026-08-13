// secret.go — вид «секреты» правой колонки экрана джобы (Фаза 15, план
// 15-02, idea-0.3.1 §6): отдельный файл, потому что у него своя строка
// (secretRow), своя сборка из окружения сессии (secretRowsFromSession) и
// (задача 3) своё поле ввода значения — модель экрана джобы и без того
// самая большая в пакете.
package ui

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"

	"github.com/f3rym/ci-shell/internal/env"
	"github.com/f3rym/ci-shell/internal/render"
)

// secretRow — одна строка вида «секреты»: имя переменной, источник (путь
// проекта или группы, приславшей её маскированной) и уже принятое решение о
// показе (для заполненной переменной — то, что вернула render.DisplayValue).
// Поля под сырое значение у типа НЕТ — тот же приём, что уже применён к
// типу недостающей переменной домена (env.Missing): тип, в котором значению
// негде поместиться, не может его случайно вынести наружу. Заполненность —
// отдельное булево поле.
type secretRow struct {
	Key     string
	Origin  string
	Display string
	Filled  bool
}

// secretRowsFromSession собирает строки вида «секреты» из окружения сессии:
// сначала незаполненные (перечень недостающих переменных, env.Missing),
// потом заполненные (переменные с признаком секрета — отображение считает
// та же функция, что и для панели окружения, второй ветки показа не
// появляется), внутри каждой группы по имени. Строка, требующая действия,
// не должна прятаться в середине алфавитного списка — именно ради неё вид
// и открывают.
func secretRowsFromSession(s *Session) []secretRow {
	missing := make(map[string]bool, len(s.environment.Missing))
	rows := make([]secretRow, 0, len(s.environment.Missing))
	for _, m := range s.environment.Missing {
		rows = append(rows, secretRow{Key: m.Key, Origin: m.Origin, Filled: false})
		missing[m.Key] = true
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })

	var filled []secretRow
	for _, v := range s.environment.Vars {
		if !v.Secret || missing[v.Key] {
			continue
		}
		filled = append(filled, secretRow{Key: v.Key, Origin: v.Origin, Display: render.DisplayValue(v), Filled: true})
	}
	sort.Slice(filled, func(i, j int) bool { return filled[i].Key < filled[j].Key })

	return append(rows, filled...)
}

// line отрисовывает одну строку вида «секреты»: имя, затем либо отображение
// приглушённым стилем, либо отметка «не задано» текстовым восклицательным
// знаком в предупреждающем стиле — контракт запрещает символ (⚠) вне
// замкнутого набора, «!» остаётся обычным текстом. Обрезка идёт ДО
// раскраски (тот же приём, что и подсветка совпадений просмотрщика лога,
// internal/ui/logview.go, highlight): управляющие последовательности стиля
// разъехали бы графемную меру ширины. Выбранная строка получает символ
// курсора справа и инверсию на всю строку — ровно как строки шагов и джоб.
// Ручного выравнивания форматным глаголом нет.
func (r secretRow) line(t Theme, width int, selected bool) string {
	statusText := "не задано !"
	style := t.Warning
	if r.Filled {
		statusText = r.Display
		style = t.Muted
	}
	trimmed := Truncate(r.Key+" "+statusText, width)
	name, status, found := strings.Cut(trimmed, " ")
	line := name
	if found {
		line += " " + style.Render(status)
	}
	if selected {
		return t.Selected.Render(line + " " + GlyphCursor)
	}
	return line
}

// secretPrompt — поле ввода значения переменной секрета (Фаза 15, план
// 15-02, JOB-02) И, с Фазы 20 (план 20-02, VAR-01), поле ввода значения
// ЛЮБОЙ обычной переменной, которую домен признал правимой (env.Editable,
// план 20-01): одно и то же поле обслуживает оба класса, различённых полем
// persist — значение уходит на диск через env.SaveSecret (секретная
// переменная), когда persist истинно; когда ложно — значение живёт только в
// памяти сессии (Session.recordOverride) и на диск не попадает никогда.
// Режим эха (скрытый/видимый) держится отдельным параметром конструктора, а
// не выводится из persist: секрет и обычная переменная сегодня совпадают по
// обоим признакам (секрет всегда скрыт и всегда сохраняется), но называть
// одно через другое значило бы утверждать связь, которой в домене нет —
// env.Editable ничего не знает про эхо, а видимость поля — забота экрана.
type secretPrompt struct {
	key     string
	input   textinput.Model
	active  bool
	err     string
	persist bool
}

// newSecretPrompt собирает поле ввода: hidden решает режим эха (скрытый —
// тот же режим, что у поля токена проводника, internal/ui/guide.go, — или
// видимый, для обычной переменной, Фаза 20), persist решает, уйдёт ли
// подтверждённое значение на диск (секрет) или останется только в памяти
// сессии (обычная переменная) — см. комментарий над типом secretPrompt.
// Приглашением ставится имя переменной, полю отдаётся фокус и возвращается
// команда мигания курсора.
func newSecretPrompt(key string, hidden, persist bool) (secretPrompt, tea.Cmd) {
	ti := textinput.New()
	ti.Prompt = key + ": "
	if hidden {
		ti.EchoMode = textinput.EchoPassword
	}
	ti.Focus()
	return secretPrompt{key: key, input: ti, active: true, persist: persist}, textinput.Blink
}

// updateKey — подтверждение: набранное обрезается по краям, пустое не
// принимается (строка ошибки, поле остаётся открытым — человек может
// дописать), непустое СРАЗУ вычищается из поля, а само значение уходит
// наружу сообщением secretEnteredMsg. Модель экрана живёт до конца сессии,
// а значение секрета не должно жить дольше одного подтверждения — тот же
// принцип, что и у поля токена проводника. Привязка возврата закрывает
// поле, вычищая его; любая другая клавиша — обычный ввод.
func (p secretPrompt) updateKey(msg tea.KeyPressMsg, k KeyMap) (secretPrompt, tea.Cmd) {
	switch {
	case msg.String() == "enter":
		value := strings.TrimSpace(p.input.Value())
		if value == "" {
			p.err = "значение не может быть пустым"
			return p, nil
		}
		varKey := p.key
		persist := p.persist
		p.input.SetValue("")
		p.active = false
		return p, func() tea.Msg { return secretEnteredMsg{Key: varKey, Value: value, Persist: persist} }
	case key.Matches(msg, k.Back):
		p.input.SetValue("")
		p.active = false
		return p, nil
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return p, cmd
}

// view — вид поля на месте строки клавиш: строка ошибки над полем тем же
// приёмом, что у строки команды (internal/ui/command.go), затем само поле.
func (p secretPrompt) view(t Theme) string {
	var b strings.Builder
	if p.err != "" {
		b.WriteString(RenderHint(t, p.err))
		b.WriteString("\n")
	}
	b.WriteString(p.input.View())
	return b.String()
}

// secretEnteredMsg — человек подтвердил значение переменной Key. Значение
// живёт ровно в этом сообщении и в замыкании команды записи (saveSecretCmd,
// только когда Persist истинно) и нигде больше: в модель оно не кладётся, в
// баннер и подсказку не попадает, в лог не пишется. Persist — тот же
// признак, что был у secretPrompt в момент подтверждения (Фаза 20, план
// 20-02): решает, уйдёт ли Value на диск (saveSecretCmd) или в память сессии
// (Session.recordOverride) — обработчик в internal/ui/job.go ветвится по
// нему.
type secretEnteredMsg struct {
	Key     string
	Value   string
	Persist bool
}

// secretSavedMsg — итог записи секрета: имя переменной, путь файла и
// ошибка. Значения здесь нет и быть не может — доменная функция уже
// гарантирует, что в тексте её ошибки значения нет (internal/env/secrets.go).
type secretSavedMsg struct {
	Key  string
	Path string
	Err  error
}

// saveSecretCmd — ЕДИНСТВЕННОЕ место вызова доменной записи секрета из
// интерфейса: команда идёт в горутине, потому что запись ходит на диск и
// подвесила бы отрисовку, и возвращает итог сообщением. Ошибка кладётся в
// сообщение как есть — доменная функция уже гарантирует, что в её тексте
// нет значения (env.SaveSecret).
func saveSecretCmd(host, projectPath, key, value string) tea.Cmd {
	return func() tea.Msg {
		path, err := env.SaveSecret(host, projectPath, key, value)
		return secretSavedMsg{Key: key, Path: path, Err: err}
	}
}
