// Онбординг заведения и допуск к партнёрскому API. Заголовка авторизации в админских
// запросах здесь нет ни одного — и это проверка, а не небрежность: админские операции в
// MVP не закрыты токеном, сотрудник называет себя тем же `User-Id`, что и покупатель.
// Закрыто ключом только партнёрское API: за ним стоит другая организация, двигающая
// чужие заказы.
package http_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	admin "github.com/IvanSergunin001/Test157/api/admin/v1"
	"github.com/IvanSergunin001/Test157/internal/pkg/httpx"
	platformhttp "github.com/IvanSergunin001/Test157/internal/platform/adapters/http"
	"github.com/IvanSergunin001/Test157/internal/platform/adapters/http/partner_v1"
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

// Адрес интеграции — это вход заведения, и заведение вправе описать его как ему удобно:
// с портом, с префиксом пути, с параметром запроса. Площадка не вырезает из него ничего —
// пути контракта она приписывает разбором адреса, а не сложением строк, поэтому запрещать
// ей тут нечего.
func TestRestaurantIsReachedAtTheAddressItGaveDownToItsQueryAndPrefix(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)

	const address = "https://baton.example.org:9443/kitchen?tenant=7"
	registered := platform.register(t, "Пекарня «Батон»", address)

	if registered.BaseURL != address {
		t.Fatalf("площадка запомнила адрес %q, ожидали %q — из адреса интеграции ничего не вырезается",
			registered.BaseURL, address)
	}

	integration := platform.integration(t, registered.Key)

	// Путь контракта приписан к пути адреса, а параметр запроса остался параметром: при
	// сложении строк путь уехал бы в строку запроса, и заведение не ответило бы ни на один
	// запрос площадки.
	const wantMenu = "https://baton.example.org:9443/kitchen/api/kitchen/v1/restaurant/menu?tenant=7"
	if got := endpointURL(t, integration, "listMenu"); got != wantMenu {
		t.Errorf("за меню площадка пойдёт по %q, ожидали %q", got, wantMenu)
	}
}

// Заведение обязано иметь возможность проверить склейку само: адрес интеграции сообщает
// оно, а пути к нему приписывает площадка, и до этой ручки первым сообщением о неверном
// адресе был неотвеченный запрос в момент первого заказа.
func TestRestaurantSeesEveryAddressThePlatformWillCallItAt(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	registered := platform.register(t, "Пекарня «Батон»", boxAddress)

	integration := platform.integration(t, registered.Key)

	if integration.BaseUrl != boxAddress {
		t.Errorf("площадка называет адресом интеграции %q, ожидали %q", integration.BaseUrl, boxAddress)
	}

	// Все обращения площадки к коробке, ни одним меньше: список сверен со спецификацией
	// отдельным тестом (TestContractOperationsMatchTheContract), здесь проверяется, что
	// ручка отдаёт именно его.
	for _, operation := range []string{"listMenu", "acceptOrder", "cancelAcceptedOrder"} {
		if endpointURL(t, integration, operation) == "" {
			t.Errorf("в ответе нет адреса операции %q, а площадка по нему пойдёт", operation)
		}
	}

	// Сегмент параметра остаётся именем параметра, а не уезжает в ответ экранированным:
	// заведение читает его как строку контракта, а не как путь, по которому к нему придут.
	const wantCancel = boxAddress + "/api/kitchen/v1/restaurant/orders/{orderId}/cancel"
	if got := endpointURL(t, integration, "cancelAcceptedOrder"); got != wantCancel {
		t.Errorf("отмену заказа площадка шлёт на %q, ожидали %q", got, wantCancel)
	}
}

// Реквизиты видит то заведение, чьим ключом спросили: посторонний не должен узнать даже
// адреса чужой коробки.
func TestIntegrationIsNotShownWithoutAPartnerKey(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)

	resp := platform.do(t, http.MethodGet, platformhttp.PartnerAPIPrefix+"/integration", "", nil)

	if document := asRefusal(t, resp, http.StatusUnauthorized); document.Code != "unauthorized" {
		t.Errorf("код отказа %q, ожидали unauthorized", document.Code)
	}
}

// Выключение заведения обязано быть обратимым: администратор видит отключённое заведение
// и включает его назад. Если чтение или запись реквизитов начнут отбирать по `is_active`,
// выключение станет необратимым — и заметит это только этот тест.
func TestSwitchedOffRestaurantIsSwitchedBackOnAndWorksAgain(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	registered := platform.register(t, "Столовая «Обед»", boxAddress)

	off, on := false, true
	if resp := platform.change(t, registered.ID, admin.RestaurantUpdate{IsActive: &off}); resp.JSON200 == nil {
		t.Fatalf("отключение заведения вернуло %d, ожидали 200: %s", resp.HTTPResponse.StatusCode, resp.Body)
	}

	resp := platform.change(t, registered.ID, admin.RestaurantUpdate{IsActive: &on})
	if resp.JSON200 == nil {
		t.Fatalf("включение заведения обратно вернуло %d, ожидали 200: %s",
			resp.HTTPResponse.StatusCode, resp.Body)
	}
	if !resp.JSON200.IsActive {
		t.Fatal("отключённое заведение не включается обратно — выключение оказалось необратимым")
	}

	// Включили его затем, чтобы оно снова работало: ключ обязан открывать партнёрское API,
	// как открывал до отключения.
	if got := platform.do(t, http.MethodPost, catalogRefresh, registered.Key, nil); got.Status == http.StatusUnauthorized {
		t.Error("площадка ответила 401: включённое обратно заведение не опознаётся по своему ключу")
	}
}

