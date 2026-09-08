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

// bundle — та часть собранного документа, которая нужна для чтения границ страницы.
type bundle struct {
	Components struct {
		Parameters struct {
			Limit struct {
				Schema struct {
					Default int `yaml:"default"`
					Maximum int `yaml:"maximum"`
				} `yaml:"schema"`
			} `yaml:"Limit"`
		} `yaml:"parameters"`
	} `yaml:"components"`
}

// ReadPageWindow читает границы страницы из собранной спецификации по пути path.
func ReadPageWindow(path string) (PageWindow, error) {
	source, err := os.ReadFile(path) //nolint:gosec // путь приходит из теста, а не извне
	if err != nil {
		return PageWindow{}, fmt.Errorf("прочитать спецификацию %s: %w", path, err)
	}

	var document bundle
	if err := yaml.Unmarshal(source, &document); err != nil {
		return PageWindow{}, fmt.Errorf("разобрать спецификацию %s: %w", path, err)
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
