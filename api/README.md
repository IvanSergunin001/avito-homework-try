# Контракты

Четыре папки: три API — клиентское, партнёрское и админское — и `common` с тем, что у них
общее. Внутри каждой — версия папкой (`v1`), источник спецификации (`openapi.yaml`,
`paths/`, `components/`), собранный документ `bundle.yaml` и конфиги кодогенерации.

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
