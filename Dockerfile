# syntax=docker/dockerfile:1
#
# Один Dockerfile обслуживает всё, для чего иначе понадобился бы локально
# установленный Go: сборку обеих служб, статический анализ и прогон тестов.
# Цель выбирается флагом `--target` (см. Makefile), образ службы — аргументом SERVICE.

ARG GO_VERSION=1.27
ARG LINT_VERSION=v2.13.2
ARG REDOCLY_VERSION=2.51.2
ARG GOOSE_VERSION=v3.28.0
ARG PLANTUML_VERSION=1.2026.8

# --- зависимости -------------------------------------------------------------
FROM golang:${GO_VERSION}-alpine AS deps
WORKDIR /src
# Отдельный слой: правка кода не приводит к повторной загрузке модулей. Кеш-монтирование
# здесь не используется намеренно — модули должны остаться в слое, иначе образ для тестов
# скачивал бы их заново на каждом прогоне. Модуля два: службы и шов контрактов (`api`),
# который вынесен отдельным модулем, чтобы его импортировали снаружи.
COPY go.mod go.sum ./
COPY api/go.mod ./api/
RUN go mod download && cd api && go mod download

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
        api/restaurant/operator/v1/openapi.yaml \
 && redocly bundle api/customer/v1/openapi.yaml -o /out/api/customer/v1/bundle.yaml \
 && redocly bundle api/partner/v1/openapi.yaml  -o /out/api/partner/v1/bundle.yaml \
 && redocly bundle api/admin/v1/openapi.yaml    -o /out/api/admin/v1/bundle.yaml \
 && redocly bundle api/restaurant/operator/v1/openapi.yaml -o /out/api/restaurant/operator/v1/bundle.yaml

# --- генерация кода по спецификациям -----------------------------------------
# Генерируем от собранных документов, а не от дерева ссылок: результат не зависит от
# того, как кодогенератор разрешает внешние `$ref`. Версия генератора закреплена
# директивой `tool` в go.mod, поэтому повторный прогон даёт тот же байт в байт код.
# Конфиги лежат рядом со спеками, а пути в них — от корня репозитория: генерация всегда
# запускается отсюда. Серверные интерфейсы уезжают к своим адаптерам в `internal`,
# клиенты остаются в модуле `api`.
FROM deps AS codegen
COPY api ./api
COPY --from=bundle /out/api ./api
RUN --mount=type=cache,target=/root/.cache/go-build \
    go tool oapi-codegen -config api/customer/v1/codegen-server.yaml               api/customer/v1/bundle.yaml \
 && go tool oapi-codegen -config api/customer/v1/codegen-client.yaml               api/customer/v1/bundle.yaml \
 && go tool oapi-codegen -config api/partner/v1/codegen-server-platform.yaml       api/partner/v1/bundle.yaml \
 && go tool oapi-codegen -config api/partner/v1/codegen-server-restaurant.yaml     api/partner/v1/bundle.yaml \
 && go tool oapi-codegen -config api/partner/v1/codegen-client.yaml                api/partner/v1/bundle.yaml \
 && go tool oapi-codegen -config api/admin/v1/codegen-server.yaml                  api/admin/v1/bundle.yaml \
 && go tool oapi-codegen -config api/admin/v1/codegen-client.yaml                  api/admin/v1/bundle.yaml \
 && go tool oapi-codegen -config api/restaurant/operator/v1/codegen-server.yaml    api/restaurant/operator/v1/bundle.yaml \
 && cd api && go mod tidy

# --- артефакты генерации ------------------------------------------------------
# Пустой образ с одними результатами: `make api` выгружает его прямо в рабочее дерево
# (`docker build --output`), потому что каталог хоста в сборку не примонтировать.
FROM scratch AS api-artifacts
COPY --from=codegen /src/api /api
COPY --from=codegen /src/internal /internal

