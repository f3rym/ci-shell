package runner

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/f3rym/ci-shell/internal/cache"
)

// ErrFileVars — не удалось материализовать файловые переменные.
var ErrFileVars = errors.New("не удалось материализовать файловые переменные")

// FileVars — результат материализации файловых переменных на диске хоста.
type FileVars struct {
	// Dir — каталог на хосте, куда легли файлы. Пусто, когда материализовать
	// было нечего.
	Dir string
	// Mount — каталог внутри контейнера, куда монтируется Dir.
	Mount string
	// Paths — ключ переменной → путь внутри контейнера.
	Paths map[string]string
	// Skipped — ключи, которые материализовать не удалось; только ключи, без
	// содержимого — тот же приём, что у env.Missing.
	Skipped []string
}

// MaterializeFileVars пишет contents (ключ → содержимое файловой переменной,
// см. env.Environment.FileContents) на диск хоста в отдельный каталог и
// возвращает путь внутри контейнера для каждого успешно материализованного
// ключа — mount станет точкой монтирования этого каталога соседом рабочего
// каталога джобы.
//
// Пустая карта contents — каталог не создаётся вовсе: пустой каталог и
// лишнее монтирование ничего не дают, Mount всё равно возвращается для
// симметрии.
//
// При любой ошибке уже созданный каталог удаляется целиком — недописанный
// каталог с секретами на диске не остаётся.
func MaterializeFileVars(contents map[string]string, mount string) (*FileVars, error) {
	if len(contents) == 0 {
		return &FileVars{Mount: mount, Paths: map[string]string{}}, nil
	}

	base, err := cache.Dir("filevars")
	if err != nil {
		return nil, fmt.Errorf("runner: %w: %w", ErrFileVars, err)
	}

	// Случайное имя каталога обязательно — каталог кэша общий между
	// запусками, и два одновременных запуска не должны драться за один
	// путь.
	dir, err := os.MkdirTemp(base, "job-")
	if err != nil {
		return nil, fmt.Errorf("runner: %w: не удалось создать каталог файловых переменных: %w", ErrFileVars, err)
	}
	// Права каталога подтверждаются явно, а не оставляются на усмотрение
	// маски процесса, создавшей его.
	if err := os.Chmod(dir, 0o700); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("runner: %w: не удалось выставить права 0700 на каталог %s: %w", ErrFileVars, dir, err)
	}

	// Ключи обходятся в отсортированном порядке — стабильность между
	// запусками, как в файле окружения (internal/runner/envfile.go).
	keys := make([]string, 0, len(contents))
	for k := range contents {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	paths := make(map[string]string, len(keys))
	var skipped []string
	for _, k := range keys {
		// Ключ проверяется существующим validKey (internal/runner/envfile.go)
		// — это и есть защита от выхода за пределы каталога: форма имени
		// переменной окружения не допускает ни косой черты, ни точки,
		// поэтому имя файла не может увести запись наружу. Проверка здесь не
		// про опрятность, а про то, что ключ приходит из API и становится
		// именем файла. Ключ, не прошедший проверку, попадает в Skipped и не
		// материализуется.
		if !validKey(k) {
			skipped = append(skipped, k)
			continue
		}

		hostPath := filepath.Join(dir, k)
		// Права при создании урезаются маской процесса — без явного Chmod
		// следом файл с секретом мог бы оказаться читаемым для всей машины.
		if err := os.WriteFile(hostPath, []byte(contents[k]), 0o600); err != nil {
			os.RemoveAll(dir)
			return nil, fmt.Errorf("runner: %w: не удалось записать файл %s: %w", ErrFileVars, hostPath, err)
		}
		if err := os.Chmod(hostPath, 0o600); err != nil {
			os.RemoveAll(dir)
			return nil, fmt.Errorf("runner: %w: не удалось выставить права 0600 на файл %s: %w", ErrFileVars, hostPath, err)
		}

		// path.Join, не filepath.Join: путь адресует файловую систему
		// контейнера, которая всегда POSIX — сборка Windows дала бы обратные
		// косые черты и неработающую переменную.
		paths[k] = path.Join(mount, k)
	}

	return &FileVars{Dir: dir, Mount: mount, Paths: paths, Skipped: skipped}, nil
}

// Remove удаляет Dir целиком, если он непуст; пустой Dir — нечего убирать,
// отсутствие ошибки. Контейнер работал под root и мог оставить в каталоге
// собственные файлы — текст ошибки называет фактический путь и готовую
// команду добивки, как в снятии чекаута (internal/repo/checkout.go).
func (f *FileVars) Remove() error {
	if f == nil || f.Dir == "" {
		return nil
	}
	if err := os.RemoveAll(f.Dir); err != nil {
		return fmt.Errorf("runner: не удалось удалить каталог файловых переменных %s: добейте вручную: rm -rf %s: %w", f.Dir, f.Dir, err)
	}
	return nil
}
