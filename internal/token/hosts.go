package token

import (
	"os"
	"sort"
)

// Hosts возвращает хосты, для которых у пользователя вообще может найтись
// токен, в порядке приоритета, без единого значения токена в возвращаемом
// значении (Фаза 10, BROW-01):
//
//   - если задана любая из envVars, первым идёт ожидаемый хост — та же
//     формула, что уже вычисляет warnEnvTokenHostMismatch (GITLAB_HOST,
//     а при её отсутствии — gitlab.com); второй формулы ожидаемого хоста в
//     проекте не появляется;
//   - дальше — ключи файла токенов, у которых запись непуста, нормализованные
//     normalizeHost и отсортированные: обход карты в Go идёт в случайном
//     порядке, а заставка берёт первый хост списка — без сортировки человек
//     получал бы разный хост от запуска к запуску.
//
// Повторы убираются, порядок первых вхождений сохраняется. Ошибка чтения
// файла токенов не возвращается наружу и не роняет ничего: список просто
// оказывается короче — пустой результат означает «токена нет вообще».
//
// Функция отвечает на вопрос «куда мы вообще можем пойти», а не «какой у нас
// секрет»: значения записей файла она не читает дальше проверки на
// пустоту (T-10-12).
func Hosts() []string {
	var hosts []string

	for _, name := range envVars {
		if os.Getenv(name) == "" {
			continue
		}
		expected := normalizeHost(os.Getenv("GITLAB_HOST"))
		if expected == "" {
			expected = "gitlab.com"
		}
		hosts = append(hosts, expected)
		break
	}

	if cfg, _, err := readConfig(); err == nil {
		var fileHosts []string
		for key, entry := range cfg.Hosts {
			if entry.Token == "" {
				continue
			}
			fileHosts = append(fileHosts, normalizeHost(key))
		}
		sort.Strings(fileHosts)
		hosts = append(hosts, fileHosts...)
	}

	return dedupeHosts(hosts)
}

// dedupeHosts убирает повторы, сохраняя порядок первых вхождений.
func dedupeHosts(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, h := range in {
		if seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}
