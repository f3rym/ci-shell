// Файл секретов имеет два пишущих входа, и оба обязаны быть
// неразрушающими (Фаза 15, план 15-02, idea-0.3.1 §8: «перезаписывать чужое
// нельзя — только добавлять и заменять по ключу, сохраняя комментарии»).
// Первый (EnsureSecretsFile) создаёт файл, которого нет. Второй (SaveSecret)
// меняет ОДНУ строку уже существующего файла. Оба пишут через временный файл
// с явными правами и переименованием поверх — третьего приёма записи в
// проекте нет (тот же, что у токенов и настроек).
package env

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/f3rym/ci-shell/internal/config"
)

// ErrInsecureSecrets означает: файл секретов найден на диске, но его права
// шире 0600, поэтому читать его небезопасно (T-02-08, T-02-12).
var ErrInsecureSecrets = errors.New("права файла секретов шире 0600")

// ErrInvalidSecretName — имя переменной не годится в ключ YAML. Имя приходит
// из окружения, собранного по ответу чужого сервера, а уходит в файл ключом
// YAML: двоеточие, перевод строки или решётка в нём перестроили бы документ
// и унесли бы чужие записи в чужие места.
var ErrInvalidSecretName = errors.New("имя переменной не годится в ключ YAML")

// ErrInvalidSecretValue — значение не годится в однострочный скаляр YAML:
// перевод строки развалил бы однострочный скаляр, а управляющая
// последовательность доехала бы до переменной окружения контейнера и до
// кадра терминала.
var ErrInvalidSecretValue = errors.New("значение не годится в однострочный скаляр YAML")

// ErrSecretNotEditable — значение этого ключа записано в файле в форме,
// которую точечная правка не переписывает (многострочный или блочный
// скаляр, якорь, ссылка): отказать честно дешевле, чем испортить файл,
// который человек правил руками.
var ErrSecretNotEditable = errors.New("значение записано в форме, которую точечная правка не переписывает")

// secretsFile — верхний уровень файла секретов: карта проектов
// (<хост>/<путь проекта>) на карту их переменных.
type secretsFile struct {
	Projects map[string]map[string]string `yaml:"projects"`
}

// Secrets — результат чтения локального файла секретов для одного проекта.
type Secrets struct {
	// Path — фактический путь к файлу секретов: существующий, если он есть
	// на диске, иначе первое по порядку предпочтения имя. Нужен, чтобы
	// сообщение пользователю называло настоящий файл, а не шаблон.
	Path string
	// Values — значения переменных проекта, если файл найден и содержит
	// для него запись. Пустая карта (не nil), если файл найден, но записи
	// для проекта нет.
	Values map[string]string
	// Found — файл секретов существует на диске. Отсутствие файла — не
	// ошибка, а штатная ситуация «пользователь ещё не заполнял».
	Found bool
}

// secretsNames — имена файла секретов в порядке предпочтения: если оба
// существуют, побеждает первый.
var secretsNames = []string{"secrets.yml", "secrets.yaml"}

// SecretsPath возвращает путь к файлу секретов — каталог берётся у
// config.Dir(), той же формулы, что использует token.configPath
// (internal/token/file.go). Если ни одно из secretsNames не существует на
// диске, возвращается путь к первому имени (secrets.yml) без ошибки —
// отсутствие файла секретов не является ошибкой на этом уровне. Экспортирована,
// потому что тот же путь нужен подкоманде ci secrets (cmd/ci/main.go) — вторая
// формула пути в проекте недопустима.
func SecretsPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", fmt.Errorf("env: %w", err)
	}

	first := filepath.Join(dir, secretsNames[0])
	for _, name := range secretsNames {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return first, nil
}

