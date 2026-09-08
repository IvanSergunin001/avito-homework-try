package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/IvanSergunin001/Test157/internal/platform/domain"
)

// orderColumns — поля заказа в том порядке, в каком их читает scanOrder.
const orderColumns = `id, customer_id, restaurant_id, status, address, items, total, request_hash,
	cancelled_by, cancellation_reason, created_at, updated_at`

// Orders — заказы покупателей в схеме `platform`.
type Orders struct {
	db *sql.DB
}

// NewOrders собирает хранилище заказов поверх подключения к базе площадки.
func NewOrders(db *sql.DB) *Orders {
	return &Orders{db: db}
}

// Add записывает оформленный заказ, а если заказ с таким идентификатором уже есть —
// отдаёт записанный.
//
// Занятый идентификатор отбивает сама вставка, а не проверка «нет ли уже такого» перед
// ней: между проверкой и вставкой помещается второе нажатие «Заказать», и обе попытки
// прошли бы проверку. Отдельной таблицы ключей идемпотентности здесь нет — ключ это
// первичный ключ заказа, и уникальность строки и есть вся защита от повтора.
//
// Уже записанный заказ читается целиком и без отбора по покупателю: чей он и тем ли
// запросом оформлен, решает предметная область, а запрос, который отобрал бы «только
// свои», не отличил бы занятый чужой идентификатор от свободного.
func (o *Orders) Add(ctx context.Context, order domain.Order) (domain.Order, bool, error) {
	items, err := json.Marshal(storedItems(order.Items))
	if err != nil {
		return domain.Order{}, false, fmt.Errorf("собрать состав заказа %s: %w", order.ID, err)
	}

	// Заказ и первая запись его журнала появляются одной транзакцией: заказ без строки
	// «оформлен» в журнале — это заказ, история которого начинается с середины, и
	// восстановить её потом неоткуда.
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Order{}, false, fmt.Errorf("начать запись заказа %s: %w", order.ID, err)
	}
	// Откат после успешного коммита — пустая операция, поэтому отдельного флага «уже
	// закоммичено» не нужно: сорванный по дороге вызов откатывает транзакцию сам.
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		INSERT INTO platform.orders
			(id, customer_id, restaurant_id, status, address, items, total, request_hash,
			 cancelled_by, cancellation_reason, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (id) DO NOTHING`,
		order.ID, order.CustomerID, order.RestaurantID, string(order.Status), order.Address,
		items, int64(order.Total), order.RequestHash, string(order.CancelledBy), order.CancellationReason,
		order.CreatedAt, order.UpdatedAt)
	if err != nil {
		return domain.Order{}, false, fmt.Errorf("записать заказ %s: %w", order.ID, err)
	}

	placed, err := result.RowsAffected()
	if err != nil {
		return domain.Order{}, false, fmt.Errorf("записать заказ %s: %w", order.ID, err)
	}
	if placed == 1 {
		if err := recordStatusChanges(ctx, tx, order); err != nil {
			return domain.Order{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return domain.Order{}, false, fmt.Errorf("записать заказ %s: %w", order.ID, err)
		}

		// Заказ уезжает наружу с уже записанным журналом: переходы, которые случатся с
		// ним дальше, допишутся к истории, а не повторят её.
		return order.Recorded(), true, nil
	}

	// Идентификатор занят: журнал занявшего его заказа уже написан тогда, когда заказ
	// оформляли, и повтор в него ничего не дописывает — повтор не изменил ничего.
	existing, err := findOrder(ctx, tx, order.ID)
	if err != nil {
		return domain.Order{}, false, err
	}

	return existing, false, nil
}

// findOrder отдаёт заказ по одному лишь идентификатору — тем ключом, который занят.
func findOrder(ctx context.Context, tx *sql.Tx, id uuid.UUID) (domain.Order, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+orderColumns+` FROM platform.orders WHERE id = $1`, id)

	order, err := scanOrder(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Строки нет, хотя вставка на неё же и наткнулась: заказы не удаляются, поэтому
		// это не «заказ не найден», а рассогласование, о котором надо знать.
		return domain.Order{}, fmt.Errorf("заказ %s занял идентификатор и исчез: %w", id, err)
	case err != nil:
		return domain.Order{}, fmt.Errorf("прочитать заказ %s: %w", id, err)
	}

	return order, nil
}

// SaveStatus записывает новое состояние заказа и его переходы в журнал.
//
// В списке колонок нет ни состава, ни суммы, ни адреса, и это не экономия запроса:
// переход состояния не вправе тронуть то, что покупатель уже оформил, а запрос, который
// перечисляет все колонки «на всякий случай», однажды перепишет заказ задним числом.
//
// Состояние и журнал пишутся одной транзакцией: заказ, у которого статус изменился, а
// журнал об этом не знает, — это заказ с историей, которой нельзя верить, и заметить
// такое расхождение можно только тогда, когда по журналу уже разбирают жалобу.
func (o *Orders) SaveStatus(ctx context.Context, order domain.Order) error {
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("начать запись состояния заказа %s: %w", order.ID, err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		UPDATE platform.orders
		SET status = $2, cancelled_by = $3, cancellation_reason = $4, updated_at = $5
		WHERE id = $1`,
		order.ID, string(order.Status), string(order.CancelledBy),
		order.CancellationReason, order.UpdatedAt)
	if err != nil {
		return fmt.Errorf("записать состояние заказа %s: %w", order.ID, err)
	}

	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("записать состояние заказа %s: %w", order.ID, err)
	}
	if changed == 0 {
		return domain.ErrOrderNotFound
	}

	if err := recordStatusChanges(ctx, tx, order); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("записать состояние заказа %s: %w", order.ID, err)
	}

	return nil
}

