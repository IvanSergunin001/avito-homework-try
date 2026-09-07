# Всё, кроме `docker compose`, выполняется внутри контейнеров: локально установленный
# Go не нужен ни для сборки, ни для линтера, ни для тестов (см. Dockerfile).

TEST_IMAGE ?= avito-kitchen-test
TEST_ARGS  ?= ./...
DOCKER_SOCK := /var/run/docker.sock

.PHONY: help up down logs build lint test

help: ## Список команд
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  %-7s %s\n", $$1, $$2}'

up: ## Поднять площадку, коробку заведения и PostgreSQL
	docker compose up --build

down: ## Остановить всё и удалить данные базы
	docker compose down --volumes

logs: ## Логи запущенных служб
	docker compose logs --follow

build: ## Скомпилировать репозиторий и прогнать go vet
	docker build --target vet .

lint: ## Линтер с конфигом из репозитория (.golangci.yml)
	docker build --target lint .

test: ## Тесты; сокет Docker нужен тестам с testcontainers
	docker build --target test --tag $(TEST_IMAGE) .
	docker run --rm --volume $(DOCKER_SOCK):$(DOCKER_SOCK) $(TEST_IMAGE) go test $(TEST_ARGS)