// LoadSecrets читает локальный файл секретов для проекта host+projectPath.
//
// Отсутствие файла — штатная ситуация: возвращается Secrets{Found: false}
// без ошибки, утилита продолжает работать. Права файла проверяются
// строго до чтения содержимого: файл, доступный группе или остальным
// (Perm()&0o077 != 0), не читается вовсе — содержимое не попадает в память
// процесса, возвращается только ErrInsecureSecrets с путём и подсказкой
// chmod (T-02-08, T-02-12). Ключ проекта в файле ищется точным совпадением
// host + "/" + projectPath; если записи для проекта нет, возвращается
// Secrets{Found: true} с пустой картой значений — файл есть, записи нет.
//
// Пакет ничего не выводит на экран: решение о показе (в том числе о том,
// что файл небезопасен или недостающие переменные) принимает вызывающий
// код (cmd/ci, internal/render) — так значение секрета не может утечь из
// низкоуровневого кода мимо единой политики вывода.
func LoadSecrets(host, projectPath string) (Secrets, error) {
	path, err := SecretsPath()
	if err != nil {
		return Secrets{}, err
	}

	// «Файла нет» — только fs.ErrNotExist. Любая другая ошибка stat
	// (EACCES, I/O, повисший automount) — это существующий, но недоступный
	// файл: молча счесть его отсутствующим значило бы предложить
	// пользователю создать файл, который уже есть (WR-03). *PathError
	// содержит только путь, не содержимое — оборачивать безопасно.
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Secrets{Path: path, Found: false}, nil
	}
	if err != nil {
		return Secrets{Path: path}, fmt.Errorf("env: файл секретов %s недоступен: %w", path, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		// Path заполнен даже в ошибке: содержимое не прочитано (риск
		// закрыт), но вызывающий код всё равно должен уметь сослаться на
		// путь файла и в предупреждении, и в отчёте о недостающих
		// переменных.
		return Secrets{Path: path}, fmt.Errorf(
			"env: файл секретов %s имеет слишком широкие права (%s), почините: chmod 600 %s: %w",
			path, info.Mode().Perm(), path, ErrInsecureSecrets,
		)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Secrets{Path: path}, fmt.Errorf("env: чтение файла секретов %s: %w", path, err)
	}

	var f secretsFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		// Текст ошибки yaml.v3 не пробрасывается: ошибки типов встраивают в
		// сообщение значение узла (например, `cannot unmarshal !!int `1234``),
		// а значения в этом файле — секреты. Наружу уходят только имя файла
		// и номера строк (WR-02).
		return Secrets{Path: path}, fmt.Errorf(
			"env: файл секретов %s не разобран как YAML%s; текст ошибки скрыт — он может содержать значения секретов; проверьте синтаксис и кавычки вокруг значений",
			path, yamlErrorLines(err),
		)
	}

	values := f.Projects[host+"/"+projectPath]

	return Secrets{Path: path, Values: values, Found: true}, nil
}

