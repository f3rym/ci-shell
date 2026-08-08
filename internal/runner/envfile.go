package runner

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/f3rym/ci-shell/internal/cache"
)

// writeEnvFile пишет vars во временный файл формата --env-file (NAME=value,
// одна пара на строку) с правами 0600 и возвращает его путь. Функция ничего
// не печатает: решение о выводе принимают cmd/ci и internal/render, как в
// пакете секретов Фазы 2.
//
// Ключи обходятся в отсортированном порядке — стабильность файла между
// запусками. Ключ, не подходящий под форму имени переменной окружения
// (validKey), пропускается. Значение с переводом строки или возвратом
// каретки тоже пропускается: построчный формат файла таков, что строка без
// знака равенства заставляет docker взять значение переменной из окружения
// машины пользователя — кривое значение способно протащить в контейнер
// посторонний секрет хозяйской машины. Пропущенные ключи возвращаются
// вызывающему в skipped — только ключи, без значений.
//
// При любой ошибке файл удаляется и возвращается ошибка, чтобы недописанный
// файл с секретами не остался на диске.
func writeEnvFile(vars map[string]string) (path string, skipped []string, err error) {
	// Постоянный кэш пользователя вместо системного временного каталога —
	// та же причина, что и у repo.Prepare: snap-сборка Docker не видит
	// хостовый /tmp, и docker create --env-file оттуда отказывает.
	dir, err := cache.Dir("env")
	if err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp(dir, "env-*")
	if err != nil {
		return "", nil, fmt.Errorf("runner: не удалось создать временный файл окружения: %w", err)
	}
	path = f.Name()

	// Право читаться зависит от umask процесса, создавшего файл — Chmod
	// делает намерение явным в коде, а не зависимым от окружения запуска.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(path)
		return "", nil, fmt.Errorf("runner: не удалось выставить права 0600 на файл окружения: %w", err)
	}

	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		v := vars[k]
		if !validKey(k) {
			skipped = append(skipped, k)
			continue
		}
		if strings.ContainsAny(v, "\n\r") {
			skipped = append(skipped, k)
			continue
		}
		if _, err := fmt.Fprintf(f, "%s=%s\n", k, v); err != nil {
			f.Close()
			os.Remove(path)
			return "", nil, fmt.Errorf("runner: не удалось записать файл окружения: %w", err)
		}
	}

	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", nil, fmt.Errorf("runner: не удалось закрыть файл окружения: %w", err)
	}

	return path, skipped, nil
}

// validKey сообщает, годится ли k как имя переменной окружения: первый
// символ — буква или подчёркивание, остальные — буквы, цифры или
// подчёркивания.
func validKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
