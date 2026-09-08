# syntax=docker/dockerfile:1
#
# Один Dockerfile обслуживает всё, для чего иначе понадобился бы локально
# установленный Go: сборку обеих служб, статический анализ и прогон тестов.
# Цель выбирается флагом `--target` (см. Makefile), образ службы — аргументом SERVICE.

ARG GO_VERSION=1.27
ARG LINT_VERSION=v2.13.2
ARG REDOCLY_VERSION=2.51.2
ARG GOOSE_VERSION=v3.28.0

# --- зависимости -------------------------------------------------------------
FROM golang:${GO_VERSION}-alpine AS deps
WORKDIR /src
# Отдельный слой: правка кода не приводит к повторной загрузке модулей. Кеш-монтирование
# здесь не используется намеренно — модули должны остаться в слое, иначе образ для тестов
# скачивал бы их заново на каждом прогоне.
COPY go.mod go.sum ./
RUN go mod download

# --- исходники ---------------------------------------------------------------
FROM deps AS source
COPY . .

# --- сборка спецификаций в один документ -------------------------------------
# Источник каждой спецификации разбит на файлы через `$ref`; партнёру и кодогенератору
# нужен самодостаточный документ, поэтому перед генерацией спецификации собираются
# в `bundle.yaml`. Линт стоит перед сборкой: сломанный контракт не должен доехать
# до генератора.
FROM redocly/cli:${REDOCLY_VERSION} AS bundle
WORKDIR /spec
COPY api ./api
RUN redocly lint \
        api/customer/v1/openapi.yaml \
        api/partner/v1/openapi.yaml \
        api/admin/v1/openapi.yaml \
        api/operator/v1/openapi.yaml \
 && redocly bundle api/customer/v1/openapi.yaml -o /out/api/customer/v1/bundle.yaml \
 && redocly bundle api/partner/v1/openapi.yaml  -o /out/api/partner/v1/bundle.yaml \
 && redocly bundle api/admin/v1/openapi.yaml    -o /out/api/admin/v1/bundle.yaml \
 && redocly bundle api/operator/v1/openapi.yaml -o /out/api/operator/v1/bundle.yaml

# --- генерация кода по спецификациям -----------------------------------------
# Генерируем от собранных документов, а не от дерева ссылок: результат не зависит от
# того, как кодогенератор разрешает внешние `$ref`. Версия генератора закреплена
# директивой `tool` в go.mod, поэтому повторный прогон даёт тот же байт в байт код.
FROM deps AS codegen
COPY api/codegen ./api/codegen
COPY --from=bundle /out/api ./api
RUN --mount=type=cache,target=/root/.cache/go-build \
    go tool oapi-codegen -config api/codegen/platform_customer.yaml         api/customer/v1/bundle.yaml \
 && go tool oapi-codegen -config api/codegen/platform_partner.yaml          api/partner/v1/bundle.yaml \
 && go tool oapi-codegen -config api/codegen/platform_admin.yaml            api/admin/v1/bundle.yaml \
 && go tool oapi-codegen -config api/codegen/platform_restaurant_client.yaml api/partner/v1/bundle.yaml \
 && go tool oapi-codegen -config api/codegen/restaurant_partner.yaml        api/partner/v1/bundle.yaml \
 && go tool oapi-codegen -config api/codegen/restaurant_platform_client.yaml api/partner/v1/bundle.yaml \
 && go tool oapi-codegen -config api/codegen/restaurant_operator.yaml       api/operator/v1/bundle.yaml

# --- артефакты генерации ------------------------------------------------------
# Пустой образ с одними результатами: `make api` выгружает его прямо в рабочее дерево
# (`docker build --output`), потому что каталог хоста в сборку не примонтировать.
FROM scratch AS api-artifacts
COPY --from=bundle /out/api /api
COPY --from=codegen /src/internal /internal

# --- инструмент миграций ------------------------------------------------------
# goose ставится отдельно и в go.mod не попадает: миграции накатывает отдельный шаг
# запуска, а службы не должны тянуть инструмент миграций в свои зависимости. Версия
# закреплена аргументом, чтобы шаг миграций был воспроизводим.
FROM golang:${GO_VERSION}-alpine AS goose
ARG GOOSE_VERSION
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go install github.com/pressly/goose/v3/cmd/goose@${GOOSE_VERSION}

