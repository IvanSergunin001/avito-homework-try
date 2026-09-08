# Контракты

Четыре папки: три API — клиентское, партнёрское и админское — и `common` с тем, что у них
общее. Внутри каждой — версия папкой (`v1`), источник спецификации (`openapi.yaml`,
`paths/`, `components/`), собранный документ `bundle.yaml` и конфиги кодогенерации.

Гранулярность источника — файл на вид: путь отдельным файлом (`paths/orders.yaml`),
компоненты тремя файлами на документ — `components/schemas.yaml`, `parameters.yaml`,
`responses.yaml`. Ссылка на компонент — указатель внутрь файла:
`$ref: '../components/schemas.yaml#/Order'`. Файла на компонент нет намеренно: полсотни
файлов по десять строк на документ читаются хуже одного словаря, а правятся так же.

Общие ответы — `400`, `401`, `403`, `404` — лежат в `common/v1/components/responses.yaml`
и несут общую основу `ProblemBase`: у этих кодов документу нечего добавить сверх неё.
Отказ по правилам предметной области (`409` и соседи) документ объявляет у себя — там в
теле нужны позиция и цена, то есть свой `Problem`.

Правится только источник: `bundle.yaml` и весь сгенерированный код пересобираются командой
`make api`, а `make api-check` в CI проверяет, что повторный прогон не даёт диффа. Пути в
конфигах — от корня репозитория: генерация всегда запускается оттуда.

| Конфиг | Что порождает | Куда |
|---|---|---|
| `customer/v1/codegen-server.yaml` | серверный интерфейс клиентского API | `internal/platform/adapters/http/customer_v1` |
| `customer/v1/codegen-client.yaml` | клиент клиентского API | `api/customer/v1` |
| `partner/v1/codegen-server-platform.yaml` | серверный интерфейс площадки (тег `platform`) | `internal/platform/adapters/http/partner_v1` |
| `partner/v1/codegen-server-restaurant.yaml` | серверный интерфейс коробки (тег `restaurant`) | `internal/restaurant/adapters/http/partner_v1` |
| `partner/v1/codegen-client.yaml` | клиент партнёрского контракта | `api/partner/v1` |
| `admin/v1/codegen-server.yaml` | серверный интерфейс админского API | `internal/platform/adapters/http/admin_v1` |

Серверные интерфейсы лежат рядом с адаптером, который их реализует. Клиенты — здесь, в
модуле `github.com/IvanSergunin001/Test157/api`: у него свой `go.mod`, поэтому стороннее
заведение импортирует клиент контракта одной строкой и не тянет за собой службы.
