# Всё, кроме `docker compose`, выполняется внутри контейнеров: локально установленный
# Go не нужен ни для сборки, ни для линтера, ни для тестов (см. Dockerfile).

API_OUT    ?= .api-artifacts
TEST_IMAGE ?= avito-kitchen-test
TEST_ARGS  ?= ./...
DOCKER_SOCK := /var/run/docker.sock
# Имя, под которым из контейнера с тестами виден хост Docker — машина, где опубликованы
# порты и базы Compose, и контейнеров testcontainers. Docker Desktop заводит это имя сам,
# на Linux его добавляет `--add-host ...:host-gateway`; поэтому имя одно на всех
# платформах, и разницу между ними не нужно знать ни человеку, ни этому файлу.
DOCKER_HOST_ALIAS := host.docker.internal
# Контекст, к которому относится новая миграция: платформа или коробка заведения. Тот же
# перечень контекстов повторяется в `Dockerfile` (шаг миграций), в `internal/pkg/testdb`
# (`migrationSets`) и в таблице контекстов README — третий правится во всех местах разом.
context    ?= platform

.PHONY: help up down logs clean-data api api-check diagrams diagrams-check migrate-up migrate-status migration build lint test

help: ## Список команд
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  %-15s %s\n", $$1, $$2}'

up: ## Поднять PostgreSQL, накатить миграции и поднять обе службы
	docker compose up --build --detach --wait platform restaurant
	@echo "Готово. Логи: make logs"

down: ## Остановить всё; данные базы остаются в ./.data
	docker compose down

# Каталог с данными создал Docker, и принадлежит он root: с хоста его сносит либо sudo,
# либо контейнер. Второе не требует прав и работает одинаково на любой машине.
clean-data: ## Погасить всё и удалить локальные данные базы (./.data)
	docker compose down
	docker run --rm --volume $(CURDIR)/.data:/data alpine:3 rm -rf /data/postgres

logs: ## Логи запущенных служб
	docker compose logs --follow

# Каталог хоста в сборку не примонтировать (демон Docker живёт снаружи), поэтому
# результат генерации выгружается из образа-артефакта. Выгрузка идёт во временный
# каталог, а не прямо в рабочее дерево: экспортёр обходит каталог назначения целиком и
# спотыкается о `./.data`, который Docker создал от root.
api: ## Собрать спецификации в bundle.yaml и сгенерировать по ним код
	rm -rf $(API_OUT)
	docker build --target api-artifacts --output type=local,dest=$(API_OUT) .
	cp -a $(API_OUT)/api/. api/
	cp -a $(API_OUT)/internal/. internal/
	rm -rf $(API_OUT)

api-check: api ## Проверить, что генерация воспроизводится: повторный прогон без диффа
	@test -z "$$(git status --porcelain -- api internal)" || { \
		git status --short -- api internal; \
		echo "Сгенерированное разошлось с закоммиченным — закоммитьте результат make api."; \
		exit 1; \
	}
	@echo "Генерация воспроизводится: диффа нет."

build: ## Скомпилировать репозиторий и прогнать go vet
	docker build --target vet .

lint: ## Линтер с конфигом из репозитория (.golangci.yml)
	docker build --target lint .

# Диаграммы — код: C4 и оба CJM лежат исходниками в `docs/puml`, картинки в `docs/img`
# отрисовываются из них и коммитятся (README показывает их прямо в тексте).
diagrams: ## Отрисовать C4 и CJM из исходников docs/puml в docs/img
	docker build --target diagram-artifacts --output type=local,dest=. .

diagrams-check: diagrams ## Проверить, что отрисованное совпадает с закоммиченным
	@test -z "$$(git status --porcelain -- docs/img)" || { \
		git status --short -- docs/img; \
		echo "Диаграммы разошлись с исходниками — закоммитьте результат make diagrams."; \
		exit 1; \
	}
	@echo "Диаграммы совпадают с исходниками: диффа нет."

# Миграции накатываются отдельным шагом до старта служб (сервис `migrate` в compose.yaml).
# Те же файлы миграций тесты накатывают на свою базу сами — см. internal/pkg/testdb.
migrate-up: ## Накатить миграции на базу Compose
	docker compose run --rm --build migrate up

migrate-status: ## Показать, какие миграции применены
	docker compose run --rm --build migrate status

# Заготовка создаётся тем же инструментом, что и накатывает миграции, и выгружается из
# образа в рабочее дерево: примонтировать каталог хоста в сборку нельзя (см. `api`).
migration: ## Создать миграцию: make migration name=add_orders [context=platform|restaurant]
	@test -n "$(name)" || { echo "нужно имя: make migration name=add_orders [context=platform]"; exit 1; }
	@test -n "$(filter $(context),platform restaurant)" || { echo "неизвестный контекст '$(context)': есть platform и restaurant"; exit 1; }
	docker build --target new-migration \
		--build-arg MIGRATION_CONTEXT=$(context) \
		--build-arg MIGRATION_NAME=$(name) \
		--output type=local,dest=. .

# Вывод прошедшего пакета `go test` без `-v` глотает целиком, поэтому в режиме
# TEST_KEEP_DB строку подключения к оставленной базе было бы видно только после падения.
# Флаг добавляется сам, когда переменная выставлена: смотреть в базу после зелёного
# прогона — ровно то, ради чего режим и заводился.
#
# Строку подключения из TEST_DB_DSN контейнер получает как есть: `localhost` в ней
# подменяет на адрес хоста Docker сама оснастка (`internal/pkg/testdb`) — она же и знает,
# что внутри контейнера `localhost` означает не ту машину, на которой человек её писал.
test: ## Тесты; сокет Docker нужен тестам с testcontainers
	docker build --target test --tag $(TEST_IMAGE) .
	docker run --rm --volume $(DOCKER_SOCK):$(DOCKER_SOCK) \
		--add-host $(DOCKER_HOST_ALIAS):host-gateway \
		--env TESTCONTAINERS_HOST_OVERRIDE=$(DOCKER_HOST_ALIAS) \
		--env TEST_DB_DSN --env TEST_KEEP_DB \
		$(TEST_IMAGE) go test $(if $(TEST_KEEP_DB),-v) $(TEST_ARGS)
