# syntax=docker/dockerfile:1
#
# Один Dockerfile обслуживает всё, для чего иначе понадобился бы локально
# установленный Go: сборку обеих служб, статический анализ и прогон тестов.
# Цель выбирается флагом `--target` (см. Makefile), образ службы — аргументом SERVICE.

ARG GO_VERSION=1.27
ARG LINT_VERSION=v2.13.2
ARG REDOCLY_VERSION=2.51.2

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
 && redocly bundle api/customer/v1/openapi.yaml -o /out/api/customer/v1/bundle.yaml \
 && redocly bundle api/partner/v1/openapi.yaml  -o /out/api/partner/v1/bundle.yaml \
 && redocly bundle api/admin/v1/openapi.yaml    -o /out/api/admin/v1/bundle.yaml

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
 && go tool oapi-codegen -config api/codegen/restaurant_partner.yaml        api/partner/v1/bundle.yaml

# --- артефакты генерации ------------------------------------------------------
# Пустой образ с одними результатами: `make api` выгружает его прямо в рабочее дерево
# (`docker build --output`), потому что каталог хоста в сборку не примонтировать.
FROM scratch AS api-artifacts
COPY --from=bundle /out/api /api
COPY --from=codegen /src/internal /internal

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
