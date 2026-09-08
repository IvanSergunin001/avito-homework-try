package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/restaurant/application"
	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
)

// orderColumns — поля заказа в том порядке, в каком их читает scanOrder.
const orderColumns = `id, platform_order_id, idempotency_key, status, address, items, total,
	cancellation_reason, placed_at, accepted_at, updated_at`

// Orders — входящие заказы коробки в схеме `restaurant`.
//
// Хранилище одно на приём заказа и на рабочее место оператора: заказ, который приняла
// коробка, и заказ, который разбирает оператор, — один и тот же заказ.
type Orders struct {
	db *sql.DB
}

// NewOrders собирает хранилище заказов поверх подключения к базе коробки.
func NewOrders(db *sql.DB) *Orders {
	return &Orders{db: db}
}

// Accept принимает заказ одной транзакцией: читает нужные позиции меню под блокировкой,
// отдаёт их решению предметной области и, если заказ принят, списывает остатки и
// записывает заказ.
//
// Всё это в одной транзакции не ради скорости, а потому, что иначе принятый заказ мог бы
// оказаться невыполнимым: между чтением меню и списанием остатка помещается и правка
// оператора, и второй заказ на ту же последнюю порцию. Блокировка снимается коммитом,
// поэтому конкурирующий приём ждёт решения по первому заказу, а не гадает вместе с ним.
func (o *Orders) Accept(
	ctx context.Context,
	request domain.OrderRequest,
	decide func(menu map[string]domain.MenuItem) (domain.Order, error),
) (domain.Order, bool, error) {
	// Повтор распознаётся до всякой работы: заказ с этим ключом коробка уже приняла,
	// и второй раз ни остаток списывать, ни готовить нечего.
	accepted, err := scanOrder(o.db.QueryRowContext(ctx,
		`SELECT `+orderColumns+` FROM restaurant.orders WHERE idempotency_key = $1`,
		request.IdempotencyKey))
	switch {
	case err == nil:
		return accepted, false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return domain.Order{}, false, fmt.Errorf("найти заказ по ключу идемпотентности: %w", err)
	}

	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Order{}, false, fmt.Errorf("начать приём заказа: %w", err)
	}
	// Откат после успешного коммита — пустая операция, поэтому отдельного флага «уже
	// закоммичено» не нужно: сорванный по дороге вызов откатывает транзакцию сам.
	defer func() { _ = tx.Rollback() }()

	menu, err := lockRequestedItems(ctx, tx, request.ExternalIDs())
	if err != nil {
		return domain.Order{}, false, err
	}

	order, err := decide(menu)
	if err != nil {
		return domain.Order{}, false, err
	}

	if err := takeFromStock(ctx, tx, order); err != nil {
		return domain.Order{}, false, err
	}

	inserted, err := insertOrder(ctx, tx, order)
	if err != nil {
		return domain.Order{}, false, err
	}
	if !inserted {
		// Ту же доставку приняла соседняя транзакция, пока эта считала. Списание
		// остатка уходит вместе с откатом, а площадке отвечает первая попытка.
		return o.acceptedEarlier(ctx, request.IdempotencyKey)
	}

	if err := tx.Commit(); err != nil {
		return domain.Order{}, false, fmt.Errorf("принять заказ: %w", err)
	}

	return order, true, nil
}

