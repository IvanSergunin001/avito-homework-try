// Онбординг заведения и допуск к партнёрскому API. Заголовка авторизации в админских
// запросах здесь нет ни одного — и это проверка, а не небрежность: админские операции в
// MVP не закрыты токеном, сотрудник называет себя тем же `User-Id`, что и покупатель.
// Закрыто ключом только партнёрское API: за ним стоит другая организация, двигающая
// чужие заказы.
package http_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	admin "github.com/IvanSergunin001/Test157/api/admin/v1"
	platformhttp "github.com/IvanSergunin001/Test157/internal/platform/adapters/http"
)

// catalogRefresh — партнёрская операция, на которой видно, пустила площадка звонящего или
// нет. Сама она приезжает своим слайсом; здесь через неё проверяется только допуск.
var catalogRefresh = platformhttp.PartnerAPIPrefix + "/catalog/refresh"

func TestAdminConnectsRestaurantWithItsIntegrationAddress(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)

	registered := platform.register(t, "Пекарня «Батон»", boxAddress)

	if registered.BaseURL != boxAddress {
		t.Errorf("площадка запомнила адрес %q, ожидали %q", registered.BaseURL, boxAddress)
	}
	// Подключённое заведение сразу работает: регистрируют его затем, чтобы принимать
	// заказы, — иначе половина заведений осталась бы в невидимом состоянии.
	if got := platform.card(t, registered.ID).Name; got != "Пекарня «Батон»" {
		t.Errorf("витрина показывает %q, ожидали подключённое заведение «Пекарня «Батон»»", got)
	}
}

func TestRestaurantMovesToAnotherAddressWithoutTouchingTheCode(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	registered := platform.register(t, "Пекарня «Батон»", boxAddress)

	const newAddress = "https://baton.example.org:9443"
	resp := platform.change(t, registered.ID, admin.RestaurantUpdate{BaseUrl: strptr(newAddress)})

	if resp.JSON200 == nil {
		t.Fatalf("переезд заведения вернул %d, ожидали 200: %s", resp.HTTPResponse.StatusCode, resp.Body)
	}
	if got := platform.integrationAddress(t, registered.ID); got != newAddress {
		t.Errorf("площадка стучится по адресу %q, ожидали новый %q", got, newAddress)
	}
}

// Правка частичная: прислали одно поле — меняется одно поле. Иначе отключение заведения
// требовало бы прислать вместе с ним и адрес, а администратор, приславший неполный
// запрос, тихо стёр бы реквизиты, которых не касался.
func TestChangingOneRequisiteLeavesTheRestAsItWas(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	registered := platform.register(t, "Пекарня «Батон»", boxAddress)

	off := false
	resp := platform.change(t, registered.ID, admin.RestaurantUpdate{IsActive: &off})

	if resp.JSON200 == nil {
		t.Fatalf("отключение заведения вернуло %d, ожидали 200: %s", resp.HTTPResponse.StatusCode, resp.Body)
	}
	if resp.JSON200.IsActive {
		t.Error("заведение осталось подключённым, а его просили отключить")
	}
	if resp.JSON200.Name != "Пекарня «Батон»" || resp.JSON200.BaseUrl != boxAddress {
		t.Errorf("после правки заведение стало %q по адресу %q, ожидали прежние «Пекарня «Батон»» и %q",
			resp.JSON200.Name, resp.JSON200.BaseUrl, boxAddress)
	}
	if got := platform.integrationAddress(t, registered.ID); got != boxAddress {
		t.Errorf("в хранилище адрес %q, ожидали прежний %q", got, boxAddress)
	}
}

func TestRestaurantThatWasNeverConnectedCannotBeMoved(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)

	resp := platform.change(t, uuid.Max, admin.RestaurantUpdate{BaseUrl: strptr(boxAddress)})

	if resp.ApplicationproblemJSON404 == nil {
		t.Fatalf("правка несуществующего заведения вернула %d, ожидали 404: %s",
			resp.HTTPResponse.StatusCode, resp.Body)
	}
	if code := resp.ApplicationproblemJSON404.Code; code != admin.RestaurantNotFound {
		t.Errorf("код отказа %q, ожидали %q", code, admin.RestaurantNotFound)
	}
}

