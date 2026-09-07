package http_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// catalogRefresh — партнёрская операция, на которой видно, пустила площадка звонящего или
// нет. Сама она приезжает своим слайсом; здесь через неё проверяется только допуск.
const catalogRefresh = "/api/partner/v1/catalog/refresh"

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
	resp := platform.change(t, registered.ID, "Пекарня «Батон»", newAddress, true, adminToken)

	if resp.Status != http.StatusOK {
		t.Fatalf("переезд заведения вернул %d, ожидали 200: %s", resp.Status, resp.Body)
	}
	if got := platform.integrationAddress(t, registered.ID); got != newAddress {
		t.Errorf("площадка стучится по адресу %q, ожидали новый %q", got, newAddress)
	}
}

func TestRestaurantAddressCannotBeChangedWithoutAdminToken(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	registered := platform.register(t, "Пекарня «Батон»", boxAddress)

	resp := platform.change(t, registered.ID, "Пекарня «Батон»", "https://чужой.example.org", true, "")

	document := asRefusal(t, resp, http.StatusUnauthorized)
	if document.Code != "unauthorized" {
		t.Errorf("код отказа %q, ожидали unauthorized", document.Code)
	}
	// Отказ называет схему: клиент без этого знает, что его не пустили, но не знает, чем
	// стучаться.
	if !strings.Contains(resp.Challenge, "Bearer") {
		t.Errorf("WWW-Authenticate = %q, ожидали указание схемы Bearer", resp.Challenge)
	}
	// Главное — не код ответа, а то, что адрес заведения остался прежним: подмена адреса
	// увела бы чужие заказы туда, куда попросил посторонний.
	if got := platform.integrationAddress(t, registered.ID); got != boxAddress {
		t.Errorf("адрес заведения стал %q, ожидали прежний %q", got, boxAddress)
	}
}

func TestUnknownTokenOpensNothingAndNamesTheScheme(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	registered := platform.register(t, "Пекарня «Батон»", boxAddress)

	resp := platform.change(t, registered.ID, "Пекарня «Батон»", boxAddress, true, "подобранный-токен")

	document := asRefusal(t, resp, http.StatusUnauthorized)
	if document.Code != "unauthorized" {
		t.Errorf("код отказа %q, ожидали unauthorized", document.Code)
	}
	if !strings.Contains(resp.Challenge, "Bearer") {
		t.Errorf("WWW-Authenticate = %q, ожидали указание схемы Bearer", resp.Challenge)
	}
}

func TestPartnerKeyDoesNotOpenAdminOperations(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	registered := platform.register(t, "Пекарня «Батон»", boxAddress)
	stranger := platform.register(t, "Кофейня «Пар»", "https://par.example.org")

	// Заведение опознано — и всё равно не может переселить ни соседа, ни себя: админская
	// операция выполняется админским токеном, а не любым известным площадке.
	resp := platform.change(t, registered.ID, "Пекарня «Батон»", "https://чужой.example.org", true, stranger.Key)

	document := asRefusal(t, resp, http.StatusForbidden)
	if document.Code != "forbidden" {
		t.Errorf("код отказа %q, ожидали forbidden", document.Code)
	}
	if got := platform.integrationAddress(t, registered.ID); got != boxAddress {
		t.Errorf("адрес заведения стал %q, ожидали прежний %q", got, boxAddress)
	}
}

func TestRestaurantThatWasNeverConnectedCannotBeMoved(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)

	resp := platform.change(t, uuid.Max, "Пекарня «Батон»", boxAddress, true, adminToken)

	if document := asRefusal(t, resp, http.StatusNotFound); document.Code != "restaurant_not_found" {
		t.Errorf("код отказа %q, ожидали restaurant_not_found", document.Code)
	}
}