// Page отдаёт окно списка заказов и общее их число.
func (o *Orders) Page(ctx context.Context, window application.Window) (application.OrderPage, error) {
	// Общее число считается отдельным запросом, а не оконной функцией поверх страницы:
	// страница за концом выдачи не содержит ни одной строки, и вместе со строками
	// потерялся бы и счётчик — оператор увидел бы «всего 0» там, где заказы есть.
	var total int64
	countRow := o.db.QueryRowContext(ctx, `SELECT count(*) FROM restaurant.orders`)
	if err := countRow.Scan(&total); err != nil {
		return application.OrderPage{}, fmt.Errorf("сосчитать заказы: %w", err)
	}

	rows, err := o.db.QueryContext(ctx, `
		SELECT `+orderColumns+`
		FROM restaurant.orders
		ORDER BY accepted_at DESC, id DESC
		LIMIT $1 OFFSET $2`, window.Limit, window.Offset)
	if err != nil {
		return application.OrderPage{}, fmt.Errorf("прочитать заказы: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Пустая страница — это пустой список, а не отсутствие списка: поверхность обещает
	// коллекцию всегда, и различать два вида пустоты читающему незачем.
	orders := make([]domain.Order, 0, window.Limit)
	for rows.Next() {
		order, err := scanOrder(rows)
		if err != nil {
			return application.OrderPage{}, fmt.Errorf("прочитать заказ: %w", err)
		}
		orders = append(orders, order)
	}
	if err := rows.Err(); err != nil {
		return application.OrderPage{}, fmt.Errorf("перебрать заказы: %w", err)
	}

	return application.OrderPage{Items: orders, Total: total}, nil
}

// Advance применяет к заказу решение оператора двигать его дальше по пути.
func (o *Orders) Advance(
	ctx context.Context, id uuid.UUID, decide func(order *domain.Order) error,
) (domain.Order, error) {
	return o.review(ctx, id, decide, keepStock)
}

// InWork перечисляет заказы, которые ещё не пришли в конечное состояние.
//
// Отдаются одни идентификаторы, а не заказы целиком: продвигающий их читает каждый заказ
// заново под блокировкой — состояние, прочитанное списком, к моменту решения уже могло
// смениться на соседнем рабочем месте.
func (o *Orders) InWork(ctx context.Context, statuses []domain.OrderStatus) ([]uuid.UUID, error) {
	names := make([]string, 0, len(statuses))
	for _, status := range statuses {
		names = append(names, string(status))
	}

	rows, err := o.db.QueryContext(ctx, `
		SELECT id
		FROM restaurant.orders
		WHERE status = ANY($1::text[])
		ORDER BY accepted_at, id`, names)
	if err != nil {
		return nil, fmt.Errorf("перечислить заказы в работе: %w", err)
	}
	defer func() { _ = rows.Close() }()

	ids := make([]uuid.UUID, 0)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("прочитать заказ в работе: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("перебрать заказы в работе: %w", err)
	}

	return ids, nil
}

// Reject применяет к заказу отказ оператора.
func (o *Orders) Reject(
	ctx context.Context, id uuid.UUID, decide func(order *domain.Order) error,
) (domain.Order, error) {
	return o.review(ctx, id, decide, restoreStock)
}

// Cancel отменяет заказ, о котором просит площадка.
//
// Заказ ищется по идентификатору площадки: собственная нумерация коробки ей неизвестна,
// и в партнёрском контракте заказ называется тем идентификатором, которым его передавали.
//
// Решение принимает предметная область, и её ответ «ничего не изменилось» — это не
// пустая запись, а повтор просьбы: заказ уже отменён, и записывать по второму разу
// нечего, а вернуть остатки второй раз означало бы выдумать продукты, которых нет.
func (o *Orders) Cancel(
	ctx context.Context, platformOrderID uuid.UUID, decide func(order *domain.Order) (bool, error),
) (domain.Order, error) {
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Order{}, fmt.Errorf("начать отмену заказа %s: %w", platformOrderID, err)
	}
	defer func() { _ = tx.Rollback() }()

	// Заказ читается под блокировкой по той же причине, что и при решении оператора:
	// между чтением и записью помещается нажатие «в доставку» с рабочего места, и без
	// блокировки отмена и продвижение перезаписали бы друг друга.
	order, err := scanOrder(tx.QueryRowContext(ctx,
		`SELECT `+orderColumns+` FROM restaurant.orders WHERE platform_order_id = $1 FOR UPDATE`,
		platformOrderID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.Order{}, domain.ErrOrderNotFound
	case err != nil:
		return domain.Order{}, fmt.Errorf("прочитать заказ %s: %w", platformOrderID, err)
	}

	cancelled, err := decide(&order)
	if err != nil {
		return domain.Order{}, err
	}
	if !cancelled {
		return order, nil
	}

	// Момент отмены ставит база — тем же `now()`, что и всякую смену состояния в коробке,
	// — и тут же отдаётся обратно: он уезжает площадке в подтверждении отмены, и брать
	// его с часов службы значило бы обещать ей время, отличное от записанного.
	err = tx.QueryRowContext(ctx, `
		UPDATE restaurant.orders
		SET status = $2, cancellation_reason = $3, updated_at = now()
		WHERE id = $1
		RETURNING updated_at`, order.ID, string(order.Status), order.CancellationReason).
		Scan(&order.UpdatedAt)
	if err != nil {
		return domain.Order{}, fmt.Errorf("записать отмену заказа %s: %w", order.ID, err)
	}

	// Продукты на отменённый заказ не потрачены — отменить его коробка соглашается
	// только до начала готовки, — и позиции снова продаются.
	if err := restoreStock(ctx, tx, order); err != nil {
		return domain.Order{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.Order{}, fmt.Errorf("отменить заказ %s: %w", order.ID, err)
	}

	return order, nil
}

// review применяет решение оператора к заказу: читает заказ под блокировкой, отдаёт его
// предметной области и записывает то, что та решила.
//
// Блокировка нужна затем же, зачем и при приёме: между чтением заказа и записью решения
// помещается решение оператора с соседнего рабочего места, и без неё второй перезаписал
// бы первого, не узнав, что заказ уже разобрали.
//
// settle распоряжается остатками: за подтверждённый заказ они уже списаны, а отклонённый
// их возвращает.
//
// Той же транзакцией уведомление о новом состоянии кладётся в очередь площадке. Не
// «после записи, отдельным шагом»: между записью и отдельным шагом помещается падение
// службы, и заказ уехал бы в доставку, о которой площадка не узнает никогда. Одной
// транзакцией — значит, либо статус сменился и уведомление поставлено в очередь, либо не
// произошло ни того, ни другого.
func (o *Orders) review(
	ctx context.Context,
	id uuid.UUID,
	decide func(order *domain.Order) error,
	settle func(ctx context.Context, tx *sql.Tx, order domain.Order) error,
) (domain.Order, error) {
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Order{}, fmt.Errorf("начать рассмотрение заказа %s: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	order, err := scanOrder(tx.QueryRowContext(ctx,
		`SELECT `+orderColumns+` FROM restaurant.orders WHERE id = $1 FOR UPDATE`, id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.Order{}, domain.ErrOrderNotFound
	case err != nil:
		return domain.Order{}, fmt.Errorf("прочитать заказ %s: %w", id, err)
	}

	if err := decide(&order); err != nil {
		return domain.Order{}, err
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE restaurant.orders
		SET status = $2, cancellation_reason = $3, updated_at = now()
		WHERE id = $1`, order.ID, string(order.Status), order.CancellationReason)
	if err != nil {
		return domain.Order{}, fmt.Errorf("записать решение по заказу %s: %w", id, err)
	}

	if err := settle(ctx, tx, order); err != nil {
		return domain.Order{}, err
	}

	if err := enqueueNotification(ctx, tx, order); err != nil {
		return domain.Order{}, err
	}

	if err := tx.Commit(); err != nil {
		return domain.Order{}, fmt.Errorf("применить решение по заказу %s: %w", id, err)
	}

	return order, nil
}

// enqueueNotification кладёт уведомление о новом состоянии заказа в очередь площадке.
//
// Время перехода ставит база, а не вызывающий: `now()` внутри транзакции — это момент её
// начала, то есть ровно то время, которым отмечена сама смена статуса. Ехать уведомление
// может сразу: пауза появляется только у того, которое не доехало.
//
// Повтор отбивается ключом «заказ + статус»: в каждое состояние заказ приходит один раз,
// и вторая строка о том же переходе означала бы второе уведомление о нём же.
func enqueueNotification(ctx context.Context, tx *sql.Tx, order domain.Order) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO restaurant.status_notifications
			(order_id, status, reason, occurred_at, next_attempt_at)
		VALUES ($1, $2, $3, now(), now())
		ON CONFLICT (order_id, status) DO NOTHING`,
		order.ID, string(order.Status), order.CancellationReason)
	if err != nil {
		return fmt.Errorf("поставить уведомление о заказе %s в очередь: %w", order.ID, err)
	}

	return nil
}

// acceptedEarlier читает заказ, который под этим ключом приняли раньше.
func (o *Orders) acceptedEarlier(ctx context.Context, key uuid.UUID) (domain.Order, bool, error) {
	accepted, err := scanOrder(o.db.QueryRowContext(ctx,
		`SELECT `+orderColumns+` FROM restaurant.orders WHERE idempotency_key = $1`, key))
	if err != nil {
		return domain.Order{}, false, fmt.Errorf("прочитать ранее принятый заказ: %w", err)
	}

	return accepted, false, nil
}

// lockRequestedItems читает позиции меню, о которых спрашивает заказ, и держит их до
// конца транзакции.
//
// Позиции берутся в порядке идентификаторов: два заказа на одни и те же позиции, взятые в
// разном порядке, дождались бы друг друга насмерть.
//
// Внешний идентификатор коробки — это идентификатор позиции меню, поэтому строка, которая
// не является uuid, заведомо неизвестна: в выдачу она не попадает, и предметная область
// назовёт её неизвестной позицией.
func lockRequestedItems(
	ctx context.Context, tx *sql.Tx, externalIDs []string,
) (map[string]domain.MenuItem, error) {
	known := make([]string, 0, len(externalIDs))
	for _, externalID := range externalIDs {
		if id, err := uuid.Parse(externalID); err == nil {
			known = append(known, id.String())
		}
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT `+menuItemColumns+`
		FROM restaurant.menu_items
		WHERE id = ANY($1::uuid[])
		ORDER BY id
		FOR UPDATE`, known)
	if err != nil {
		return nil, fmt.Errorf("прочитать позиции заказа: %w", err)
	}
	defer func() { _ = rows.Close() }()

	menu := make(map[string]domain.MenuItem, len(known))
	for rows.Next() {
		item, err := scanMenuItem(rows)
		if err != nil {
			return nil, fmt.Errorf("прочитать позицию заказа: %w", err)
		}
		menu[item.ExternalID()] = item
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("перебрать позиции заказа: %w", err)
	}

	return menu, nil
}

// takeFromStock списывает остатки под принятый заказ.
//
// Одним запросом на весь состав, а не запросом на позицию: заказ из сотни позиций иначе
// означал бы сотню обращений к базе внутри транзакции, которая держит блокировки меню.
// Что остатка хватает, уже решила предметная область по тем же строкам, которые заперты
// этой транзакцией, — здесь остаётся только вычесть.
func takeFromStock(ctx context.Context, tx *sql.Tx, order domain.Order) error {
	ids, quantities := orderedQuantities(order)

	_, err := tx.ExecContext(ctx, `
		UPDATE restaurant.menu_items AS item
		SET stock = item.stock - ordered.quantity
		FROM unnest($1::uuid[], $2::int[]) AS ordered(id, quantity)
		WHERE item.id = ordered.id`, ids, quantities)
	if err != nil {
		return fmt.Errorf("списать остатки по заказу %s: %w", order.ID, err)
	}

	return nil
}

// restoreStock возвращает в меню остатки, списанные под заказ: продукты на отклонённый
// заказ не потрачены, и позиция снова продаётся. Иначе отказ оператора съедал бы остаток
// молча, а вернуть его пришлось бы вручную.
//
// Позиция, которую с тех пор удалили из меню, ничего не получает: возвращать остаток
// некуда, и заводить её заново отказ от заказа не должен.
func restoreStock(ctx context.Context, tx *sql.Tx, order domain.Order) error {
	ids, quantities := orderedQuantities(order)

	_, err := tx.ExecContext(ctx, `
		UPDATE restaurant.menu_items AS item
		SET stock = item.stock + ordered.quantity
		FROM unnest($1::uuid[], $2::int[]) AS ordered(id, quantity)
		WHERE item.id = ordered.id`, ids, quantities)
	if err != nil {
		return fmt.Errorf("вернуть остатки по заказу %s: %w", order.ID, err)
	}

	return nil
}

// keepStock оставляет остатки как есть: за подтверждённый заказ они списаны при приёме, и
// заказ в работе их и потратит.
func keepStock(context.Context, *sql.Tx, domain.Order) error {
	return nil
}

// orderedQuantities раскладывает состав заказа по колонкам: базе он уезжает двумя
// массивами, а не строкой за строкой.
func orderedQuantities(order domain.Order) (ids []string, quantities []int) {
	ids = make([]string, 0, len(order.Items))
	quantities = make([]int, 0, len(order.Items))
	for _, item := range order.Items {
		ids = append(ids, item.MenuItemID.String())
		quantities = append(quantities, item.Quantity)
	}

	return ids, quantities
}

// insertOrder записывает принятый заказ. Ложь в первом результате означает, что заказ с
// этим ключом идемпотентности уже принят: второй не заводится.
//
// Повтор отбивается ограничением базы, а не проверкой перед вставкой: между проверкой и
// вставкой помещается вторая доставка того же заказа, и оба вызова прошли бы проверку.
func insertOrder(ctx context.Context, tx *sql.Tx, order domain.Order) (bool, error) {
	items, err := json.Marshal(storedItems(order.Items))
	if err != nil {
		return false, fmt.Errorf("собрать состав заказа %s: %w", order.ID, err)
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO restaurant.orders
			(id, platform_order_id, idempotency_key, status, address, items, total,
			 cancellation_reason, placed_at, accepted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (idempotency_key) DO NOTHING`,
		order.ID, order.PlatformOrderID, order.IdempotencyKey, string(order.Status),
		order.Address, items, int64(order.Total), order.CancellationReason,
		placedAt(order.PlacedAt), order.AcceptedAt)
	if err != nil {
		return false, fmt.Errorf("записать заказ %s: %w", order.ID, err)
	}

	written, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("записать заказ %s: %w", order.ID, err)
	}

	return written > 0, nil
}

// storedItem — позиция заказа в том виде, в каком она лежит в базе.
//
// Отдельный тип, а не сам domain.OrderItem: формат хранения не должен меняться от того,
// что кто-то переименовал поле в предметной области, — прочитать после этого уже
// записанные заказы было бы нечем.
type storedItem struct {
	MenuItemID uuid.UUID `json:"menu_item_id"`
	Name       string    `json:"name"`
	Price      int64     `json:"price"`
	Quantity   int       `json:"quantity"`
}

// storedItems переводит состав заказа в формат хранения.
func storedItems(items []domain.OrderItem) []storedItem {
	stored := make([]storedItem, 0, len(items))
	for _, item := range items {
		stored = append(stored, storedItem{
			MenuItemID: item.MenuItemID,
			Name:       item.Name,
			Price:      int64(item.Price),
			Quantity:   item.Quantity,
		})
	}

	return stored
}

// placedAt переводит время оформления заказа в значение колонки: его могло не быть —
// площадка не обязана его присылать.
func placedAt(moment time.Time) sql.NullTime {
	return sql.NullTime{Time: moment, Valid: !moment.IsZero()}
}

// scanOrder читает заказ из строки результата.
func scanOrder(row rowScanner) (domain.Order, error) {
	var (
		order  domain.Order
		status string
		items  []byte
		placed sql.NullTime
		total  int64
	)

	err := row.Scan(&order.ID, &order.PlatformOrderID, &order.IdempotencyKey, &status,
		&order.Address, &items, &total, &order.CancellationReason, &placed,
		&order.AcceptedAt, &order.UpdatedAt)
	if err != nil {
		return domain.Order{}, err
	}

	var stored []storedItem
	if err := json.Unmarshal(items, &stored); err != nil {
		return domain.Order{}, fmt.Errorf("разобрать состав заказа %s: %w", order.ID, err)
	}

	order.Status = domain.OrderStatus(status)
	order.Total = domain.Money(total)
	order.PlacedAt = placed.Time
	order.Items = make([]domain.OrderItem, 0, len(stored))
	for _, item := range stored {
		order.Items = append(order.Items, domain.OrderItem{
			MenuItemID: item.MenuItemID,
			Name:       item.Name,
			Price:      domain.Money(item.Price),
			Quantity:   item.Quantity,
		})
	}

	return order, nil
}
