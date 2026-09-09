// Package spec читает собранную спецификацию — тот самый `bundle.yaml`, который уходит
// наружу как артефакт.
//
// Нужен он тестам: границы страницы объявлены контрактом, а служба подставляет их
// умолчаниями в коде. Пока кодогенератор не переносит `default` и `maximum` в
// сгенерированный тип, единственный способ не дать двум спискам разъехаться — сверять
// константы со спецификацией на каждом прогоне тестов.
package spec

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// PageWindow — окно страницы, объявленное параметром `limit` в спецификации.
type PageWindow struct {
	// Default — размер страницы, когда клиент не просил своего.
	Default int
	// Max — потолок, выше которого служба страницу не отдаёт.
	Max int
}

// bundle — та часть собранного документа, которая нужна тестам: объявленные контрактом
// ограничения и перечень операций. Всё остальное yaml пропускает — читать документ
// целиком незачем.
type bundle struct {
	Paths map[string]map[string]struct {
		OperationID string   `yaml:"operationId"`
		Tags        []string `yaml:"tags"`
	} `yaml:"paths"`
	Components struct {
		Schemas struct {
			NewRestaurant struct {
				Properties struct {
					Name struct {
						MaxLength int `yaml:"maxLength"`
					} `yaml:"name"`
					BaseURL struct {
						MaxLength int `yaml:"maxLength"`
					} `yaml:"base_url"`
				} `yaml:"properties"`
			} `yaml:"NewRestaurant"`
		} `yaml:"schemas"`
		Parameters struct {
			Limit struct {
				Schema struct {
					Default int `yaml:"default"`
					Maximum int `yaml:"maximum"`
				} `yaml:"schema"`
			} `yaml:"Limit"`
			SearchQuery struct {
				Schema struct {
					MaxLength int `yaml:"maxLength"`
				} `yaml:"schema"`
			} `yaml:"SearchQuery"`
		} `yaml:"parameters"`
	} `yaml:"components"`
}

// readBundle читает и разбирает собранную спецификацию по пути path.
func readBundle(path string) (bundle, error) {
	source, err := os.ReadFile(path) //nolint:gosec // путь приходит из теста, а не извне
	if err != nil {
		return bundle{}, fmt.Errorf("прочитать спецификацию %s: %w", path, err)
	}

	var document bundle
	if err := yaml.Unmarshal(source, &document); err != nil {
		return bundle{}, fmt.Errorf("разобрать спецификацию %s: %w", path, err)
	}

	return document, nil
}

// ReadPageWindow читает границы страницы из собранной спецификации по пути path.
func ReadPageWindow(path string) (PageWindow, error) {
	document, err := readBundle(path)
	if err != nil {
		return PageWindow{}, err
	}

	window := PageWindow{
		Default: document.Components.Parameters.Limit.Schema.Default,
		Max:     document.Components.Parameters.Limit.Schema.Maximum,
	}
	if window.Default == 0 || window.Max == 0 {
		return PageWindow{}, fmt.Errorf("в спецификации %s не объявлены границы параметра limit", path)
	}

	return window, nil
}

// ReadSearchQueryMaxLength читает предел длины поисковой подстроки `q`, объявленный
// спецификацией по пути path.
func ReadSearchQueryMaxLength(path string) (int, error) {
	document, err := readBundle(path)
	if err != nil {
		return 0, err
	}

	maxLength := document.Components.Parameters.SearchQuery.Schema.MaxLength
	if maxLength == 0 {
		return 0, fmt.Errorf("в спецификации %s не объявлен maxLength параметра q", path)
	}

	return maxLength, nil
}

// RestaurantLimits — пределы реквизитов заведения, объявленные схемой `NewRestaurant`
// админской спецификации.
type RestaurantLimits struct {
	// NameMaxLength — предел длины названия заведения.
	NameMaxLength int
	// BaseURLMaxLength — предел длины адреса интеграции.
	BaseURLMaxLength int
}

// ReadRestaurantLimits читает пределы реквизитов заведения из собранной спецификации по
// пути path.
func ReadRestaurantLimits(path string) (RestaurantLimits, error) {
	document, err := readBundle(path)
	if err != nil {
		return RestaurantLimits{}, err
	}

	properties := document.Components.Schemas.NewRestaurant.Properties
	limits := RestaurantLimits{
		NameMaxLength:    properties.Name.MaxLength,
		BaseURLMaxLength: properties.BaseURL.MaxLength,
	}
	if limits.NameMaxLength == 0 || limits.BaseURLMaxLength == 0 {
		return RestaurantLimits{}, fmt.Errorf(
			"в спецификации %s не объявлены пределы реквизитов заведения (NewRestaurant)", path)
	}

	return limits, nil
}

// Operation — операция собранной спецификации: её `operationId`, метод и путь. Путь —
// такой, каким он объявлен в документе, то есть без префикса из адреса сервера.
type Operation struct {
	// Name — `operationId` операции.
	Name string
	// Method — метод запроса заглавными буквами, как его пишет net/http.
	Method string
	// Path — путь операции в документе.
	Path string
}

// ReadOperations читает операции с тегом tag из собранной спецификации по пути path.
//
// Порядок выдачи — по пути и методу, а не по порядку в документе: сравнивать с ним список
// в коде можно только тогда, когда порядок не зависит от того, как кто-то переставил
// абзацы в спецификации.
func ReadOperations(path, tag string) ([]Operation, error) {
	document, err := readBundle(path)
	if err != nil {
		return nil, err
	}

	var operations []Operation

	for route, methods := range document.Paths {
		for method, operation := range methods {
			if !slices.Contains(operation.Tags, tag) {
				continue
			}

			operations = append(operations, Operation{
				Name:   operation.OperationID,
				Method: strings.ToUpper(method),
				Path:   route,
			})
		}
	}

	if len(operations) == 0 {
		return nil, fmt.Errorf("в спецификации %s нет ни одной операции с тегом %s", path, tag)
	}

	slices.SortFunc(operations, func(a, b Operation) int {
		if byPath := strings.Compare(a.Path, b.Path); byPath != 0 {
			return byPath
		}

		return strings.Compare(a.Method, b.Method)
	})

	return operations, nil
}
