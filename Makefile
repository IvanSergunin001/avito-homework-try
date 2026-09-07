# Всё, кроме `docker compose`, выполняется внутри контейнеров: локально установленный
# Go не нужен ни для сборки, ни для линтера, ни для тестов (см. Dockerfile).

TEST_IMAGE ?= avito-kitchen-test
TEST_ARGS  ?= ./...
DOCKER_SOCK := /var/run/docker.sock

.PHONY: help up down logs clean-data api api-check build lint test

help: ## Список команд
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  %-10s %s\n", $$1, $$2}'

up: ## Поднять площадку, коробку заведения и PostgreSQL
	docker compose up --build --detach --wait
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
# результат генерации выгружается из образа-артефакта прямо в рабочее дерево.
api: ## Собрать спецификации в bundle.yaml и сгенерировать по ним код
	docker build --target api-artifacts --output type=local,dest=. .

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

test: ## Тесты; сокет Docker нужен тестам с testcontainers
	docker build --target test --tag $(TEST_IMAGE) .
	docker run --rm --volume $(DOCKER_SOCK):$(DOCKER_SOCK) $(TEST_IMAGE) go test $(TEST_ARGS)
