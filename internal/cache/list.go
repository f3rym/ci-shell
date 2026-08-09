// list.go — кэш списков GitLab на диске.
//
// Сюда кладутся только списки, которые человек и так видит в веб-интерфейсе,
// — группы, проекты, пайплайны, джобы. Ни значений переменных, ни лога
// джобы, ни значения токена здесь не бывает никогда, и это не «пока», а
// правило: значение токена и вывод джобы переживают процесс только в
// памяти. Пакет по-прежнему не импортирует ни одного пакета проекта — он
// нижний уровень, как и был.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// Version — версия формата записи кэша списков. Несовпадение версии
	// при чтении считается промахом, а не ошибкой разбора.
	Version = 1
	// TTLTree — срок годности для того, что меняется редко: групп и
	// проектов. Дерево организации не перестраивается каждую минуту.
	TTLTree = 24 * time.Hour
	// TTLRuns — срок годности для того, что меняется на глазах:
	// пайплайнов и джоб. Короткий срок ловит свежий статус без того,
	// чтобы список считался устаревшим на глазах у человека.
	TTLRuns = 2 * time.Minute
	// maxEntryBytes — предел размера файла записи. Запись шире предела
	// считается промахом и удаляется — испорченный или чужой файл кэша не
	// должен читаться целиком в память.
	maxEntryBytes = 4 * 1024 * 1024 // 4 МиБ
)

// Key — то, что однозначно определяет один кэшированный список: хост,
// вид списка (группы, проекты, пайплайны, джобы) и область (путь
// родителя, путь проекта, номер пайплайна — что уместно для вида).
type Key struct {
	Host  string
	Kind  string
	Scope string
}

// filename — имя файла записи: шестнадцатеричный хэш от трёх полей ключа,
// склеенных нулевым байтом. Имя файла не собирается из чужой строки (хост и
// путь группы приходят снаружи, и «сложить их в путь файла» — известный
// способ уехать из каталога кэша), и оно не выдаёт структуру организации
// соседу по машине (T-10-06).
func (k Key) filename() string {
	sum := sha256.Sum256([]byte(k.Host + "\x00" + k.Kind + "\x00" + k.Scope))
	return hex.EncodeToString(sum[:]) + ".json"
}

// Entry — то, что отдаётся и принимается снаружи пакета.
type Entry[T any] struct {
	Items     []T
	Complete  bool
	FetchedAt time.Time
}

// record — то, что на самом деле лежит в файле: Entry плюс версия формата,
// хост, вид и область в человекочитаемом виде, чтобы содержимое файла можно
// было прочитать глазами при разборе поломки.
type record[T any] struct {
	FormatVersion int       `json:"format_version"`
	Host          string    `json:"host"`
	Kind          string    `json:"kind"`
	Scope         string    `json:"scope"`
	Items         []T       `json:"items"`
	Complete      bool      `json:"complete"`
	FetchedAt     time.Time `json:"fetched_at"`
}

// Load читает запись ключа k из кэша. Второе значение означает «попадание и
// запись свежая». Промахом (без ошибки) считаются: файла нет; размер файла
// больше maxEntryBytes; права шире 0600; версия формата не совпала; разбор
// не удался; запись старше maxAge. Кэш никогда не бывает причиной отказа —
// испорченный или чужой файл просто удаляется и считается промахом,
// потому что уронить утилиту из-за своего же кэша было бы обиднее всего.
func Load[T any](k Key, maxAge time.Duration) (Entry[T], bool) {
	dir, err := Dir("lists")
	if err != nil {
		return Entry[T]{}, false
	}
	path := filepath.Join(dir, k.filename())

	info, err := os.Stat(path)
	if err != nil {
		return Entry[T]{}, false
	}
	if info.Size() > maxEntryBytes {
		os.Remove(path)
		return Entry[T]{}, false
	}
	if info.Mode().Perm()&0o077 != 0 {
		os.Remove(path)
		return Entry[T]{}, false
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Entry[T]{}, false
	}

	var rec record[T]
	if err := json.Unmarshal(data, &rec); err != nil {
		os.Remove(path)
		return Entry[T]{}, false
	}
	if rec.FormatVersion != Version {
		os.Remove(path)
		return Entry[T]{}, false
	}
	if time.Since(rec.FetchedAt) > maxAge {
		return Entry[T]{}, false
	}

	return Entry[T]{Items: rec.Items, Complete: rec.Complete, FetchedAt: rec.FetchedAt}, true
}

// Save записывает запись e под ключом k. Каталог берётся у Dir с
// подкаталогом списков (создаётся правами 0700 самой Dir). Запись атомарна
// тем же приёмом, что уже применён к настройкам (internal/config.Save) и к
// токену: временный файл в том же каталоге, явно выставленные права 0600
// (не полагаться на маску процесса), сериализация, переименование поверх
// целевого пути; при любой ошибке до переименования временный файл
// удаляется.
func Save[T any](k Key, e Entry[T]) error {
	dir, err := Dir("lists")
	if err != nil {
		return err
	}

	rec := record[T]{
		FormatVersion: Version,
		Host:          k.Host,
		Kind:          k.Kind,
		Scope:         k.Scope,
		Items:         e.Items,
		Complete:      e.Complete,
		FetchedAt:     e.FetchedAt,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("cache: сериализация записи списка: %w", err)
	}

	path := filepath.Join(dir, k.filename())
	tmp, err := os.CreateTemp(dir, ".list-*.json.tmp")
	if err != nil {
		return fmt.Errorf("cache: не удалось создать временный файл списка: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("cache: не удалось выставить права 0600 на временный файл списка: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("cache: не удалось записать временный файл списка: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("cache: не удалось закрыть временный файл списка: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("cache: не удалось сохранить список %s: %w", path, err)
	}
	return nil
}

// Drop удаляет запись ключа k. Отсутствие файла ошибкой не считается. Это
// то, что делает ручное обновление по клавише обновления настоящим
// обновлением, а не чтением того же самого.
func Drop(k Key) error {
	dir, err := Dir("lists")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, k.filename())
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("cache: не удалось удалить список %s: %w", path, err)
	}
	return nil
}