// EnsureSecretsFile создаёт файл секретов, уже заполненный именами
// недостающих переменных missing, если файла ещё нет на диске.
//
// Существующий файл не читается и не переписывается вовсе: возвращается его
// путь и ложный признак создания. В этом файле живут комментарии
// пользователя, значения других проектов и его собственный порядок ключей —
// переписывание превратило бы удобство в потерю данных, а недостающие ключи
// и так печатаются готовым к вставке фрагментом (missingReport в
// internal/render/env.go).
//
// Пустой host или пустой projectPath (случай подкоманды ci secrets на
// чистой машине, где проект ещё не известен) — вместо настоящего ключа
// проекта пишется закомментированный пример той же формы: файл остаётся
// валидным YAML, а пользователь видит формат.
//
// Содержимое собирается текстом построчно, а не сериализацией структуры —
// только текстовая сборка позволяет положить в файл шапку с пояснением и
// оставить её там навсегда. Запись атомарна тем же приёмом, что у токенов и
// настроек (internal/token/file.go, internal/config/config.go): временный
// файл через os.CreateTemp в том же каталоге, явный os.Chmod на 0o600
// (маска процесса иначе оставила бы файл читаемым всей машине), запись,
// закрытие, os.Rename поверх целевого пути; при любой ошибке до
// переименования временный файл удаляется.
//
// В файл не может попасть ни одно значение: единственный вход, несущий
// сведения о переменных, — срез Missing, у которого поля под значение нет по
// устройству типа.
//
// Пакет по-прежнему ничего не выводит на экран.
func EnsureSecretsFile(host, projectPath string, missing []Missing) (path string, created bool, err error) {
	path, err = SecretsPath()
	if err != nil {
		return "", false, err
	}

	if _, statErr := os.Stat(path); statErr == nil {
		return path, false, nil
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return path, false, fmt.Errorf("env: файл секретов %s недоступен: %w", path, statErr)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false, fmt.Errorf("env: не удалось создать каталог %s: %w", dir, err)
	}

	var b strings.Builder
	b.WriteString("# файл секретов ci-shell.\n")
	b.WriteString("# здесь хранятся значения переменных, которые GitLab никогда не отдаёт\n")
	b.WriteString("# через API (тип masked_and_hidden — пишутся один раз и не читаются никем).\n")
	b.WriteString("# права файла обязаны быть 0600 — иначе утилита отказывается его читать.\n")
	b.WriteString("# открыть файл в редакторе: ci secrets\n")
	b.WriteString("projects:\n")

	if host == "" || projectPath == "" {
		b.WriteString("  # <хост>/<путь проекта>:\n")
		b.WriteString("  #   ПЕРЕМЕННАЯ: \"<значение>\"\n")
	} else {
		fmt.Fprintf(&b, "  %s/%s:\n", host, projectPath)
		for _, m := range missing {
			fmt.Fprintf(&b, "    %s: \"\"\n", m.Key)
		}
	}

	tmp, err := os.CreateTemp(dir, ".secrets-*.yml.tmp")
	if err != nil {
		return "", false, fmt.Errorf("env: не удалось создать временный файл секретов: %w", err)
	}
	tmpPath := tmp.Name()
	// Право читаться не должно зависеть от umask процесса — Chmod делает
	// намерение явным в коде (тот же приём, что в token.Save/config.Save).
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", false, fmt.Errorf("env: не удалось выставить права 0600 на временный файл секретов: %w", err)
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", false, fmt.Errorf("env: не удалось записать временный файл секретов: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", false, fmt.Errorf("env: не удалось закрыть временный файл секретов: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return "", false, fmt.Errorf("env: не удалось сохранить файл секретов %s: %w", path, err)
	}

	return path, true, nil
}

// validSecretNameRe — форма имени переменной, которую допускает сам GitLab:
// буква или подчёркивание в начале, буквы, цифры и подчёркивания дальше.
var validSecretNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// mapEntry ищет запись с ключом name в отображении m (узел yaml.MappingNode)
// и возвращает узел ключа, узел значения и признак найденности. Единственный
// обход узлов отображения в проекте — SaveSecret зовёт его трижды (раздел
// проектов, ключ проекта, сама переменная), второго обхода не заводится.
func mapEntry(m *yaml.Node, name string) (keyNode, valueNode *yaml.Node, ok bool) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil, false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		if k.Value == name {
			return k, m.Content[i+1], true
		}
	}
	return nil, nil, false
}

// editableScalar сообщает, можно ли переписать значение val точечной
// правкой одной строки: простой скаляр (не якорь, не ссылка — Kind !=
// ScalarNode отсекает алиас), не блочный (literal/folded — они всегда
// начинаются со следующей строки, что уже ловит проверка Line), на той же
// строке, что и его ключ keyNode. parent — карта, СОДЕРЖАЩАЯ значение (для
// случая 1 — конкретный проект, projVal), а не сам узел значения. Валидный
// YAML допускает gitlab.com/proj: {FOO: "a", BAR: "b"} — обе пары на одной
// физической строке; без проверки стиля родителя editableScalar считал бы
// FOO редактируемым (он простой скаляр на строке своего ключа), а
// построчная правка стёрла бы всю строку целиком, включая BAR — соседнюю
// переменную того же проекта (CR-03 обзора v0.3.1).
func editableScalar(val, keyNode, parent *yaml.Node) bool {
	if parent.Style == yaml.FlowStyle {
		return false
	}
	if val.Kind != yaml.ScalarNode {
		return false
	}
	if val.Line != keyNode.Line {
		return false
	}
	if val.Style == yaml.LiteralStyle || val.Style == yaml.FoldedStyle {
		return false
	}
	if val.Anchor != "" {
		return false
	}
	return true
}

