// Меню заведения глазами площадки: та половина партнёрского контракта, которую площадка
// вызывает, а не реализует. Сам вызов собран сгенерированным по контракту построителем
// запроса, а вот ответ разбирается здесь руками — и это осознанно.
//
// Сгенерированный разбор читает тело целиком в память и только потом смотрит, что в нём:
// на ответе стороннего заведения это означает, что защита от него включается после того,
// как он уже прочитан. Поэтому ответ читается потоково и с потолком: площадка перестаёт
// читать заведение, которое отдаёт больше обещанного, а не узнаёт об этом задним числом.

package restaurant_client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	partner "github.com/IvanSergunin001/Test157/api/partner/v1"
	"github.com/IvanSergunin001/Test157/internal/platform/application"
	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

const (
	// maxMenuResponseBytes — потолок на размер одного ответа заведения: 20 МБ.
	//
	// Меню в контракте страничное, и страница в 500 позиций до такого размера не
	// дотягивает на порядки. Упереться в потолок может только заведение, нарушающее
	// контракт, — намеренно или из-за собственной аварии; читать его до конца площадка не
	// обязана и не станет: тело неограниченного размера читается в память до того, как
	// хоть одна проверка успеет сказать, что ответ негоден.
	maxMenuResponseBytes = 20 << 20

	// partnerContractPath — префикс партнёрского контракта у заведения. Завершающий слэш в
	// адресе сервера обязателен: без него сгенерированный построитель разрешит
	// относительный путь операции относительно родительского каталога, и запрос уйдёт
	// не туда.
	partnerContractPath = "/api/kitchen/v1/restaurant/"
)

// errMenuTooLarge — ответ заведения перестал помещаться в потолок. Ошибка возникает
// внутри чтения тела и всплывает через разбор, поэтому она сравнимая, а не текстовая:
// разбор сообщит о неожиданном конце ответа, а причина у него именно эта.
var errMenuTooLarge = errors.New("menu response too large")

// MenuSource забирает меню заведения по партнёрскому контракту.
//
// Клиент свой, а не http.DefaultClient: у того нет таймаута вовсе, и заведение, которое
// приняло соединение и замолчало, держало бы синхронизацию площадки бесконечно.
type MenuSource struct {
	client *http.Client
}

// NewMenuSource собирает источник меню с таймаутом на запрос к заведению.
//
// Таймаут задаётся конфигурацией площадки, а не выводится из чего-либо: сколько ждать
// стороннюю систему — решение эксплуатации, и меняется оно без пересборки службы.
func NewMenuSource(timeout time.Duration) *MenuSource {
	return &MenuSource{client: &http.Client{Timeout: timeout}}
}

