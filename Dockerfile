# syntax=docker/dockerfile:1
#
# Один Dockerfile обслуживает всё, для чего иначе понадобился бы локально
# установленный Go: сборку обеих служб, статический анализ и прогон тестов.
# Цель выбирается флагом `--target` (см. Makefile), образ службы — аргументом SERVICE.

ARG GO_VERSION=1.27
ARG LINT_VERSION=v2.13.2

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