// recordStatusChanges дописывает в журнал переходы, случившиеся с заказом.
//
// Что именно записать, решает агрегат, а не хранилище: он один знает, изменилось ли
// состояние на самом деле, — и отброшенное опоздавшее уведомление в журнал не попадает
// именно поэтому, а не потому, что об этом помнит вызывающий.
func recordStatusChanges(ctx context.Context, tx *sql.Tx, order domain.Order) error {
	for _, change := range order.StatusChanges() {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO platform.order_status_events (order_id, status, actor, occurred_at)
			VALUES ($1, $2, $3, $4)`,
			order.ID, string(change.Status), string(change.Actor), change.OccurredAt)
		if err != nil {
			return fmt.Errorf("записать в журнал переход заказа %s в %s: %w", order.ID, change.Status, err)
		}
	}

	return nil
}

// Find отдаёт заказ по одному лишь идентификатору, кому бы он ни принадлежал.
//
// Так заказ читает единственный сценарий — отмена через саппорта: обращение о заказе
// приходит к нему от заведения, и покупателя, вместе с которым заказ полагается
// спрашивать, у саппорта на руках нет. Открыт этот путь ровно там, где его закрывает
// админский токен; всё остальное читает заказ вместе с тем, кому он виден.
func (o *Orders) Find(ctx context.Context, id uuid.UUID) (domain.Order, error) {
	row := o.db.QueryRowContext(ctx, `SELECT `+orderColumns+` FROM platform.orders WHERE id = $1`, id)

	order, err := scanOrder(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.Order{}, domain.ErrOrderNotFound
	case err != nil:
		return domain.Order{}, fmt.Errorf("прочитать заказ %s: %w", id, err)
	}

	return order, nil
}

// FindForCustomer отдаёт заказ, принадлежащий этому покупателю.
//
// Принадлежность стоит условием самого запроса, а не проверкой после чтения: заказ,
// прочитанный «чтобы посмотреть, чей он», уже лежит в памяти службы, и забытая проверка
// отдаёт его целиком. Чужой заказ здесь неотличим от несуществующего — этого заказа у
// покупателя нет ни в том, ни в другом случае.
func (o *Orders) FindForCustomer(ctx context.Context, id, customer uuid.UUID) (domain.Order, error) {
	row := o.db.QueryRowContext(ctx,
		`SELECT `+orderColumns+` FROM platform.orders WHERE id = $1 AND customer_id = $2`, id, customer)

	order, err := scanOrder(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.Order{}, domain.ErrOrderNotFound
	case err != nil:
		return domain.Order{}, fmt.Errorf("прочитать заказ %s: %w", id, err)
	}

	return order, nil
}

// FindForRestaurant отдаёт заказ, который выполняет это заведение.
//
// Заведение стоит условием самого запроса — по той же причине, по которой в запросе
// заказа покупателя стоит покупатель: заказ, прочитанный «чтобы посмотреть, чей он», уже
// лежит в памяти службы, и забытая проверка отдаёт его целиком. Чужой заказ здесь
// неотличим от несуществующего: заведению, перебирающему идентификаторы, не полагается
// узнать даже то, что такой заказ на площадке есть.
func (o *Orders) FindForRestaurant(ctx context.Context, id, restaurant uuid.UUID) (domain.Order, error) {
	row := o.db.QueryRowContext(ctx,
		`SELECT `+orderColumns+` FROM platform.orders WHERE id = $1 AND restaurant_id = $2`, id, restaurant)

	order, err := scanOrder(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.Order{}, domain.ErrOrderNotFound
	case err != nil:
		return domain.Order{}, fmt.Errorf("прочитать заказ %s: %w", id, err)
	}

	return order, nil
}

// storedItem — позиция заказа в том виде, в каком она лежит в базе.
//
// Отдельный тип, а не сам domain.OrderItem: формат хранения не должен меняться от того,
// что кто-то переименовал поле в предметной области, — прочитать после этого уже
// записанные заказы было бы нечем.
type storedItem struct {
	ProductID  uuid.UUID `json:"product_id"`
	ExternalID string    `json:"external_id"`
	Name       string    `json:"name"`
	Price      int64     `json:"price"`
	Quantity   int       `json:"quantity"`
}

// storedItems переводит состав заказа в формат хранения.
func storedItems(items []domain.OrderItem) []storedItem {
	stored := make([]storedItem, 0, len(items))
	for _, item := range items {
		stored = append(stored, storedItem{
			ProductID:  item.ProductID,
			ExternalID: item.ExternalID,
			Name:       item.Name,
			Price:      int64(item.Price),
			Quantity:   item.Quantity,
		})
	}

	return stored
}

// scanOrder читает заказ из строки результата.
func scanOrder(row rowScanner) (domain.Order, error) {
	var (
		order       domain.Order
		status      string
		cancelledBy string
		items       []byte
		total       int64
	)

	err := row.Scan(&order.ID, &order.CustomerID, &order.RestaurantID, &status, &order.Address,
		&items, &total, &order.RequestHash, &cancelledBy, &order.CancellationReason,
		&order.CreatedAt, &order.UpdatedAt)
	if err != nil {
		return domain.Order{}, err
	}

	var stored []storedItem
	if err := json.Unmarshal(items, &stored); err != nil {
		return domain.Order{}, fmt.Errorf("разобрать состав заказа %s: %w", order.ID, err)
	}

	order.Status = domain.OrderStatus(status)
	order.CancelledBy = domain.Actor(cancelledBy)
	order.Total = domain.Money(total)
	order.Items = make([]domain.OrderItem, 0, len(stored))
	for _, item := range stored {
		order.Items = append(order.Items, domain.OrderItem{
			ProductID:  item.ProductID,
			ExternalID: item.ExternalID,
			Name:       item.Name,
			Price:      domain.Money(item.Price),
			Quantity:   item.Quantity,
		})
	}

	return order, nil
}
