// Передача заказа заведению: вторая половина той части партнёрского контракта, которую
// площадка вызывает, а не реализует.
//
// Запрос собирается сгенерированным по контракту построителем — заголовки идемпотентности
// и путь операции берутся из спецификации, а не пишутся здесь руками. Ответ разбирать
// нечем: заведение подтверждает им то, что площадка и так знает, — принят ли заказ, видно
// по коду ответа.

package restaurant_client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/IvanSergunin001/Test157/internal/platform/application"
	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// maxAcceptanceResponseBytes — сколько байт ответа заведения площадка готова прочитать,
// прежде чем закрыть соединение.
//
// Подтверждение приёма — это идентификатор заказа и время: килобайта хватает с запасом.
// Читается оно только затем, чтобы соединение вернулось в пул переиспользуемых, а не
// обрывалось после каждого заказа; заведение, отдающее в ответ мегабайты, площадку это
// делать не заставит.
const maxAcceptanceResponseBytes = 1 << 10

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
	request, err := NewAcceptOrderRequest(
		strings.TrimRight(baseURL, "/")+partnerContractPath,
		&AcceptOrderParams{IdempotencyKey: order.ID, IdempotencyAttempt: attempt},
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

	// Тело успешного ответа площадке не нужно, но прочитать его стоит: закрытое
	// недочитанным соединение в пул не возвращается, и каждый заказ открывал бы к
	// заведению новое.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxAcceptanceResponseBytes))

	// Принят заказ или нет, видно по коду ответа: `201` — заказ заведён, `200` — заведение
	// уже принимало его раньше и отвечает результатом первой попытки.
	//
	// Всё остальное для площадки одинаково: заказ не принят, и покупатель узнаёт об этом
	// сразу. Разбор машиночитаемых причин отказа — «позиция кончилась», «цена
	// изменилась» — это следующий слайс; заводить его половину здесь значило бы показать
	// покупателю причину, по которой он ничего не может сделать.
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return application.RestaurantUnavailableError{
			Err: fmt.Errorf("заведение ответило на переданный заказ %d", response.StatusCode),
		}
	}

	return nil
}

// incomingOrder переводит заказ в схему партнёрского контракта.
//
// Цена уезжает тем же целым числом копеек, каким хранится: перевод в дробное число по
// дороге означал бы, что у площадки и у заведения разные деньги. Позиция называется
// внешним идентификатором — тем, которым её знает само заведение: собственный
// идентификатор товара выдала площадка, и заведению он ничего не говорит.
func incomingOrder(order domain.Order) IncomingOrder {
	items := make([]IncomingOrderItem, 0, len(order.Items))
	for _, item := range order.Items {
		items = append(items, IncomingOrderItem{
			ExternalId: item.ExternalID,
			Quantity:   item.Quantity,
			Price:      int64(item.Price),
		})
	}

	// Время оформления заведению не обязательно, но полезно: по нему оператор видит, как
	// давно покупатель ждёт, — и видит это независимо от того, когда заказ доехал.
	placedAt := order.CreatedAt

	return IncomingOrder{
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
