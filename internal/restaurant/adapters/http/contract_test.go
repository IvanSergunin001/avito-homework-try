package http_test

import (
	"testing"

	"github.com/IvanSergunin001/Test157/internal/pkg/spec"
	restauranthttp "github.com/IvanSergunin001/Test157/internal/restaurant/adapters/http"
)

// TestMenuPageWindowMatchesTheContract не даёт коду и контракту разъехаться: страничную
// выдачу меню обещает партнёрский контракт, а границы подставляет коробка.
func TestMenuPageWindowMatchesTheContract(t *testing.T) {
	window, err := spec.ReadPageWindow("../../../../api/partner/v1/bundle.yaml")
	if err != nil {
		t.Fatalf("прочитать границы страницы из контракта: %v", err)
	}

	if window.Default != restauranthttp.MenuPageDefault {
		t.Errorf("контракт обещает страницу по умолчанию %d, коробка подставляет %d",
			window.Default, restauranthttp.MenuPageDefault)
	}
	if window.Max != restauranthttp.MenuPageMax {
		t.Errorf("контракт обещает потолок страницы %d, коробка держит %d",
			window.Max, restauranthttp.MenuPageMax)
	}
}
