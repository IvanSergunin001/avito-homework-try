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

	"gopkg.in/yaml.v3"
)

// PageWindow — окно страницы, объявленное параметром `limit` в спецификации.
type PageWindow struct {
	// Default — размер страницы, когда клиент не просил своего.
	Default int
	// Max — потолок, выше которого служба страницу не отдаёт.
	Max int
}

// bundle — та часть собранного документа, которая нужна для чтения объявленных
// контрактом ограничений на параметры запроса.
type bundle struct {
	Components struct {
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
