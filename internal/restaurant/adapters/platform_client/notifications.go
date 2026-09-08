// Package platform_client — та половина партнёрского контракта, которую коробка вызывает,
// а не реализует: уведомление площадки о смене статуса заказа.
//
// Запрос собирается сгенерированным по контракту построителем — путь операции и схема тела
// берутся из спецификации, а не пишутся здесь руками. Принято уведомление или нет, видно
// по коду ответа: тело площадки коробке не нужно — в нём действующий статус заказа, а
// решать по нему коробке нечего, её собственный статус от чужого ответа не меняется.
package platform_client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
)

const (
	// platformPartnerPath — префикс партнёрской поверхности площадки. Завершающий слэш в
	// адресе сервера обязателен: без него сгенерированный построитель разрешит
	// относительный путь операции относительно родительского каталога, и запрос уйдёт
	// не туда.
	platformPartnerPath = "/api/partner/v1/"

	// maxAnswerBytes — сколько байт ответа площадки коробка готова прочитать.
	//
	// Читается он не ради разбора, а ради двух вещей: соединение, закрытое недочитанным,
	// не возвращается в пул — и каждое уведомление открывало бы к площадке новое; а текст
	// отказа попадает в очередь и в лог, и по нему человек понимает, почему статус не
	// доехал. Нескольких килобайт хватает и на то, и на другое.
	maxAnswerBytes = 4 << 10
)

// StatusNotifier доставляет площадке уведомления о смене статуса заказа.
//
// Клиент свой, а не http.DefaultClient: у того нет таймаута вовсе, и площадка, принявшая
// соединение и замолчавшая, держала бы проход доставки бесконечно — вместе со всеми
// уведомлениями, которые ждут за этим.
type StatusNotifier struct {
	client     *http.Client
	baseURL    string
	partnerKey string
}

// NewStatusNotifier собирает доставку уведомлений площадке, живущей по адресу baseURL.
//
// partnerKey — партнёрский ключ, выданный заведению при онбординге: партнёрские операции
// площадки закрыты им, и уведомление без ключа площадка не примет. Ключ приходит
// конфигурацией: заведение получает его от площадки, а не выдумывает себе само.
func NewStatusNotifier(baseURL, partnerKey string, timeout time.Duration) *StatusNotifier {
	return &StatusNotifier{
		client:     &http.Client{Timeout: timeout},
		baseURL:    baseURL,
		partnerKey: partnerKey,
	}
}

// Notify доставляет площадке одно уведомление.
//
// Уведомление адресуется идентификатором заказа на площадке, а не идентификатором коробки:
// заказ у каждой стороны свой, и своим номером коробка рассказала бы площадке о заказе,
// которого та не знает.
func (n *StatusNotifier) Notify(ctx context.Context, notification domain.PendingNotification) error {
	status, known := notifiedStatus(notification.Status)
	if !known {
		return fmt.Errorf("состояние %q не входит в перечень уведомляемых партнёрским контрактом",
			notification.Status)
	}

	body := StatusNotification{Status: status, OccurredAt: notification.OccurredAt}
	// Причина есть только у отмены, и присылается она только с ней: пустая строка
	// сообщила бы площадке, что заказ отменили, не объяснив причины, — а её показывают
	// покупателю.
	if notification.Reason != "" {
		reason := notification.Reason
		body.Reason = &reason
	}

	request, err := NewNotifyOrderStatusRequest(
		strings.TrimRight(n.baseURL, "/")+platformPartnerPath, notification.PlatformOrderID, body)
	if err != nil {
		return fmt.Errorf("собрать уведомление о заказе %s: %w", notification.PlatformOrderID, err)
	}
	request.Header.Set("Authorization", "Bearer "+n.partnerKey)

	response, err := n.client.Do(request.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("уведомить площадку о заказе %s: %w", notification.PlatformOrderID, err)
	}
	defer func() { _ = response.Body.Close() }()

	answer, _ := io.ReadAll(io.LimitReader(response.Body, maxAnswerBytes))
	_, _ = io.Copy(io.Discard, response.Body)

	// Принято уведомление или нет, видно по коду ответа. Любой другой ответ — повод
	// повторить: площадка могла перезапускаться, а могла и отказать по существу, и
	// различать эти случаи коробке нечем — зато повтор безвреден, приём уведомлений
	// идемпотентен по контракту.
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("площадка ответила на уведомление %d: %s",
			response.StatusCode, strings.TrimSpace(string(answer)))
	}

	return nil
}

// notifiedStatus переводит состояние заказа в значение партнёрского контракта.
//
// Перевод явный, а не приведением строки: перечень уведомляемых состояний в контракте
// закрыт, и состояния, которого в нём нет, площадка не ждёт. `under_review` в него не
// входит — его площадка ставит себе сама, приняв синхронный ответ на переданный заказ.
func notifiedStatus(status domain.OrderStatus) (NotifiedStatus, bool) {
	switch status {
	case domain.Confirmed:
		return NotifiedStatusConfirmed, true
	case domain.Cooking:
		return NotifiedStatusCooking, true
	case domain.Delivering:
		return NotifiedStatusDelivering, true
	case domain.Completed:
		return NotifiedStatusCompleted, true
	case domain.Cancelled:
		return NotifiedStatusCancelled, true
	default:
		return "", false
	}
}