# --- шаг миграций -------------------------------------------------------------
# Образ с одним бинарём и SQL-файлами: Compose выполняет его до старта служб
# (`depends_on: service_completed_successfully`), а `make migrate-status` — вручную.
FROM alpine:3.22 AS migrate
COPY --from=goose /go/bin/goose /usr/local/bin/goose
COPY internal/platform/migrations/*.sql /migrations/platform/
COPY internal/restaurant/migrations/*.sql /migrations/restaurant/
# Демонстрационные данные — отдельный контекст со своим журналом: они часть запуска демо,
# а не часть схемы. Тестам они не накатываются (см. internal/pkg/testdb), иначе каждый
# тест, который считает заведения, считал бы вместе со своими ещё и демонстрационное.
COPY internal/platform/migrations/seed/*.sql /migrations/platform_seed/
# У каждого контекста свой каталог миграций и свой журнал применённых. Журналы лежат в
# public, а не в самих схемах: goose создаёт журнал до первой миграции, а первая миграция
# как раз и создаёт схему. Демо-данные идут последними: они вставляют строки в таблицы,
# которые заводят миграции схемы.
COPY <<'SCRIPT' /usr/local/bin/migrate
#!/bin/sh
set -eu

for context in platform restaurant platform_seed; do
	echo "== ${context}: goose $*"
	goose -table "public.goose_${context}_version" -dir "/migrations/${context}" "$@"
done
SCRIPT
RUN chmod +x /usr/local/bin/migrate
ENTRYPOINT ["/usr/local/bin/migrate"]
CMD ["up"]

# --- заготовка новой миграции -------------------------------------------------
# `make migration name=...` создаёт файл тем же инструментом, которым миграции
# накатываются, и выгружает его в рабочее дерево: нумерация продолжает уже имеющуюся,
# а шаблон не расходится с тем, что goose ожидает прочитать.
FROM goose AS migration-draft
ARG MIGRATION_CONTEXT=platform
ARG MIGRATION_NAME
WORKDIR /work
COPY internal/${MIGRATION_CONTEXT}/migrations/*.sql /existing/
RUN test -n "${MIGRATION_NAME}" || { echo "нужно имя миграции: make migration name=..."; exit 1; } \
 && cp /existing/*.sql /work/ \
 && goose -s -dir /work create "${MIGRATION_NAME}" sql \
 && mkdir -p "/out/internal/${MIGRATION_CONTEXT}/migrations" \
 && for file in /work/*.sql; do \
        [ -f "/existing/$(basename "${file}")" ] || cp "${file}" "/out/internal/${MIGRATION_CONTEXT}/migrations/"; \
    done

FROM scratch AS new-migration
COPY --from=migration-draft /out /

# --- сборка службы -----------------------------------------------------------
FROM source AS build
ARG SERVICE
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -o /out/service ./cmd/${SERVICE}

# --- компиляция и go vet всего репозитория -----------------------------------
FROM source AS vet
RUN --mount=type=cache,target=/root/.cache/go-build \
    go build ./... && go vet ./...

# --- образ для прогона тестов ------------------------------------------------
# Тесты запускаются не в сборке, а из этого образа: тестам следующих этапов нужен
# сокет Docker (testcontainers), а в сборке его не пробросить.
FROM source AS test
ENV CGO_ENABLED=0
CMD ["go", "test", "./..."]

# --- линтер ------------------------------------------------------------------
FROM golangci/golangci-lint:${LINT_VERSION}-alpine AS lint
WORKDIR /src
# Модули берём из слоя deps: иначе линтер качал бы их заново после каждой правки кода.
COPY --from=deps /go/pkg/mod /go/pkg/mod
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/root/.cache/golangci-lint \
    golangci-lint run

# --- образ службы ------------------------------------------------------------
FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates
COPY --from=build /out/service /usr/local/bin/service
USER nobody
ENTRYPOINT ["/usr/local/bin/service"]
