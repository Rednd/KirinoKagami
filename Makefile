# KirinoKagami - Docker helpers. Run `make help` for the list of targets.

COMPOSE      ?= docker compose
COMPOSE_DB   := $(COMPOSE) --profile db
SERVICE      := kirinokagami
DB_SERVICE   := mariadb
ADMIN_TOKEN  ?=
URL          ?= http://localhost:62011

.DEFAULT_GOAL := help
.PHONY: help config check-config build rebuild up up-all down down-all restart logs logs-db logs-all \
        ps shell db-shell status reindex test go-build clean nuke

help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

config: ## Create config.toml from the example if it doesn't exist
	@if [ -f config.toml ]; then echo "config.toml already exists"; \
	else cp config.example.toml config.toml && echo "created config.toml - fill it in before starting"; fi

check-config:
	@[ -f config.toml ] || { echo "config.toml missing - run 'make config' and fill it in first"; exit 1; }

build: ## Build the mirror image
	$(COMPOSE) build

rebuild: ## Build the mirror image without cache
	$(COMPOSE) build --no-cache

up: check-config ## Run just the mirror (uses your own external database)
	$(COMPOSE) up -d --build $(SERVICE)

up-all: check-config ## Run the mirror plus the bundled MariaDB
	$(COMPOSE_DB) up -d --build

down: ## Stop the mirror (leaves a bundled database running)
	$(COMPOSE) stop $(SERVICE)
	$(COMPOSE) rm -f $(SERVICE)

down-all: ## Stop and remove everything, including the bundled database (data volumes are kept)
	$(COMPOSE_DB) down

restart: ## Restart the mirror
	$(COMPOSE) restart $(SERVICE)

logs: ## Follow the mirror's logs
	$(COMPOSE) logs -f --tail=200 $(SERVICE)

logs-db: ## Follow the bundled database's logs
	$(COMPOSE_DB) logs -f --tail=200 $(DB_SERVICE)

logs-all: ## Follow logs of all services
	$(COMPOSE_DB) logs -f --tail=200

ps: ## Show running services
	$(COMPOSE_DB) ps

shell: ## Open a shell in the mirror container
	$(COMPOSE) exec $(SERVICE) sh

db-shell: ## Open a MariaDB client in the bundled database
	$(COMPOSE_DB) exec $(DB_SERVICE) sh -c 'mariadb -u"$$MARIADB_USER" -p"$$MARIADB_PASSWORD" "$$MARIADB_DATABASE"'

status: ## Show mirror status (DB counts, search index, scraper)
	@curl -fsS $(URL)/api/status | (command -v jq >/dev/null && jq . || cat); echo

reindex: ## Rebuild the search index now (needs ADMIN_TOKEN=...)
	@[ -n "$(ADMIN_TOKEN)" ] || { echo "usage: make reindex ADMIN_TOKEN=<server.admin_token>"; exit 1; }
	curl -fsS -X POST -H "Authorization: Bearer $(ADMIN_TOKEN)" $(URL)/admin/search/rebuild; echo

test: ## Run Go tests locally
	go test ./...

go-build: ## Build the binary locally (no Docker)
	CGO_ENABLED=0 go build -trimpath -o kirinokagami ./cmd/kirinokagami

clean: ## Remove the local binary and the built image
	rm -f kirinokagami
	-docker image rm kirinokagami:latest

nuke: ## Stop everything AND delete data volumes (downloads, search snapshot, bundled DB)
	@printf "This deletes all cached .osz files and the bundled database. Type 'yes' to continue: "; \
	read ans; [ "$$ans" = "yes" ] || { echo "aborted"; exit 1; }
	$(COMPOSE_DB) down -v
