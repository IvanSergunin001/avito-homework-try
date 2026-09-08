-- Продвижение заказа по кухне и очередь уведомлений площадке.
--
-- Заказ, подтверждённый оператором, живёт дальше: его готовят, везут и отдают. Перечень
-- статусов в ограничении расширяется теми же значениями, которыми заказ живёт на
-- площадке, — два словаря для одних и тех же состояний означали бы перевод на каждой
-- границе и расхождение при первой же правке одного из них.
--
-- Каждая смена статуса кладёт уведомление в очередь этой же транзакцией. Очередь —
-- обычная таблица коробки, а не вызов площадки из обработчика: площадка бывает
-- недоступна ровно в ту секунду, когда оператор нажал «в доставку», и статус, потерянный
-- из-за чужого перезапуска, покупатель увидел бы как зависший заказ.
--
-- Ключ таблицы — пара «заказ + статус», а не отдельный идентификатор: путь заказа идёт в
-- одну сторону, и в каждое состояние он приходит ровно один раз. Значит, вторая строка о
-- том же переходе — это не второе уведомление, а повтор, и отбивает его ключ, а не
-- проверка перед вставкой, между которой и вставкой помещается соседняя транзакция.
--
-- В `docs/PLAN.md` таблица набросана как `status_callbacks`. Здесь она названа словом
-- контракта — уведомление, — потому что партнёрская спецификация и код обеих служб
-- говорят «уведомление»: третье название для той же вещи пришлось бы переводить в голове
-- каждому, кто читает и схему, и контракт.

-- +goose Up
ALTER TABLE restaurant.orders DROP CONSTRAINT orders_status_check;
ALTER TABLE restaurant.orders ADD CONSTRAINT orders_status_check
    CHECK (status IN ('under_review', 'confirmed', 'cooking', 'delivering', 'completed', 'cancelled'));

CREATE TABLE restaurant.status_notifications (
    order_id        uuid        NOT NULL REFERENCES restaurant.orders (id),
    -- Перечень — тот, который объявлен партнёрским контрактом уведомляемым: `pending` и
    -- `under_review` площадка ставит сама, и уведомлять её о них нечем.
    status          text        NOT NULL
        CHECK (status IN ('confirmed', 'cooking', 'delivering', 'completed', 'cancelled')),
    -- «Причины нет» — пустая строка, а не NULL: она есть только у отмены, и различать
    -- третье состояние текста пришлось бы всем читателям.
    reason          text        NOT NULL DEFAULT '' CHECK (length(reason) <= 500),
    occurred_at     timestamptz NOT NULL,
    attempts        integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    -- Когда уведомление можно везти следующий раз. Свежее едет сразу, недоставленное —
    -- после паузы, которая растёт с каждой неудачей.
    next_attempt_at timestamptz NOT NULL,
    delivered_at    timestamptz,
    -- Момент, когда попытки кончились. Отдельная колонка, а не вывод из счётчика:
    -- исчерпание — это то, что ищут запросом, разбирая жалобу на застрявший статус, и
    -- считать его на лету каждому читателю значило бы повторять правило в каждом запросе.
    failed_at       timestamptz,
    -- Чем ответила площадка на последнюю неудачную попытку: с этим идут разбираться.
    last_error      text        NOT NULL DEFAULT '',
    PRIMARY KEY (order_id, status)
);

-- Индекс повторяет запрос доставщика целиком: он ищет только то, что ещё не доехало и
-- чему уже пора. Частичный — потому что доставленные уведомления копятся, а искать их
-- этим запросом никто не будет.
CREATE INDEX status_notifications_due_idx
    ON restaurant.status_notifications (next_attempt_at)
    WHERE delivered_at IS NULL AND failed_at IS NULL;

-- +goose Down
DROP TABLE restaurant.status_notifications;

ALTER TABLE restaurant.orders DROP CONSTRAINT orders_status_check;
ALTER TABLE restaurant.orders ADD CONSTRAINT orders_status_check
    CHECK (status IN ('under_review', 'confirmed', 'cancelled'));
