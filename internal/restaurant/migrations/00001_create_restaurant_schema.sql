-- Схема коробки заведения. Она не ссылается на схему площадки ни одним внешним ключом:
-- заведение — другая организация, и в реальной эксплуатации это вообще другой инстанс
-- PostgreSQL. Один инстанс на два контекста — экономия на инфраструктуре демо.

-- +goose Up
CREATE SCHEMA IF NOT EXISTS restaurant;

-- +goose Down
DROP SCHEMA IF EXISTS restaurant CASCADE;