func TestRestaurantIsNotConnectedWithoutAnAddressToReach(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)

	// Тела здесь неполные и неверные — таких сгенерированный клиент типами не выразит,
	// поэтому они уезжают его же ручкой, принимающей готовое тело: заголовки и путь
	// остаются контрактными, а содержимое проверяет площадка.
	for name, body := range map[string]string{
		"без адреса":           `{"name":"Пекарня «Батон»"}`,
		"адрес не адрес":       `{"name":"Пекарня «Батон»","base_url":"пекарня за углом"}`,
		"адрес без схемы":      `{"name":"Пекарня «Батон»","base_url":"baton.example.org"}`,
		"без названия":         `{"base_url":"https://baton.example.org"}`,
		"название из пробелов": `{"name":"   ","base_url":"https://baton.example.org"}`,
		"адрес с параметрами":  `{"name":"Пекарня «Батон»","base_url":"https://baton.example.org?debug=1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := platform.onboarding(t).RegisterRestaurantWithBodyWithResponse(t.Context(),
				&admin.RegisterRestaurantParams{UserId: staffMember},
				"application/json", strings.NewReader(body))
			if err != nil {
				t.Fatalf("отправить регистрацию: %v", err)
			}

			if resp.ApplicationproblemJSON400 == nil {
				t.Fatalf("регистрация вернула %d, ожидали 400: %s", resp.HTTPResponse.StatusCode, resp.Body)
			}
			if code := resp.ApplicationproblemJSON400.Code; code != admin.ValidationFailed {
				t.Errorf("код отказа %q, ожидали %q", code, admin.ValidationFailed)
			}
		})
	}
}

func TestRegistrationTellsTheRestaurantEverythingItNeedsToStart(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)

	resp, err := platform.onboarding(t).RegisterRestaurantWithResponse(t.Context(),
		&admin.RegisterRestaurantParams{UserId: staffMember},
		admin.NewRestaurant{Name: "Пекарня «Батон»", BaseUrl: "https://baton.example.org/"})
	if err != nil {
		t.Fatalf("подключить заведение: %v", err)
	}
	if resp.JSON201 == nil {
		t.Fatalf("регистрация вернула %d, ожидали 201: %s", resp.HTTPResponse.StatusCode, resp.Body)
	}

	registered := *resp.JSON201

	if registered.PartnerKey == "" {
		t.Error("в ответе нет партнёрского ключа, а другого случая узнать его у заведения не будет")
	}
	if !registered.IsActive {
		t.Error("подключённое заведение отдано выключенным, а подключают его, чтобы оно работало")
	}
	// Завершающий слэш адреса площадка снимает при подключении: иначе один и тот же
	// адрес, записанный со слэшем и без, давал бы два разных пути к одному заведению.
	if registered.BaseUrl != "https://baton.example.org" {
		t.Errorf("адрес интеграции %q, ожидали приведённый к https://baton.example.org", registered.BaseUrl)
	}
	if registered.Name != "Пекарня «Батон»" {
		t.Errorf("название %q, ожидали «Пекарня «Батон»»", registered.Name)
	}
}

func TestPlatformKeepsOnlyTheHashOfThePartnerKey(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	registered := platform.register(t, "Пекарня «Батон»", boxAddress)

	stored := platform.storedKeyHash(t, registered.ID)

	if stored == registered.Key {
		t.Fatal("ключ лежит в базе открытым: утёкшая копия базы дала бы звонить от имени заведения")
	}
	// Хеш детерминированный и без соли — иначе заведение не найти по предъявленному
	// ключу, не перебрав всю таблицу.
	sum := sha256.Sum256([]byte(registered.Key))
	if want := hex.EncodeToString(sum[:]); stored != want {
		t.Errorf("в базе %q, ожидали хеш выданного ключа %q", stored, want)
	}
}

func TestPartnerIsRecognisedByTheKeyItWasGiven(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	registered := platform.register(t, "Пекарня «Батон»", boxAddress)

	resp := platform.do(t, http.MethodPost, catalogRefresh, registered.Key, nil)

	// Что именно ответит операция — дело слайса, который её реализует; правило,
	// проверяемое здесь, в другом: заведение со своим ключом на партнёрское API
	// проходит, а не отвергается на пороге.
	if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden {
		t.Errorf("площадка ответила %d: выданный ею же ключ не открыл партнёрское API", resp.Status)
	}
}

func TestPartnerAPIIsClosedToRequestsWithoutAKey(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)

	resp := platform.do(t, http.MethodPost, catalogRefresh, "", nil)

	document := asRefusal(t, resp, http.StatusUnauthorized)
	if document.Code != "unauthorized" {
		t.Errorf("код отказа %q, ожидали unauthorized", document.Code)
	}
	// Отказ называет схему: клиент без этого знает, что его не пустили, но не знает, чем
	// стучаться.
	if !strings.Contains(resp.Challenge, "Bearer") {
		t.Errorf("WWW-Authenticate = %q, ожидали указание схемы Bearer", resp.Challenge)
	}
}

func TestUnknownKeyOpensNothingAndNamesTheScheme(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)

	resp := platform.do(t, http.MethodPost, catalogRefresh, "pk_подобранный-ключ", nil)

	document := asRefusal(t, resp, http.StatusUnauthorized)
	if document.Code != "unauthorized" {
		t.Errorf("код отказа %q, ожидали unauthorized", document.Code)
	}
	if !strings.Contains(resp.Challenge, "Bearer") {
		t.Errorf("WWW-Authenticate = %q, ожидали указание схемы Bearer", resp.Challenge)
	}
}

func TestKeyOfSwitchedOffRestaurantOpensNothing(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	registered := platform.register(t, "Столовая «Обед»", boxAddress)

	off := false
	if resp := platform.change(t, registered.ID, admin.RestaurantUpdate{IsActive: &off}); resp.JSON200 == nil {
		t.Fatalf("отключение заведения вернуло %d, ожидали 200: %s", resp.HTTPResponse.StatusCode, resp.Body)
	}

	// Отключили заведение затем, чтобы оно перестало работать с площадкой, а не только
	// исчезло с витрины: иначе отключённый партнёр продолжал бы дёргать её ручки.
	resp := platform.do(t, http.MethodPost, catalogRefresh, registered.Key, nil)

	if document := asRefusal(t, resp, http.StatusUnauthorized); document.Code != "unauthorized" {
		t.Errorf("код отказа %q, ожидали unauthorized", document.Code)
	}
}

// integrationAddress читает адрес, по которому площадка стучится в заведение. Ручки,
// которая показывала бы реквизиты, у админского API нет — а правило «адрес меняет только
// тот, кто его прислал» проверяется именно по хранимому адресу, а не по коду ответа.
func (p *platform) integrationAddress(t *testing.T, id uuid.UUID) string {
	t.Helper()

	var address string
	row := p.db.QueryRowContext(t.Context(), `SELECT base_url FROM platform.restaurants WHERE id = $1`, id)
	if err := row.Scan(&address); err != nil {
		t.Fatalf("прочитать адрес заведения %s: %v", id, err)
	}

	return address
}

// storedKeyHash читает то, что площадка хранит вместо партнёрского ключа.
func (p *platform) storedKeyHash(t *testing.T, id uuid.UUID) string {
	t.Helper()

	var hash string
	row := p.db.QueryRowContext(t.Context(), `SELECT api_key_hash FROM platform.restaurants WHERE id = $1`, id)
	if err := row.Scan(&hash); err != nil {
		t.Fatalf("прочитать хеш ключа заведения %s: %v", id, err)
	}

	return hash
}

// strptr — адрес строкового литерала: частичная правка отличает «поле не прислали» от
// присланного указателем, а взять адрес литерала в Go нельзя.
func strptr(s string) *string {
	return &s
}
