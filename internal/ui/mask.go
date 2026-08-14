// mask.go — единственный маскировщик известных значений переменных в
// проекте (Фаза 10, BROW-04): два потребителя — ограниченный буфер вывода
// локального прогона (internal/ui/session.go) и панель лога настоящего
// прогона, полученного через обход (internal/ui/joblog.go, CR-04 обзора
// v1.0.0). Второго правила замены нет: обе стороны идут через
// buildSecretMask и secretMask.Replace, а замена берётся у той же функции,
// что рисует панель окружения (render.DisplayValue) — второй ветки
// «показывать или нет» в проекте не появляется.
package ui

import (
	"context"
	"sort"
	"strings"

	"github.com/f3rym/ci-shell/internal/env"
	"github.com/f3rym/ci-shell/internal/provider"
	"github.com/f3rym/ci-shell/internal/render"
)

// minMaskLength — порог длины маскируемого значения: короче него значения в
// маску не попадают — замена односимвольного значения сделала бы весь
// вывод нечитаемым и ничего бы не защитила.
const minMaskLength = 4

// secretMask — набор пар «известное значение -> чем его заменить»,
// построенный один раз из окружения джобы.
type secretMask struct {
	replacer *strings.Replacer
}

// buildSecretMask собирает маску из окружения e: для каждой переменной, чьё
// отображение по render.DisplayValue НЕ совпадает с её значением (то есть
// решение её прячет), в маску идёт пара из значения и этого отображения.
// Пустые и короче minMaskLength значения не попадают в маску; пары
// упорядочены от длинного значения к короткому, чтобы значение, целиком
// входящее в другое, не рвало замену на части.
func buildSecretMask(e env.Environment) secretMask {
	type pair struct{ value, display string }
	var pairs []pair
	for _, v := range e.Vars {
		if len(v.Value) < minMaskLength {
			continue
		}
		display := render.DisplayValue(v)
		if display == v.Value {
			continue
		}
		pairs = append(pairs, pair{value: v.Value, display: display})
	}
	sort.Slice(pairs, func(i, j int) bool { return len(pairs[i].value) > len(pairs[j].value) })

	if len(pairs) == 0 {
		return secretMask{}
	}
	flat := make([]string, 0, len(pairs)*2)
	for _, p := range pairs {
		flat = append(flat, p.value, p.display)
	}
	return secretMask{replacer: strings.NewReplacer(flat...)}
}

// Replace заменяет известные значения в s их отображением. Пустая маска
// (нет известных секретов длиннее порога) возвращает s как есть.
func (m secretMask) Replace(s string) string {
	if m.replacer == nil {
		return s
	}
	return m.replacer.Replace(s)
}

// buildBrowseMask собирает маску для лога джобы, полученного через обход
// (internal/browse.Client.Log, internal/ui/joblog.go) — до входа в сессию
// воспроизведения, где для той же цели уже строится buildSecretMask(e) из
// полностью собранного окружения (internal/ui/session.go). На этом этапе
// полного окружения ещё нет (нет ни разобранного конфига пайплайна, ни
// сессионных правок — сама сессия ещё не начата), поэтому маска строится из
// тех же ДВУХ источников значений, что несут реальные секреты — переменных
// API (Provider.Variables) и локального файла секретов (env.LoadSecrets) —
// без слоёв, которых здесь нет и не может быть; их отсутствие не отнимает
// ни одного уже известного значения, оно лишь не добавляет то, что взялось
// бы из конфига пайплайна или правок текущей сессии.
//
// Ошибка получения переменных или файла секретов не отказывает показом
// лога — она лишь сужает маску до того, что удалось узнать: то же
// поведение деградации, каким Session.PrepareCmd относится к точно таким
// же двум источникам (notices вместо отказа). Второй реализации
// маскирования здесь не заводится — buildBrowseMask лишь собирает
// env.Environment из другого набора слоёв и передаёт его в тот же
// buildSecretMask/secretMask.Replace, что и сессия.
func buildBrowseMask(ctx context.Context, p provider.Provider, host string, job provider.Job) secretMask {
	var apiVars []provider.Variable
	if p != nil {
		if vs, err := p.Variables(ctx, job); err == nil {
			apiVars = vs.Variables
		}
	}
	secrets, _ := env.LoadSecrets(host, job.ProjectPath)
	e := env.Assemble(env.Input{
		Job:         job,
		Host:        host,
		APIVars:     apiVars,
		Secrets:     secrets.Values,
		SecretsPath: secrets.Path,
	})
	return buildSecretMask(e)
}
