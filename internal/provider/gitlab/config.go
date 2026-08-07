package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/f3rym/ci-shell/internal/provider"
)

// referenceTag — тег YAML, которым GitLab помечает подстановки !reference.
// Раскрытие таких подстановок эквивалентно частичной реализации CI Lint,
// поэтому вместо этого возвращается честная ошибка (idea §7, NEXT-02).
const referenceTag = "!reference"

// reservedRootKeys — корневые ключи файла конфига пайплайна, которые не
// являются определениями джоб.
var reservedRootKeys = map[string]bool{
	"stages":        true,
	"variables":     true,
	"include":       true,
	"default":       true,
	"workflow":      true,
	"image":         true,
	"services":      true,
	"before_script": true,
	"after_script":  true,
	"cache":         true,
}

// MergedConfig возвращает конфигурацию пайплайна на указанном коммите.
//
// В Фазе 1 это сырой файл конфига пайплайна, полученный через Repository
// Files API и разобранный для простых джоб; смёрженный конфиг через CI
// Lint — это NEXT-02.
func (c *Client) MergedConfig(ctx context.Context, projectPath, sha string) (provider.Config, error) {
	path := fmt.Sprintf(
		"/projects/%s/repository/files/%s/raw?ref=%s",
		url.PathEscape(projectPath),
		url.PathEscape(".gitlab-ci.yml"),
		url.QueryEscape(sha),
	)
	body, err := c.do(ctx, http.MethodGet, path)
	if err != nil {
		if errors.Is(err, errStatusNotFound) {
			return provider.Config{}, fmt.Errorf("gitlab: конфиг пайплайна %s@%s не найден: %w", projectPath, sha, provider.ErrConfigNotFound)
		}
		return provider.Config{}, err
	}

	cfg, err := parseConfig(body)
	if err != nil {
		return provider.Config{}, err
	}
	return cfg, nil
}

// parseConfig разбирает сырые байты файла конфига пайплайна в provider.Config.
// Поддерживает только простые джобы (idea §3.3, этап 0): наивное чтение
// .gitlab-ci.yml без раскрытия include/extends/!reference.
func parseConfig(data []byte) (provider.Config, error) {
	var root map[string]yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return provider.Config{}, fmt.Errorf("gitlab: разбор конфига пайплайна: %w", err)
	}

	if rootHasReferenceTag(root) {
		return provider.Config{}, fmt.Errorf(
			"gitlab: конфиг использует !reference — нужен смёрженный конфиг через CI Lint (NEXT-02, см. REQUIREMENTS.md): %w",
			provider.ErrUnresolvedRefs,
		)
	}
	if _, ok := root["include"]; ok {
		return provider.Config{}, fmt.Errorf(
			"gitlab: конфиг использует include — нужен смёрженный конфиг через CI Lint (NEXT-02, см. REQUIREMENTS.md): %w",
			provider.ErrUnresolvedRefs,
		)
	}

	var defaultBlock map[string]yaml.Node
	if node, ok := root["default"]; ok {
		if err := node.Decode(&defaultBlock); err != nil {
			return provider.Config{}, fmt.Errorf("gitlab: разбор default: %w", err)
		}
	}

	rootVariables, err := decodeVariables(root["variables"])
	if err != nil {
		return provider.Config{}, fmt.Errorf("gitlab: разбор variables: %w", err)
	}

	jobs := make(map[string]provider.JobConfig)
	unresolved := make(map[string]error)
	for name, node := range root {
		if reservedRootKeys[name] || strings.HasPrefix(name, ".") {
			continue
		}

		var jobBlock map[string]yaml.Node
		if err := node.Decode(&jobBlock); err != nil {
			// Не отображение — не определение джобы (в валидном конфиге
			// такого быть не должно, но пропускаем, а не падаем).
			continue
		}
		if _, hasExtends := jobBlock["extends"]; hasExtends {
			// Отказ точечный: без смёрженного конфига не разобрать только
			// эту джобу — остальные джобы файла разбираются дальше.
			unresolved[name] = fmt.Errorf(
				"gitlab: джоба %q использует extends — нужен смёрженный конфиг через CI Lint (NEXT-02, см. REQUIREMENTS.md): %w",
				name, provider.ErrUnresolvedRefs,
			)
			continue
		}

		jc, err := buildJobConfig(name, jobBlock, defaultBlock, root, rootVariables)
		if err != nil {
			return provider.Config{}, fmt.Errorf("gitlab: разбор джобы %q: %w", name, err)
		}
		jobs[name] = jc
	}

	return provider.Config{Jobs: jobs, Unresolved: unresolved, Raw: data}, nil
}

// buildJobConfig собирает JobConfig для одной джобы, наследуя image,
// before_script и variables по убыванию приоритета: сама джоба → default →
// корень документа.
func buildJobConfig(name string, job, defaultBlock, root map[string]yaml.Node, rootVariables map[string]string) (provider.JobConfig, error) {
	stage := "test" // значение GitLab по умолчанию, если у джобы нет stage
	if node, ok := job["stage"]; ok {
		if err := node.Decode(&stage); err != nil {
			return provider.JobConfig{}, fmt.Errorf("stage: %w", err)
		}
	}

	image, err := resolveImage(job, defaultBlock, root)
	if err != nil {
		return provider.JobConfig{}, fmt.Errorf("image: %w", err)
	}

	beforeScript, err := resolveScriptList("before_script", job, defaultBlock, root)
	if err != nil {
		return provider.JobConfig{}, fmt.Errorf("before_script: %w", err)
	}

	script, err := decodeScriptList(job["script"])
	if err != nil {
		return provider.JobConfig{}, fmt.Errorf("script: %w", err)
	}

	jobVariables, err := decodeVariables(job["variables"])
	if err != nil {
		return provider.JobConfig{}, fmt.Errorf("variables: %w", err)
	}

	return provider.JobConfig{
		Name:         name,
		Stage:        stage,
		Image:        image,
		BeforeScript: beforeScript,
		Script:       script,
		Variables:    mergeVariables(rootVariables, jobVariables),
	}, nil
}

