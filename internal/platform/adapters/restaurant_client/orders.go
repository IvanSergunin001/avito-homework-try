// Передача заказа заведению: вторая половина той части партнёрского контракта, которую
// площадка вызывает, а не реализует.
//
// Запрос собирается сгенерированным по контракту построителем — заголовки идемпотентности
// и путь операции берутся из спецификации, а не пишутся здесь руками. Принят заказ или
// нет, видно по коду ответа; тело разбирается только у отказа — в нём заведение называет
// причину, из-за которой заказ не взяли, и покупателю решать по ней, что делать дальше.

package restaurant_client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	partner "github.com/IvanSergunin001/Test157/api/partner/v1"
	"github.com/IvanSergunin001/Test157/internal/platform/application"
	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// maxAcceptanceResponseBytes — сколько байт ответа заведения площадка готова прочитать,
// прежде чем закрыть соединение.
//
// Подтверждение приёма — это идентификатор заказа и время, отказ — причина, позиция и
// цена: нескольких килобайт хватает и тому и другому с запасом. Читается ответ и ради
// разбора отказа, и ради того, чтобы соединение вернулось в пул переиспользуемых, а не
// обрывалось после каждого заказа; заведение, отдающее в ответ мегабайты, площадку читать
// их не заставит.
const maxAcceptanceResponseBytes = 4 << 10

// OrderIntake передаёт заказ заведению и ждёт его синхронного решения.
//
// Клиент свой, а не http.DefaultClient: у того нет таймаута вовсе, и заведение, которое
// приняло соединение и замолчало, держало бы покупателя перед экраном бесконечно.
type OrderIntake struct {
	client *http.Client
}

// NewOrderIntake собирает передачу заказа с таймаутом на ответ заведения.
//
// Таймаут задаётся конфигурацией площадки, а не выводится из чего-либо: сколько ждать
// стороннюю систему, решает эксплуатация, и меняется это без пересборки службы. Ожидание
// заказа короче, чем ожидание меню: по ту сторону экрана стоит человек.
func NewOrderIntake(timeout time.Duration) *OrderIntake {
	return &OrderIntake{client: &http.Client{Timeout: timeout}}
}

// Accept передаёт заказ заведению, живущему по адресу baseURL.
//
// Заказ уезжает тем составом и по тем ценам, которые площадка показала покупателю:
// заведение сверяет их со своими и отвечает, берётся ли оно за заказ. Ключ
// идемпотентности — идентификатор самого заказа: отдельного ключа в этом обмене нет, и
// повторная отправка узнаётся заведением по нему же.
func (i *OrderIntake) Accept(
	ctx context.Context, baseURL string, order domain.Order, attempt int,
) error {
	request, err := partner.NewAcceptOrderRequest(
		strings.TrimRight(baseURL, "/")+partnerContractPath,
		&partner.AcceptOrderParams{IdempotencyKey: order.ID, IdempotencyAttempt: attempt},
		incomingOrder(order),
	)
	if err != nil {
		return fmt.Errorf("собрать запрос заказа %s: %w", order.ID, err)
	}

	response, err := i.client.Do(request.WithContext(ctx))
	if err != nil {
		return application.RestaurantUnavailableError{TimedOut: timedOut(err), Err: err}
	}
	defer func() { _ = response.Body.Close() }()

	// Тело читается всегда, даже когда разбирать в нём нечего: закрытое недочитанным
	// соединение в пул не возвращается, и каждый заказ открывал бы к заведению новое.
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAcceptanceResponseBytes))
	if err != nil {
		return application.RestaurantUnavailableError{TimedOut: timedOut(err), Err: err}
	}
	_, _ = io.Copy(io.Discard, response.Body)

	// Принят заказ или нет, видно по коду ответа: `201` — заказ заведён, `200` — заведение
	// уже принимало его раньше и отвечает результатом первой попытки.
	if response.StatusCode == http.StatusCreated || response.StatusCode == http.StatusOK {
		return nil
	}

	// Заказ не принят. Если заведение назвало причину по контракту, она уезжает
	// покупателю: по ней он и решает, что делать дальше — заменить позицию, согласиться
	// на новую цену или уйти в другое заведение.
	if refusal, refused := refusalOf(response.StatusCode, body); refused {
		return refusal
	}

	// Причины нет: заведение ответило не по контракту. Для покупателя это то же самое,
	// что молчание, — выбрать по такому ответу ему нечего.
	return application.RestaurantUnavailableError{
		Err: fmt.Errorf("заведение ответило на переданный заказ %d", response.StatusCode),
	}
}

