-- Реквизиты синхронизации каталога: чем площадка узнаёт позицию заведения между
-- выгрузками, чем помнит исчезнувшую и когда в последний раз забирала меню.
--
-- `external_id` — идентификатор позиции в системе заведения. Пара «заведение + внешний
-- идентификатор» — единственный ключ, по которому площадка сводит новую выгрузку со
-- старым каталогом: собственный идентификатор товара выдаёт она сама, а заведение о нём
-- не знает и прислать его не может. Колонка добавляется сразу NOT NULL и без значения по
-- умолчанию: до этой миграции меню приезжать было неоткуда — каталог наполняет
-- синхронизация, которую заводит этот же слайс, — и подставленный пустой внешний
-- идентификатор означал бы позицию, которую следующая выгрузка не узнает.
--
-- `is_active` — позиция исчезла из выгрузки заведения. Именно деактивируется, а не
-- удаляется: удалённая строка унесла бы с собой историю, а вернувшаяся назавтра позиция
-- получила бы новый идентификатор и разошлась бы со ссылками, которые уже у покупателя.
--
-- `updated_at` — когда позицию последний раз тронула выгрузка. Она не участвует в отборе
-- витрины и нужна человеку, разбирающему жалобу «цена на витрине не та»: по ней видно,
-- доехало ли до площадки изменение и когда.

-- +goose Up
ALTER TABLE platform.products
    ADD COLUMN external_id text        NOT NULL CHECK (length(external_id) BETWEEN 1 AND 200),
    ADD COLUMN is_active   boolean     NOT NULL DEFAULT true,
    ADD COLUMN updated_at  timestamptz NOT NULL DEFAULT now();

-- Уникальность пары — не украшение, а само правило обновления: две позиции заведения с
-- одним внешним идентификатором сделали бы выгрузку неразрешимой, и площадка выбирала бы
-- между ними наугад. Этот же индекс ищет позицию при каждом обновлении каталога.
CREATE UNIQUE INDEX products_restaurant_external_idx
    ON platform.products (restaurant_id, external_id);

-- Индекс витрины пересобирается частичным: деактивированные позиции витрина не читает
-- никогда, и держать их в индексе значило бы платить за них на каждой странице меню.
DROP INDEX platform.products_menu_idx;
CREATE INDEX products_menu_idx ON platform.products (restaurant_id, name, id) WHERE is_active;

-- Время последней синхронизации каталога заведения. Пусто — меню не забирали ни разу:
-- отличать «не синхронизировалось» от «синхронизировалось в эпоху Unix» приходится тому,
-- кто разбирается, почему на витрине пусто, поэтому третье состояние здесь настоящее.
ALTER TABLE platform.restaurants
    ADD COLUMN last_synced_at timestamptz;

-- +goose Down
ALTER TABLE platform.restaurants
    DROP COLUMN last_synced_at;

DROP INDEX platform.products_menu_idx;
CREATE INDEX products_menu_idx ON platform.products (restaurant_id, name, id);

DROP INDEX platform.products_restaurant_external_idx;

ALTER TABLE platform.products
    DROP COLUMN external_id,
    DROP COLUMN is_active,
    DROP COLUMN updated_at;