// resolveImage ищет ключ image в джобе, затем в default, затем в корне —
// первое найденное значение побеждает.
func resolveImage(job, defaultBlock, root map[string]yaml.Node) (string, error) {
	if node, ok := job["image"]; ok {
		return decodeImage(node)
	}
	if node, ok := defaultBlock["image"]; ok {
		return decodeImage(node)
	}
	if node, ok := root["image"]; ok {
		return decodeImage(node)
	}
	return "", nil
}

// resolveScriptList ищет ключ key (before_script/after_script) в джобе,
// затем в default, затем в корне — первое найденное значение побеждает.
func resolveScriptList(key string, job, defaultBlock, root map[string]yaml.Node) ([]string, error) {
	if node, ok := job[key]; ok {
		return decodeScriptList(node)
	}
	if node, ok := defaultBlock[key]; ok {
		return decodeScriptList(node)
	}
	if node, ok := root[key]; ok {
		return decodeScriptList(node)
	}
	return nil, nil
}

// decodeImage принимает image строкой или отображением с ключом name.
func decodeImage(n yaml.Node) (string, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		var s string
		if err := n.Decode(&s); err != nil {
			return "", err
		}
		return s, nil
	case yaml.MappingNode:
		var m struct {
			Name string `yaml:"name"`
		}
		if err := n.Decode(&m); err != nil {
			return "", err
		}
		return m.Name, nil
	default:
		return "", fmt.Errorf("неожиданный формат image")
	}
}

// decodeScriptList принимает before_script/script строкой (оборачивается в
// срез из одного элемента) или списком строк. GitLab also allows nested
// arrays inside the list (script: [[a, b], c]) and flattens them one
// level — mirror that instead of failing on valid syntax (WR-03).
func decodeScriptList(n yaml.Node) ([]string, error) {
	switch n.Kind {
	case 0: // ключ отсутствует
		return nil, nil
	case yaml.ScalarNode:
		var s string
		if err := n.Decode(&s); err != nil {
			return nil, err
		}
		return []string{s}, nil
	case yaml.SequenceNode:
		var list []string
		for _, item := range n.Content {
			switch item.Kind {
			case yaml.SequenceNode:
				var nested []string
				if err := item.Decode(&nested); err != nil {
					return nil, err
				}
				list = append(list, nested...)
			default:
				var s string
				if err := item.Decode(&s); err != nil {
					return nil, err
				}
				list = append(list, s)
			}
		}
		return list, nil
	default:
		return nil, fmt.Errorf("неожиданный формат списка шагов")
	}
}

// decodeVariables декодирует блок variables. Отсутствующий ключ — это nil,
// а не ошибка. Values are decoded leniently: a plain scalar is taken as
// is, and GitLab's expanded form (a mapping with value/description/...)
// contributes its "value" key — both are valid GitLab syntax (WR-03).
func decodeVariables(n yaml.Node) (map[string]string, error) {
	if n.Kind == 0 {
		return nil, nil
	}
	var raw map[string]yaml.Node
	if err := n.Decode(&raw); err != nil {
		return nil, err
	}
	vars := make(map[string]string, len(raw))
	for k, v := range raw {
		if v.Kind == yaml.MappingNode {
			var m struct {
				Value string `yaml:"value"`
			}
			if err := v.Decode(&m); err != nil {
				return nil, err
			}
			vars[k] = m.Value
			continue
		}
		var s string
		if err := v.Decode(&s); err != nil {
			return nil, err
		}
		vars[k] = s
	}
	return vars, nil
}

// mergeVariables сливает корневые и джобные переменные: значения джобы
// перекрывают корневые.
func mergeVariables(rootVariables, jobVariables map[string]string) map[string]string {
	if len(rootVariables) == 0 && len(jobVariables) == 0 {
		return nil
	}
	merged := make(map[string]string, len(rootVariables)+len(jobVariables))
	for k, v := range rootVariables {
		merged[k] = v
	}
	for k, v := range jobVariables {
		merged[k] = v
	}
	return merged
}

// rootHasReferenceTag ищет тег !reference в любом узле документа —
// подстановки !reference могут встречаться в любой части конфига, не
// только в списках шагов.
func rootHasReferenceTag(root map[string]yaml.Node) bool {
	for _, n := range root {
		n := n
		if nodeHasReferenceTag(&n) {
			return true
		}
	}
	return false
}

func nodeHasReferenceTag(n *yaml.Node) bool {
	if n == nil {
		return false
	}
	if n.Tag == referenceTag {
		return true
	}
	for _, c := range n.Content {
		if nodeHasReferenceTag(c) {
			return true
		}
	}
	return false
}