func TestRestaurantIsNotConnectedWithoutAnAddressToReach(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)

	for name, body := range map[string]string{
		"без адреса":           `{"name":"Пекарня «Батон»"}`,
		"адрес не адрес":       `{"name":"Пекарня «Батон»","base_url":"пекарня за углом"}`,
		"адрес без схемы":      `{"name":"Пекарня «Батон»","base_url":"baton.example.org"}`,
		"без названия":         `{"base_url":"https://baton.example.org"}`,
		"название из пробелов": `{"name":"   ","base_url":"https://baton.example.org"}`,
		"адрес с параметрами":  `{"name":"Пекарня «Батон»","base_url":"https://baton.example.org?debug=1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			resp := platform.do(t, http.MethodPost, "/api/admin/v1/restaurants", adminToken, []byte(body))

			if document := asRefusal(t, resp, http.StatusBadRequest); document.Code != "validation_failed" {
				t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
			}
		})
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
	// проверяемое здесь, в другом: заведение со своим ключом на партнёрскую поверхность
	// проходит, а не отвергается на пороге.
	if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden {
		t.Errorf("площадка ответила %d: выданный ею же ключ не открыл партнёрскую поверхность", resp.Status)
	}
}

func TestPartnerSurfaceIsClosedToRequestsWithoutAKey(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)

	resp := platform.do(t, http.MethodPost, catalogRefresh, "", nil)

	document := asRefusal(t, resp, http.StatusUnauthorized)
	if document.Code != "unauthorized" {
		t.Errorf("код отказа %q, ожидали unauthorized", document.Code)
	}
	if !strings.Contains(resp.Challenge, "Bearer") {
		t.Errorf("WWW-Authenticate = %q, ожидали указание схемы Bearer", resp.Challenge)
	}
}

func TestAdminTokenDoesNotSpeakForARestaurant(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)

	// Партнёрская операция выполняется от имени заведения, а админский токен заведения не
	// называет: выполнить её «вообще» нельзя.
	resp := platform.do(t, http.MethodPost, catalogRefresh, adminToken, nil)

	if document := asRefusal(t, resp, http.StatusForbidden); document.Code != "forbidden" {
		t.Errorf("код отказа %q, ожидали forbidden", document.Code)
	}
}

func TestKeyOfSwitchedOffRestaurantOpensNothing(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	registered := platform.register(t, "Столовая «Обед»", boxAddress)

	if resp := platform.change(t, registered.ID, "Столовая «Обед»", boxAddress, false, adminToken); resp.Status != http.StatusOK {
		t.Fatalf("отключение заведения вернуло %d, ожидали 200: %s", resp.Status, resp.Body)
	}

	// Отключили заведение затем, чтобы оно перестало работать с площадкой, а не только
	// исчезло с витрины: иначе отключённый партнёр продолжал бы дёргать её ручки.
	resp := platform.do(t, http.MethodPost, catalogRefresh, registered.Key, nil)

	if document := asRefusal(t, resp, http.StatusUnauthorized); document.Code != "unauthorized" {
		t.Errorf("код отказа %q, ожидали unauthorized", document.Code)
	}
}

// integrationAddress читает адрес, по которому площадка стучится в заведение. Ручки,
// которая показывала бы реквизиты, у админской поверхности нет — а правило «адрес не
// подменить» проверяется именно по хранимому адресу, а не по коду ответа.
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

// registeredRestaurant — ответ регистрации целиком: тесты разбирают его, когда проверяют
// не сам факт подключения, а то, что площадка о заведении рассказала.
type registeredRestaurant struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	BaseURL    string `json:"base_url"`
	IsActive   bool   `json:"is_active"`
	PartnerKey string `json:"partner_key"`
}

func TestRegistrationTellsTheRestaurantEverythingItNeedsToStart(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)

	resp := platform.do(t, http.MethodPost, "/api/admin/v1/restaurants", adminToken,
		[]byte(`{"name":"Пекарня «Батон»","base_url":"https://baton.example.org/"}`))
	if resp.Status != http.StatusCreated {
		t.Fatalf("регистрация вернула %d, ожидали 201: %s", resp.Status, resp.Body)
	}

	var registered registeredRestaurant
	if err := json.Unmarshal(resp.Body, &registered); err != nil {
		t.Fatalf("разобрать ответ регистрации: %v", err)
	}

	if registered.PartnerKey == "" {
		t.Error("в ответе нет партнёрского ключа, а другого случая узнать его у заведения не будет")
	}
	if !registered.IsActive {
		t.Error("подключённое заведение отдано выключенным, а подключают его, чтобы оно работало")
	}
	// Завершающий слэш адреса площадка снимает при подключении: иначе один и тот же
	// адрес, записанный со слэшем и без, давал бы два разных пути к одному заведению.
	if registered.BaseURL != "https://baton.example.org" {
		t.Errorf("адрес интеграции %q, ожидали приведённый к https://baton.example.org", registered.BaseURL)
	}
	if registered.Name != "Пекарня «Батон»" {
		t.Errorf("название %q, ожидали «Пекарня «Батон»»", registered.Name)
	}
}