// refusalOf разбирает ответ заведения как отказ по правилам предметной области.
//
// Отказ — это `409` с машиночитаемой причиной из закрытого перечня контракта, и ничего
// больше: код ответа и код причины проверяются оба. `409` без разбираемого тела, с
// причиной не из перечня или с причиной, которая к приёму заказа отношения не имеет
// (скажем, «заказ уже нельзя отменить»), — это заведение, разошедшееся с контрактом, и
// выдавать его ответ покупателю за объяснение отказа нельзя.
func refusalOf(status int, body []byte) (application.RestaurantRefusalError, bool) {
	if status != http.StatusConflict {
		return application.RestaurantRefusalError{}, false
	}

	var document partner.Problem
	if err := json.Unmarshal(body, &document); err != nil {
		return application.RestaurantRefusalError{}, false
	}

	reason, described := refusalReason(document.Code)
	if !described {
		return application.RestaurantRefusalError{}, false
	}

	refusal := application.RestaurantRefusalError{Reason: reason}
	if document.ProductExternalId != nil {
		refusal.ProductExternalID = *document.ProductExternalId
	}
	if document.CurrentPrice != nil {
		refusal.CurrentPrice = domain.Money(*document.CurrentPrice)
	}
	if document.Detail != nil {
		refusal.Detail = *document.Detail
	}

	return refusal, true
}

// refusalReason переводит код причины партнёрского контракта в причину предметной области
// площадки.
//
// Перевод явный, а не приведением строки: перечень кодов контракта закрыт, и причина,
// которой в нём нет, — это не новая причина, а разошедшееся с контрактом заведение.
// Коды, которыми контракт отвечает на что угодно, кроме приёма заказа, сюда не попадают
// намеренно: отказ в приёме заказа объясняется только тем, что заведение не может его
// выполнить.
func refusalReason(code partner.ProblemCode) (domain.RefusalReason, bool) {
	switch code {
	case partner.OutOfStock:
		return domain.OutOfStock, true
	case partner.PriceChanged:
		return domain.PriceChanged, true
	case partner.UnknownProduct:
		return domain.UnknownProduct, true
	case partner.RestaurantClosed:
		return domain.RestaurantClosed, true
	default:
		return "", false
	}
}

// incomingOrder переводит заказ в схему партнёрского контракта.
//
// Цена уезжает тем же целым числом копеек, каким хранится: перевод в дробное число по
// дороге означал бы, что у площадки и у заведения разные деньги. Позиция называется
// внешним идентификатором — тем, которым её знает само заведение: собственный
// идентификатор товара выдала площадка, и заведению он ничего не говорит.
func incomingOrder(order domain.Order) partner.IncomingOrder {
	items := make([]partner.IncomingOrderItem, 0, len(order.Items))
	for _, item := range order.Items {
		items = append(items, partner.IncomingOrderItem{
			ExternalId: item.ExternalID,
			Quantity:   item.Quantity,
			Price:      int64(item.Price),
		})
	}

	// Время оформления заведению не обязательно, но полезно: по нему оператор видит, как
	// давно покупатель ждёт, — и видит это независимо от того, когда заказ доехал.
	placedAt := order.CreatedAt

	return partner.IncomingOrder{
		OrderId:  order.ID,
		Address:  order.Address,
		Items:    items,
		Total:    int64(order.Total),
		PlacedAt: &placedAt,
	}
}

// timedOut отвечает, означает ли сорванный вызов, что заведение не уложилось в отведённое
// ему время.
//
// Различить это стоит потому, что покупатель получает разные коды ответа: молчащее
// заведение — сорванный вызов, не успевшее — истёкшее ожидание. Проверяется и таймаут
// клиента, и отменённый по сроку контекст: первый срабатывает на медленном ответе,
// второй — когда срок пришёл из вызывающего кода.
func timedOut(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var timeout net.Error

	return errors.As(err, &timeout) && timeout.Timeout()
}
