-- Заведения площадки. Витрина покупателя читает эту таблицу и никакую другую: карточка
-- заведения и его меню — разные ресурсы, меню приезжает синхронизацией каталога в свою
-- таблицу и в карточку не встраивается.
--
-- Признак «работает» один — `is_active`. Отдельного «закрыто сейчас» в MVP нет: площадка
-- узнаёт о том, что заведение перестало работать, только как о снятии этого флага
-- (админской правкой при отключении партнёра), а закрытие по часам работы видно не витрине,
-- а оформлению заказа — синхронным отказом заведения `restaurant_closed`.

-- +goose Up
CREATE TABLE platform.restaurants (
    id         uuid        PRIMARY KEY,
    name       text        NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    is_active  boolean     NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Индекс повторяет запрос витрины целиком: отбор по флагу и порядок, в котором страница
-- отдаётся клиенту. Частичный — неработающие заведения витрина не читает никогда.
CREATE INDEX restaurants_showcase_idx ON platform.restaurants (name, id) WHERE is_active;

-- +goose Down
DROP TABLE platform.restaurants;