// Page забирает страницу меню заведения, живущего по адресу baseURL.
//
// Ограничений три, и каждое закрывает свою беду: таймаут — заведение, которое не отвечает;
// потолок размера — заведение, которое отвечает бесконечно; потолок числа позиций —
// заведение, которое отдало не ту страницу, о которой его просили. Все три срабатывают до
// того, как хоть одна позиция доедет до каталога.
func (s *MenuSource) Page(
	ctx context.Context, baseURL string, page domain.Page,
) (application.MenuBatch, error) {
	limit, offset := page.Limit, page.Offset

	request, err := partner.NewListMenuRequest(strings.TrimRight(baseURL, "/")+partnerContractPath,
		&partner.ListMenuParams{Limit: &limit, Offset: &offset})
	if err != nil {
		return application.MenuBatch{}, fmt.Errorf("собрать запрос меню: %w", err)
	}

	response, err := s.client.Do(request.WithContext(ctx))
	if err != nil {
		return application.MenuBatch{}, fmt.Errorf("запросить меню: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		// Тело отказа наружу не идёт: чем именно недовольно заведение, решать не площадке,
		// а последствие у любого отказа одно — синхронизация отменяется целиком.
		return application.MenuBatch{}, fmt.Errorf("заведение ответило на запрос меню %d", response.StatusCode)
	}

	batch, err := decodeMenuPage(&cappedReader{body: response.Body, left: maxMenuResponseBytes + 1}, page.Limit)
	if err != nil {
		if errors.Is(err, errMenuTooLarge) {
			return application.MenuBatch{}, domain.BrokenMenuError{
				Reason: fmt.Sprintf("ответ с меню больше %d байт", maxMenuResponseBytes),
			}
		}

		return application.MenuBatch{}, err
	}

	return batch, nil
}

// decodeMenuPage разбирает страницу меню потоково: позиция за позицией, не собирая ответ
// целиком ни в память, ни в промежуточное дерево.
//
// Разбор по токенам нужен не ради экономии: он позволяет прекратить чтение на позиции,
// которая перевалила за потолок, а не после того, как весь ответ уже прочитан. Неизвестные
// поля пропускаются — заведение, реализовавшее контракт новее нашего, ломаться о поле, о
// котором площадка не знает, не должно.
func decodeMenuPage(body io.Reader, maxItems int) (application.MenuBatch, error) {
	decoder := json.NewDecoder(body)

	if err := expectDelimiter(decoder, '{'); err != nil {
		return application.MenuBatch{}, err
	}

	var batch application.MenuBatch

	for decoder.More() {
		field, err := decoder.Token()
		if err != nil {
			return application.MenuBatch{}, fmt.Errorf("разобрать меню: %w", err)
		}

		switch field {
		case "items":
			batch.Items, err = decodeMenuItems(decoder, maxItems)
		case "page":
			var meta partner.PageMeta
			if err = decoder.Decode(&meta); err == nil {
				batch.Total = meta.Total
			}
		default:
			var ignored json.RawMessage
			err = decoder.Decode(&ignored)
		}

		if err != nil {
			return application.MenuBatch{}, err
		}
	}

	return batch, nil
}

// decodeMenuItems читает массив позиций, проверяя каждую предметной областью на месте.
//
// Позиция, не прошедшая проверку, прекращает разбор, а не выбрасывается из выдачи:
// заведение, приславшее позицию без названия, ошиблось в выгрузке целиком, и «почти всё
// меню» на витрине хуже прежнего.
func decodeMenuItems(decoder *json.Decoder, maxItems int) ([]domain.CatalogItem, error) {
	if err := expectDelimiter(decoder, '['); err != nil {
		return nil, err
	}

	items := make([]domain.CatalogItem, 0, maxItems)

	for decoder.More() {
		var arrived partner.MenuItem
		if err := decoder.Decode(&arrived); err != nil {
			return nil, fmt.Errorf("разобрать позицию меню: %w", err)
		}

		if len(items) == maxItems {
			return nil, domain.BrokenMenuError{
				Reason: fmt.Sprintf("на странице меню больше %d позиций, чем просила площадка", maxItems),
			}
		}

		item, err := domain.NewCatalogItem(arrived.ExternalId, arrived.Name,
			describe(arrived.Description), domain.Money(arrived.Price), arrived.IsAvailable)
		if err != nil {
			return nil, err
		}

		items = append(items, item)
	}

	return items, expectDelimiter(decoder, ']')
}

// expectDelimiter читает следующий токен и убеждается, что это ожидаемая скобка: ответ,
// устроенный иначе, разбирать нечем.
func expectDelimiter(decoder *json.Decoder, want json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("разобрать меню: %w", err)
	}
	if token != want {
		return fmt.Errorf("разобрать меню: ожидали %q, получили %v", want, token)
	}

	return nil
}

// describe приводит необязательное описание к строке: отсутствие описания и пустое
// описание для площадки одно и то же — показывать в карточке нечего.
func describe(description *string) string {
	if description == nil {
		return ""
	}

	return *description
}

// cappedReader — тело ответа, которое нельзя читать дальше объявленного предела.
//
// Предел стоит на чтении, а не на объявленной длине ответа: `Content-Length` может
// отсутствовать вовсе (chunked) либо врать, и доверять ему значило бы не иметь потолка
// там, где он единственно и нужен.
type cappedReader struct {
	body io.Reader
	// left — сколько байт ещё разрешено прочитать. Заводится на байт больше предела:
	// ответу ровно в предел нужно ещё одно чтение, чтобы разбор увидел конец тела, а не
	// упёрся в потолок на последнем байте.
	left int64
}

// Read отдаёт очередной кусок тела, пока не исчерпан остаток предела.
func (r *cappedReader) Read(p []byte) (int, error) {
	if r.left <= 0 {
		return 0, errMenuTooLarge
	}

	if int64(len(p)) > r.left {
		p = p[:r.left]
	}

	read, err := r.body.Read(p)
	r.left -= int64(read)

	return read, err
}
