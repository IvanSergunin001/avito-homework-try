// Отмена заказа у заведения: та часть партнёрского контракта, которой площадка сообщает
// заведению своё решение, а не спрашивает его.
//
// Запрос собирается сгенерированным по контракту построителем — путь операции и тело
// берутся из спецификации, а не пишутся здесь руками. Разбирать ответ нечего: судьбу
// заказа площадка уже решила, и в ответе заведения её интересует только то, приняло ли
// оно отмену, — а это видно по коду.

package restaurant_client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	partner "github.com/IvanSergunin001/Test157/api/partner/v1"
	"github.com/IvanSergunin001/Test157/internal/platform/application"
	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// maxCancellationResponseBytes — сколько байт ответа заведения площадка готова прочитать,
// прежде чем закрыть соединение.
//
// Подтверждение отмены — это идентификатор заказа и время, отказ — причина: нескольких
// килобайт хватает и тому и другому с запасом. Читается ответ ради того, чтобы соединение
// вернулось в пул переиспользуемых, а не обрывалось после каждой отмены, и ради текста
// отказа, который попадёт в лог площадки.
const maxCancellationResponseBytes = 4 << 10

// OrderCancellations доносит до заведения отмену принятого заказа.
//
// Клиент свой, а не http.DefaultClient: у того нет таймаута вовсе, и заведение, которое
// приняло соединение и замолчало, задержало бы ответ покупателю, которому уже нечего
// ждать — отмену площадка к этому моменту записала.
type OrderCancellations struct {
	client *http.Client
}

// NewOrderCancellations собирает отмену с таймаутом на ответ заведения.
//
// Предел тот же, что и у переданного заказа, и по той же причине: по ту сторону экрана
// стоит человек, а сколько ждать стороннюю систему, решает эксплуатация — конфигурацией,
// а не пересборкой службы.
func NewOrderCancellations(timeout time.Duration) *OrderCancellations {
	return &OrderCancellations{client: &http.Client{Timeout: timeout}}
}

// Cancel сообщает заведению, живущему по адресу baseURL, что заказ отменён.
//
// Заказ называется идентификатором площадки — тем самым, которым его передавали:
// собственная нумерация коробки площадке неизвестна, и контракт спрашивает именно его.
//
// Причина уезжает для оператора: он видит в списке заказ, который у него забрали, и
// «отменён» без единого слова читается как сбой интеграции.
//
// Отказ заведения — это ошибка вызова, а не результат: судьбу заказа площадка решила
// сама, и «заведение не согласилось» ей возразить нечем. Что именно ответила коробка,
// остаётся в тексте ошибки — по нему разбирается человек.
func (c *OrderCancellations) Cancel(
	ctx context.Context, baseURL string, order domain.Order, reason string,
) error {
	request, err := partner.NewCancelAcceptedOrderRequest(
		strings.TrimRight(baseURL, "/")+partnerContractPath,
		order.ID,
		partner.CancellationRequest{Reason: &reason},
	)
	if err != nil {
		return fmt.Errorf("собрать запрос отмены заказа %s: %w", order.ID, err)
	}

	response, err := c.client.Do(request.WithContext(ctx))
	if err != nil {
		return application.RestaurantUnavailableError{TimedOut: timedOut(err), Err: err}
	}
	defer func() { _ = response.Body.Close() }()

	// Тело читается всегда, даже когда разбирать в нём нечего: закрытое недочитанным
	// соединение в пул не возвращается, и каждая отмена открывала бы к заведению новое.
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCancellationResponseBytes))
	if err != nil {
		return application.RestaurantUnavailableError{TimedOut: timedOut(err), Err: err}
	}
	_, _ = io.Copy(io.Discard, response.Body)

	// `200` — заведение отменило заказ либо уже считало его отменённым: контракт обещает
	// на повтор тот же ответ, и различать их площадке незачем.
	if response.StatusCode == http.StatusOK {
		return nil
	}

	return fmt.Errorf("заведение ответило на отмену заказа %s %d: %s",
		order.ID, response.StatusCode, body)
}