// insertLines вставляет newLines в срез lines перед позицией idx (0-индекс).
// Позиция idx = yaml.Node.Line совпадает с «сразу после строки с этим
// номером» без дополнительного сдвига, потому что Line 1-индексная, а срез
// строк — 0-индексный.
func insertLines(lines []string, idx int, newLines ...string) []string {
	out := make([]string, 0, len(lines)+len(newLines))
	out = append(out, lines[:idx]...)
	out = append(out, newLines...)
	out = append(out, lines[idx:]...)
	return out
}

// SaveSecret — точечная запись ОДНОГО значения секрета: находит место в уже
// существующем файле разбором в узел YAML по номерам строк и колонок и
// правит ровно одну строку исходного текста (в случае нового проекта — две,
// в случае пустого файла — три) — второй, разрушающей формулы записи
// (сериализация всего документа целиком) в проекте нет и не появляется.
//
// Порядок шагов обязателен — каждый закрывает свою поломку:
//  1. имя переменной проверяется формой переменной окружения GitLab —
//     иначе двоеточие, перевод строки или решётка в имени перестроили бы
//     документ и унесли бы чужие записи в чужие места;
//  2. значение проверяется отсутствием перевода строки и управляющих
//     символов — иначе перевод строки развалил бы однострочный скаляр, а
//     управляющая последовательность доехала бы до контейнера и до кадра
//     терминала;
//  3. путь файла берётся SecretsPath(), а сам файл — если его ещё нет —
//     создаётся EnsureSecretsFile (та же формула создания, что и везде в
//     проекте, второй не заводится);
//  4. права файла проверяются тем же правилом, что и при чтении (шире 0600
//     — отказ той же часовой ошибкой ErrInsecureSecrets), и ДО чтения
//     содержимого: дописать значение в файл, который читает вся машина,
//     значит своими руками положить туда секрет; права не чинятся молча —
//     файл открыл кто-то другой, и решение об этом принимает человек;
//  5. содержимое разбирается в узел YAML (не в структуру secretsFile) —
//     узлы несут номера строк и колонок, а найти место и есть цель разбора
//     здесь; ошибка разбора возвращается тем же способом, что и при чтении:
//     наружу уходят только имя файла и номера строк (WR-02);
//  6. место правки ищется единственным помощником mapEntry, зовущимся
//     трижды: раздел проектов, ключ проекта, сама переменная;
//  7. значение превращается в однострочный скаляр YAML СЕРИАЛИЗАТОРОМ
//     (yaml.Marshal одной строки, с отбрасыванием завершающего перевода
//     строки), а не собственным экранированием — первая же кавычка или
//     обратный слэш в ручном экранировании испортили бы файл или чужие
//     соседние записи; если после сериализации в скаляре всё-таки оказался
//     перевод строки — отказ ErrInvalidSecretValue, второй рубеж после
//     шага 2;
//  8. правится или добавляется РОВНО одна строка исходного текста (в случае
//     нового проекта — две, в случае пустого файла — три); остальные строки
//     не пересобираются;
//  9. запись атомарна: временный файл в том же каталоге, явные права 0600,
//     запись, закрытие, переименование поверх; при любой ошибке до
//     переименования временный файл удаляется — тот же приём, что у
//     EnsureSecretsFile, у токенов и у настроек, четвёртого не появляется.
//
// Ни один текст ошибки не содержит значения — только путь файла, имя
// переменной и номера строк; это правило уже действует для ошибок разбора
// при чтении (LoadSecrets), и запись живёт по нему же.
//
//nolint:gocyclo // ветвление — четыре места правки файла, а не случайная сложность
func SaveSecret(host, projectPath, key, value string) (path string, err error) {
	// Шаг 1: имя переменной.
	if !validSecretNameRe.MatchString(key) {
		return "", fmt.Errorf("env: имя переменной %q не годится в ключ YAML: %w", key, ErrInvalidSecretName)
	}

	// Шаг 2: значение — без перевода строки и без управляющих символов.
	if strings.ContainsRune(value, '\n') {
		return "", fmt.Errorf("env: значение переменной %s содержит перевод строки: %w", key, ErrInvalidSecretValue)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("env: значение переменной %s содержит управляющий символ: %w", key, ErrInvalidSecretValue)
		}
	}

	// Шаг 3: путь и (при необходимости) создание файла — второй формулы
	// создания в проекте не появляется.
	path, err = SecretsPath()
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(path); statErr != nil {
		if !errors.Is(statErr, fs.ErrNotExist) {
			return path, fmt.Errorf("env: файл секретов %s недоступен: %w", path, statErr)
		}
		if _, _, ensureErr := EnsureSecretsFile(host, projectPath, []Missing{{Key: key}}); ensureErr != nil {
			return path, ensureErr
		}
	}

	// Шаг 4: права — тем же правилом, что и при чтении, до чтения
	// содержимого.
	info, err := os.Stat(path)
	if err != nil {
		return path, fmt.Errorf("env: файл секретов %s недоступен: %w", path, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return path, fmt.Errorf(
			"env: файл секретов %s имеет слишком широкие права (%s), почините: chmod 600 %s: %w",
			path, info.Mode().Perm(), path, ErrInsecureSecrets,
		)
	}

	// Шаг 5: разбор в узел YAML.
	data, err := os.ReadFile(path)
	if err != nil {
		return path, fmt.Errorf("env: чтение файла секретов %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return path, fmt.Errorf(
			"env: файл секретов %s не разобран как YAML%s; текст ошибки скрыт — он может содержать значения секретов; проверьте синтаксис и кавычки вокруг значений",
			path, yamlErrorLines(err),
		)
	}
	var root *yaml.Node
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		root = doc.Content[0]
	}

	// Шаг 7: значение — скаляром сериализатора, до поиска места: если
	// значение вообще не сериализуется в однострочный скаляр, дальше идти
	// незачем.
	scalarBytes, err := yaml.Marshal(value)
	if err != nil {
		return path, fmt.Errorf("env: значение переменной %s не сериализовано в скаляр YAML: %w", key, ErrInvalidSecretValue)
	}
	scalar := strings.TrimSuffix(string(scalarBytes), "\n")
	if strings.Contains(scalar, "\n") {
		return path, fmt.Errorf("env: значение переменной %s не уместилось в однострочный скаляр YAML: %w", key, ErrInvalidSecretValue)
	}

	// Шаг 6 и 8: место правки и сама правка исходного текста построчно.
	lines := strings.Split(string(data), "\n")
	projectKey := host + "/" + projectPath

	projectsKeyNode, projectsVal, hasProjects := mapEntry(root, "projects")
	switch {
	case !hasProjects:
		// Случай 4: раздела проектов нет вовсе (файл пуст или человек его
		// выпотрошил) — три строки дописываются в конец файла, ни одна
		// существующая строка при этом не меняется.
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines,
			"projects:",
			"  "+projectKey+":",
			"    "+key+": "+scalar,
			"",
		)

	default:
		projKeyNode, projVal, hasProject := mapEntry(projectsVal, projectKey)
		switch {
		case !hasProject:
			// Случай 3: раздел проектов есть, проекта нет — сразу после
			// строки раздела вставляются две строки: ключ проекта и
			// переменная под ним, с отступами на два и на четыре больше
			// колонки раздела.
			if projectsVal.Style == yaml.FlowStyle {
				return path, fmt.Errorf(
					"env: раздел projects в файле %s записан в форме, которую точечная правка не переписывает (flow-запись {...} на строке %d): %w",
					path, projectsKeyNode.Line, ErrSecretNotEditable,
				)
			}
			indent := projectsKeyNode.Column - 1
			lines = insertLines(lines, projectsKeyNode.Line,
				strings.Repeat(" ", indent+2)+projectKey+":",
				strings.Repeat(" ", indent+4)+key+": "+scalar,
			)

		default:
			varKeyNode, varValNode, hasVar := mapEntry(projVal, key)
			switch {
			case !hasVar:
				// Случай 2: проект найден, переменной нет — новая строка
				// вставляется сразу после строки ключа проекта, с отступом
				// на два больше его колонки: конец блока нельзя определить,
				// не гадая, кому принадлежат идущие следом комментарии, а
				// вставка первой строкой не может присвоить себе чужой
				// комментарий.
				if projVal.Style == yaml.FlowStyle {
					return path, fmt.Errorf(
						"env: проект %s в файле %s записан в форме, которую точечная правка не переписывает (flow-запись {...} на строке %d): %w",
						projectKey, path, projKeyNode.Line, ErrSecretNotEditable,
					)
				}
				indent := projKeyNode.Column - 1
				lines = insertLines(lines, projKeyNode.Line,
					strings.Repeat(" ", indent+2)+key+": "+scalar,
				)

			default:
				// Случай 1: переменная найдена — правится её собственная
				// строка целиком; отступ берётся из КОЛОНКИ узла ключа, а
				// не выдумывается.
				if !editableScalar(varValNode, varKeyNode, projVal) {
					return path, fmt.Errorf(
						"env: значение переменной %s в файле %s записано в форме, которую точечная правка не переписывает (строка %d): %w",
						key, path, varKeyNode.Line, ErrSecretNotEditable,
					)
				}
				indent := varKeyNode.Column - 1
				lines[varKeyNode.Line-1] = strings.Repeat(" ", indent) + key + ": " + scalar
			}
		}
	}

	newContent := strings.Join(lines, "\n")

	// Шаг 9: атомарная запись — временный файл в том же каталоге, явные
	// права 0600 (маска процесса иначе оставила бы файл читаемым всей
	// машине), запись, закрытие, переименование поверх; при любой ошибке до
	// переименования временный файл удаляется.
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".secrets-*.yml.tmp")
	if err != nil {
		return path, fmt.Errorf("env: не удалось создать временный файл секретов: %w", err)
	}
	tmpPath := tmp.Name()
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return path, fmt.Errorf("env: не удалось выставить права 0600 на временный файл секретов: %w", err)
	}
	if _, err := tmp.WriteString(newContent); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return path, fmt.Errorf("env: не удалось записать временный файл секретов: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return path, fmt.Errorf("env: не удалось закрыть временный файл секретов: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return path, fmt.Errorf("env: не удалось сохранить файл секретов %s: %w", path, err)
	}

	return path, nil
}

// yamlLineRe находит в тексте ошибки yaml.v3 упоминания «line N» — из всей
// ошибки пользователю можно показывать только номера строк: остальной текст
// может содержать значения узлов.
var yamlLineRe = regexp.MustCompile(`line (\d+)`)

// yamlErrorLines извлекает из ошибки yaml.v3 номера строк и форматирует их
// суффиксом вида " (строки: 3, 7)". Пустая строка, если номеров в ошибке
// нет. Сам текст ошибки никогда не возвращается (WR-02).
func yamlErrorLines(err error) string {
	matches := yamlLineRe.FindAllStringSubmatch(err.Error(), -1)
	if len(matches) == 0 {
		return ""
	}
	lines := make([]string, 0, len(matches))
	for _, m := range matches {
		lines = append(lines, m[1])
	}
	return " (строки: " + strings.Join(lines, ", ") + ")"
}
