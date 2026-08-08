package env

import (
	"os"
	"strings"
)

// unrestrictedScope истинно для пустой строки и для одиночной звёздочки —
// обе формы означают «переменная видна любому окружению». Отдельная
// функция, потому что это условие нужно в трёх местах (scopeMatches,
// двухпроходное наложение слоя в applyAPILayer), и разъехавшиеся копии
// дали бы разное поведение для одного и того же значения.
func unrestrictedScope(scope string) bool {
	return scope == "" || scope == "*"
}

// scopeMatches решает, видна ли переменная с областью окружения scope
// джобе, у которой имя окружения (уже раскрытое из подстановок) — environment.
//
//   - unrestrictedScope(scope) — истина независимо от окружения джобы
//     (сегодняшнее поведение сохраняется);
//   - пустое environment при ограниченной области — ложь: у джобы
//     окружения нет, значит в настоящем прогоне такой переменной у неё
//     тоже не было;
//   - область без звёздочки — точное сравнение строк;
//   - область со звёздочками — сверка по частям: строка области режется по
//     звёздочке, первая часть обязана быть началом имени окружения,
//     последняя — концом, промежуточные ищутся в имени по порядку слева
//     направо, каждая после предыдущей. Звёздочка при этом покрывает и
//     косую черту, поэтому "preprod/*" совпадает и с "preprod/api", и с
//     "preprod/api/v2". GitLab сверяет область именно так — сопоставление
//     по образцу из stdlib для путей (path.Match) считает косую черту
//     границей и на "preprod/api/v2" дало бы ложь, то есть ровно ту тихую
//     потерю переменной, ради которой всё это и делается.
func scopeMatches(scope, environment string) bool {
	if unrestrictedScope(scope) {
		return true
	}
	if environment == "" {
		return false
	}
	if !strings.Contains(scope, "*") {
		return scope == environment
	}

	parts := strings.Split(scope, "*")
	first, last := parts[0], parts[len(parts)-1]
	if !strings.HasPrefix(environment, first) {
		return false
	}
	if !strings.HasSuffix(environment, last) {
		return false
	}

	// Промежуточные части ищутся по порядку слева направо, каждая после
	// конца предыдущей — это гарантирует, что части не пересекаются и не
	// переставлены местами.
	pos := len(first)
	middle := parts[1 : len(parts)-1]
	for _, part := range middle {
		if part == "" {
			continue
		}
		idx := strings.Index(environment[pos:], part)
		if idx < 0 {
			return false
		}
		pos += idx + len(part)
	}
	// Последняя часть должна начинаться не раньше, чем закончились уже
	// найденные промежуточные части.
	return pos <= len(environment)-len(last)
}

// expandEnvName раскрывает подстановки в имени окружения name из уже
// собранных переменных vars: environment: preprod/$CI_COMMIT_REF_SLUG —
// типовая запись, и без раскрытия сверка области сравнивала бы шаблон со
// строкой, содержащей знак доллара, и снова теряла переменные молча.
//
// Вторым результатом возвращаются имена нераскрытых переменных — вызывающий
// превращает их в оговорку. Значения переменных функция только читает: она
// ничего не печатает и ничего не возвращает, кроме имени окружения и
// списка ключей.
func expandEnvName(name string, vars map[string]Variable) (string, []string) {
	var missing []string
	expanded := os.Expand(name, func(key string) string {
		if v, ok := vars[key]; ok {
			return v.Value
		}
		missing = append(missing, key)
		// Подстановка не найдена — восстанавливаем исходную запись со
		// знаком доллара, чтобы не потерять её молча и не выдать пустую
		// строку за настоящее значение.
		return "$" + key
	})
	return expanded, missing
}
