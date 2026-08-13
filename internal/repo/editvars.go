// Список правленных за сессию переменных (VAR-02) кладётся В ТОТ ЖЕ каталог,
// что и патч работы (patchesDir, единственная формула каталога в пакете) —
// второго места хранения результатов сессии не заводится. Отдельный файл, а
// не довесок к patch.go, потому что у него своя, узкая обязанность (список
// имён, ни одного значения) и она стоит отдельно от снятия и наложения
// диффа.
package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// editVarsFileName — единственное имя файла в проекте, куда пишется список
// правленных за сессию переменных.
const editVarsFileName = ".edit_vars"

// WriteEditVars записывает рядом с патчем работы (тот же каталог,
// patchesDir(host, projectPath)) список ИМЁН переменных, изменённых за
// сессию. Сигнатура структурно не принимает значение переменной — параметром
// идут только имена (VAR-03: файл называет, ЧТО менялось, а не чему равно, и
// это гарантировано формой функции, а не соглашением о том, что в неё
// передают). Пустой names ничего не пишет и возвращает пустой path без
// ошибки — тем же приёмом, что и пустой патч не создаёт файла патча
// (ErrNoChanges в Capture).
func WriteEditVars(host, projectPath string, names []string) (path string, err error) {
	if len(names) == 0 {
		return "", nil
	}

	dir, err := patchesDir(host, projectPath)
	if err != nil {
		return "", err
	}

	// Отдельный, отсортированный срез — файл стабилен между вызовами,
	// независимо от порядка, в котором подтверждались правки.
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)

	var b strings.Builder
	for _, name := range sorted {
		b.WriteString(name)
		b.WriteString("\n")
	}

	target := filepath.Join(dir, editVarsFileName)

	// Атомарная запись тем же приёмом, что и Capture/SaveSecret: временный
	// файл в том же каталоге, явные права 0o600 ДО записи содержимого,
	// запись, закрытие, os.Rename поверх целевого пути; при любой ошибке —
	// удаление временного файла и обёрнутая ошибка с префиксом "repo: ".
	tmp, err := os.CreateTemp(dir, ".edit_vars-*.tmp")
	if err != nil {
		return "", fmt.Errorf("repo: не удалось создать временный файл списка правок: %w", err)
	}
	tmpPath := tmp.Name()
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("repo: не удалось выставить права 0600 на временный файл списка правок: %w", err)
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("repo: не удалось записать временный файл списка правок: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("repo: не удалось закрыть временный файл списка правок: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("repo: не удалось сохранить список правок %s: %w", target, err)
	}

	return target, nil
}