// Правка без единого поля — не «ничего не меняем», а запрос, в котором забыли сказать, что
// менять. Принять его значило бы переписать отметку о последней правке и соврать о том,
// когда реквизиты трогали.
func TestRequisiteChangeWithoutASingleFieldIsRefused(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	registered := platform.register(t, "Пекарня «Батон»", boxAddress)

	resp := platform.change(t, registered.ID, admin.RestaurantUpdate{})

	if resp.ApplicationproblemJSON400 == nil {
		t.Fatalf("пустая правка вернула %d, ожидали 400: %s", resp.HTTPResponse.StatusCode, resp.Body)
	}
	if code := resp.ApplicationproblemJSON400.Code; code != admin.ValidationFailed {
		t.Errorf("код отказа %q, ожидали %q", code, admin.ValidationFailed)
	}
}

// Предел на тело запроса стоит на всех трёх API площадки, а не на двух из трёх: тело
// читается в память до того, как хоть одно правило успеет сказать, что запрос неверен.
func TestAdminRequestBodyLargerThanTheLimitIsRefusedWithoutBeingRead(t *testing.T) {
	t.Parallel()

	oversized := bytes.Repeat([]byte("a"), 2*httpx.MB)

	document := asRefusal(t, newPlatform(t).post(t,
		platformhttp.AdminAPIPrefix+"/restaurants", oversized), http.StatusRequestEntityTooLarge)

	if document.Code != string(admin.ValidationFailed) {
		t.Errorf("код отказа %q, ожидали %q", document.Code, admin.ValidationFailed)
	}
}

// Тело без объявленной длины (chunked) отдаётся тем же отказом: до middleware ошибка
// `MaxBytesReader` не поднимается — её обязан свести к `413` тот, кто разбирает тело.
func TestAdminRequestBodyWithoutDeclaredLengthIsRefusedTheSameWay(t *testing.T) {
	t.Parallel()

	oversized := append([]byte(`{"name":"`), bytes.Repeat([]byte("a"), httpx.MB+1024)...)

	document := asRefusal(t, newPlatform(t).postWithoutDeclaredLength(t,
		platformhttp.AdminAPIPrefix+"/restaurants", oversized), http.StatusRequestEntityTooLarge)

	if document.Code != string(admin.ValidationFailed) {
		t.Errorf("код отказа %q, ожидали %q", document.Code, admin.ValidationFailed)
	}
	if strings.Contains(document.Detail, "http: request body too large") {
		t.Errorf("в detail протекло сообщение стандартной библиотеки: %q", document.Detail)
	}
}

// Партнёрское API закрыто ключом, но ключ говорит только о том, что звонит заведение, — не
// о том, что оно исправно. Предел на тело стоит и здесь, после проверки ключа.
func TestPartnerRequestBodyLargerThanTheLimitIsRefusedEvenWithAValidKey(t *testing.T) {
	t.Parallel()

	platform := newPlatform(t)
	registered := platform.register(t, "Пекарня «Батон»", boxAddress)

	oversized := bytes.Repeat([]byte("a"), 2*httpx.MB)

	document := asRefusal(t, platform.do(t, http.MethodPost, catalogRefresh, registered.Key, oversized),
		http.StatusRequestEntityTooLarge)

	if document.Code != "validation_failed" {
		t.Errorf("код отказа %q, ожидали validation_failed", document.Code)
	}
}

// Посторонний с непомерным телом получает `401`, а не `413`: ключ проверяется первым, и
// отказ «у вас слишком большое тело» рассказал бы об API больше, чем «вас сюда не пускают».
func TestOversizedBodyFromAStrangerIsStillRefusedAsUnauthorized(t *testing.T) {
	t.Parallel()

	oversized := bytes.Repeat([]byte("a"), 2*httpx.MB)

	resp := newPlatform(t).do(t, http.MethodPost, catalogRefresh, "", oversized)

	if document := asRefusal(t, resp, http.StatusUnauthorized); document.Code != "unauthorized" {
		t.Errorf("код отказа %q, ожидали unauthorized", document.Code)
	}
}

// integration спрашивает у площадки, по каким адресам она будет ходить в заведение,
// ключом самого заведения.
//
// Ответ разбирается сгенерированным по контракту типом — тем же, которым площадка его
// пишет: сгенерированного клиента у партнёрского API площадки нет (клиент собирается по
// половине контракта, которую реализует заведение), а своя копия схемы в тесте разошлась
// бы с контрактом молча.
func (p *platform) integration(t *testing.T, key string) partner_v1.Integration {
	t.Helper()

	resp := p.do(t, http.MethodGet, platformhttp.PartnerAPIPrefix+"/integration", key, nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("площадка ответила %d, ожидали 200: %s", resp.Status, resp.Body)
	}

	var document partner_v1.Integration
	if err := json.Unmarshal(resp.Body, &document); err != nil {
		t.Fatalf("разобрать реквизиты интеграции: %v", err)
	}

	return document
}

// endpointURL достаёт из ответа адрес операции контракта; пустая строка — операции в
// ответе нет.
func endpointURL(t *testing.T, integration partner_v1.Integration, operation string) string {
	t.Helper()

	for _, endpoint := range integration.Endpoints {
		if endpoint.Operation == operation {
			return endpoint.Url
		}
	}

	return ""
}