# --- диаграммы ----------------------------------------------------------------
# Диаграммы — код: C4 и оба CJM лежат исходниками в `docs/puml`, а картинки в `docs/img`
# порождаются из них. Нарисованная руками картинка расходится с системой на первой же
# правке, и заметить это некому; отрисовка одной командой (`make diagrams`) делает
# расхождение видимым — пересобранная диаграмма даёт дифф.
#
# Ни одной внешней ссылки при отрисовке не разрешается: библиотека C4 входит в поставку
# PlantUML, поэтому сборка не ходит в сеть и не зависит от того, жив ли сегодня чужой
# репозиторий с макросами. `-failfast2` останавливает всё на первой сломанной диаграмме:
# PlantUML иначе положит текст ошибки картинкой, и в репозиторий уедет она.
FROM plantuml/plantuml:${PLANTUML_VERSION} AS diagrams
USER root
WORKDIR /docs
COPY docs/puml ./puml
RUN mkdir -p /out/docs/img \
 && java -jar /opt/plantuml.jar -failfast2 -nometadata -tpng -o /out/docs/img puml/*.puml

# --- артефакты отрисовки ------------------------------------------------------
# Как и у генерации по спецификациям: каталог хоста в сборку не примонтировать, поэтому
# результат выгружается из пустого образа прямо в рабочее дерево.
FROM scratch AS diagram-artifacts
COPY --from=diagrams /out /

# --- инструмент миграций ------------------------------------------------------
# goose CLI ставится отдельным бинарём и в бинари служб не попадает: миграции накатывает
# отдельный шаг запуска, а службе инструмент миграций не нужен — на этом стоит сторож
# `TestServiceBinariesDoNotCarryTheMigrationTool`. В go.mod goose при этом есть:
# его библиотеку импортирует тестовая оснастка (`internal/pkg/testdb`), чтобы накатывать
# в тестах те же миграции. Версия CLI закреплена аргументом (`GOOSE_VERSION`) и держится
# той же, что у библиотеки в go.mod: тогда тесты и шаг запуска катят одним и тем же goose.
# На то, что эти две версии не разъехались, стоит сторож
# `TestTheMigrationToolIsPinnedToTheSameVersionAsItsLibrary`.
FROM golang:${GO_VERSION}-alpine AS goose
ARG GOOSE_VERSION
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go install github.com/pressly/goose/v3/cmd/goose@${GOOSE_VERSION}

# --- журналы миграций ---------------------------------------------------------
# Имя журнала применённых миграций объявлено Go-константой (`VersionTable` в пакете
# миграций контекста): оттуда его берёт тестовая оснастка, и шаг запуска обязан брать то
# же самое — иначе тесты и запуск разойдутся по разным журналам молча. Поэтому имя не
# пишется здесь руками, а вынимается из тех же констант.
FROM source AS migration-journals
RUN --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p /journals \
 && for context in platform restaurant platform_seed; do \
        go run ./internal/tools/migrationjournal "${context}" > "/journals/${context}"; \
    done

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
COPY --from=migration-journals /journals /journals
# У каждого контекста свой каталог миграций и свой журнал применённых; имя журнала лежит
# рядом, в /journals/<контекст>, и приходит из Go-константы. Журналы лежат в public,
# а не в самих схемах: goose создаёт журнал до первой миграции, а первая миграция как раз
# и создаёт схему. Демо-данные накатываются последними и откатываются первыми: они живут
# в таблицах, которые заводит схема.
COPY <<'SCRIPT' /usr/local/bin/migrate
#!/bin/sh
set -eu

# Перечень контекстов шага запуска повторяется здесь и в стадии `migration-journals`, а
# их имена документирует README. `platform` и `restaurant` дополнительно перечислены в
# Makefile и `migrationSets` (internal/pkg/testdb); `platform_seed` там намеренно нет —
# заготовки seed создаются не общей командой, а тестам демо-данные не нужны.
#
# Демо-данные накатываются только там, где их просят: у демо-заведения опубликованный
# ключ (см. seed/00001_demo_restaurant.sql), и подсовывать его любому окружению, где
# выполнили штатный шаг миграций, площадка не вправе. `compose.yaml` включает их сам,
# поэтому `docker compose up` остаётся рабочим демо без единой настройки.
contexts="platform restaurant"
if [ "${SEED_DEMO_DATA:-0}" = "1" ]; then
	contexts="${contexts} platform_seed"
fi

# Откат идёт в обратном порядке: `Down` сида удаляет строки из таблиц, которые сносит
# `Down` схемы, и в прямом порядке он выполнялся бы уже по несуществующей схеме — goose
# падал бы, а журнал сида остался бы с отметкой «применено». Следующий `up` поднял бы
# схему заново и пропустил сид как накатанный: демо-заведения не было бы, и ошибки при
# этом не вывелось бы.
case "${1:-}" in
down | down-to | reset)
	reversed=""
	for context in ${contexts}; do
		reversed="${context}${reversed:+ }${reversed}"
	done
	contexts="${reversed}"

	# Сносящая команда спрашивает подтверждение: `reset` и `down-to 0` удаляют схемы
	# целиком вместе с данными, а восстановить их после `docker compose run` нечем.
	# Молчание считается отказом — в неинтерактивном запуске (CI, скрипт) читать ответ
	# не у кого, и там подтверждение даётся заранее: SEED_DEMO_DATA этому не поможет,
	# нужен MIGRATE_ASSUME_YES=1.
	if [ "${MIGRATE_ASSUME_YES:-0}" != "1" ]; then
		printf 'goose %s снесёт миграции контекстов [%s] вместе с их данными. Вы уверены? [y/N] ' "$*" "${contexts}"
		read -r answer || answer=""
		case "${answer}" in
		y | Y | yes | YES) ;;
		*)
			echo "Отменено: ничего не снесено."
			exit 1
			;;
		esac
	fi
	;;
esac

for context in ${contexts}; do
	echo "== ${context}: goose $*"
	goose -table "$(cat "/journals/${context}")" -dir "/migrations/${context}" "$@"
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
