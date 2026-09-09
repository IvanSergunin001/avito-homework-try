package http_test

import (
	"testing"

	"github.com/IvanSergunin001/Test157/internal/pkg/spec"
	platformhttp "github.com/IvanSergunin001/Test157/internal/platform/adapters/http"
)

// TestCatalogPageWindowMatchesTheContract не даёт коду и контракту разъехаться: границы
// страницы обещаны клиентам спецификацией, а подставляет их площадка. Разойтись они
// могут только молча — этот тест и есть то место, где расхождение становится слышным.
func TestCatalogPageWindowMatchesTheContract(t *testing.T) {
	window, err := spec.ReadPageWindow("../../../../api/customer/v1/bundle.yaml")
	if err != nil {
		t.Fatalf("прочитать границы страницы из контракта: %v", err)
	}

	if window.Default != platformhttp.CustomerPageDefault {
		t.Errorf("контракт обещает страницу по умолчанию %d, площадка подставляет %d",
			window.Default, platformhttp.CustomerPageDefault)
	}
	if window.Max != platformhttp.CustomerPageMax {
		t.Errorf("контракт обещает потолок страницы %d, площадка держит %d",
			window.Max, platformhttp.CustomerPageMax)
	}
}

// TestSearchQueryLimitMatchesTheContract — та же сверка для предела длины подстроки `q`:
// кодогенератор `maxLength` в тип не переносит, поэтому проверяет его площадка сама, и
// без этого теста правка спецификации не уронила бы ничего.
func TestSearchQueryLimitMatchesTheContract(t *testing.T) {
	maxLength, err := spec.ReadSearchQueryMaxLength("../../../../api/customer/v1/bundle.yaml")
	if err != nil {
		t.Fatalf("прочитать предел длины подстроки из контракта: %v", err)
	}

	if maxLength != platformhttp.CustomerMaxSearchQuery {
		t.Errorf("контракт обещает подстроку не длиннее %d знаков, площадка держит %d",
			maxLength, platformhttp.CustomerMaxSearchQuery)
	}
}
