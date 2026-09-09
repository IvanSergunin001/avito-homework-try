package http_test

import (
	"slices"
	"testing"

	"github.com/IvanSergunin001/Test157/internal/pkg/spec"
	platformhttp "github.com/IvanSergunin001/Test157/internal/platform/adapters/http"
	"github.com/IvanSergunin001/Test157/internal/platform/domain"
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

// TestRestaurantLimitsMatchTheContract — та же сверка для пределов реквизитов заведения:
// `maxLength` схемы `NewRestaurant` объявлен админской спецификацией, а проверяет его
// предметная область сама, потому что кодогенератор `maxLength` в тип не переносит.
// Без этого теста правка спецификации разошлась бы с кодом молча.
func TestRestaurantLimitsMatchTheContract(t *testing.T) {
	limits, err := spec.ReadRestaurantLimits("../../../../api/admin/v1/bundle.yaml")
	if err != nil {
		t.Fatalf("прочитать пределы реквизитов заведения из контракта: %v", err)
	}

	if limits.NameMaxLength != domain.PartnerNameMaxLength {
		t.Errorf("контракт обещает название не длиннее %d знаков, площадка держит %d",
			limits.NameMaxLength, domain.PartnerNameMaxLength)
	}
	if limits.BaseURLMaxLength != domain.BaseURLMaxLength {
		t.Errorf("контракт обещает адрес интеграции не длиннее %d знаков, площадка держит %d",
			limits.BaseURLMaxLength, domain.BaseURLMaxLength)
	}
}

// TestContractOperationsMatchTheContract сверяет со спецификацией список обращений
// площадки к коробке: по нему строятся адреса, которые площадка показывает заведению
// (`GET /partner/integration`), и по нему же она будет в заведение ходить. Список объявлен
// в коде, потому что путь нужно приписать к адресу интеграции, а не взять из документа
// целиком, — и без этой сверки новая операция контракта не появилась бы в нём никогда.
func TestContractOperationsMatchTheContract(t *testing.T) {
	// Тег `restaurant` — половина контракта, которую реализует заведение: именно за ней
	// площадка и ходит. Операции с тегом `platform` живут на площадке, и адрес у них свой.
	operations, err := spec.ReadOperations("../../../../api/partner/v1/bundle.yaml", "restaurant")
	if err != nil {
		t.Fatalf("прочитать операции партнёрского контракта: %v", err)
	}

	// Путь в документе — без префикса из адреса сервера: спецификация объявляет сервером
	// `{restaurantBaseUrl}/api/kitchen/v1/restaurant`, а площадка приписывает к адресу
	// интеграции путь целиком.
	const contractPrefix = "/api/kitchen/v1/restaurant"

	want := make([]string, 0, len(operations))
	for _, operation := range operations {
		want = append(want, operation.Method+" "+contractPrefix+operation.Path+" ("+operation.Name+")")
	}

	got := make([]string, 0, len(domain.ContractOperations()))
	for _, operation := range domain.ContractOperations() {
		got = append(got, operation.Method+" "+operation.Path+" ("+operation.Name+")")
	}

	slices.Sort(got)
	slices.Sort(want)

	if !slices.Equal(got, want) {
		t.Errorf("площадка ходит в заведение по %v, контракт объявляет %v", got, want)
	}
}
