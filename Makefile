DOCKER_CONTEXT ?= colima-distsys
E2E_ARTIFACTS_DIR ?= $(CURDIR)/artifacts/e2e
E2E_GO_FLAGS ?= -v

.PHONY: up down docker-setup generate build test check e2e e2e-report e2e-open logs db

generate:
	cd catalog-service && go generate ./internal/api

build: generate
	cd catalog-service && go build -o catalog .

test: generate
	cd catalog-service && go test -race ./...

check: test
	cd catalog-service && go vet ./...

e2e: generate
	cd catalog-service && DOCKER_CONTEXT="$(DOCKER_CONTEXT)" E2E_ARTIFACTS_DIR="$(abspath $(E2E_ARTIFACTS_DIR))" go test -tags=e2e -race -count=1 -timeout=5m $(E2E_GO_FLAGS) ./e2e

e2e-report:
	E2E_ARTIFACTS_DIR="$(abspath $(E2E_ARTIFACTS_DIR))" node scripts/e2e-report.mjs generate

e2e-open:
	E2E_ARTIFACTS_DIR="$(abspath $(E2E_ARTIFACTS_DIR))" node scripts/e2e-report.mjs open

logs:
	docker --context "$(DOCKER_CONTEXT)" compose logs -f catalog

db:
	docker --context "$(DOCKER_CONTEXT)" compose exec postgres psql -U marketplace -d marketplace

up: docker-setup
	docker --context "$(DOCKER_CONTEXT)" compose up --build -d --wait

down:
	docker --context "$(DOCKER_CONTEXT)" compose down

docker-setup:
	@command -v docker >/dev/null 2>&1 || { echo "Docker CLI не найден" >&2; exit 1; }
	@if [ "$(DOCKER_CONTEXT)" = "colima-distsys" ]; then \
		command -v colima >/dev/null 2>&1 || { echo "Colima не найдена: установите её или укажите другой DOCKER_CONTEXT" >&2; exit 1; }; \
		if ! colima status distsys >/dev/null 2>&1; then \
			printf "Создать или запустить профиль Colima distsys? [y/N] "; \
			read -r answer || exit 1; \
			case "$$answer" in y|Y|yes|YES) colima start distsys --activate=false || exit 1 ;; *) echo "Отменено"; exit 1 ;; esac; \
		fi; \
	fi
	@docker context inspect "$(DOCKER_CONTEXT)" >/dev/null 2>&1 || { echo "Docker context $(DOCKER_CONTEXT) не найден" >&2; exit 1; }
	@docker --context "$(DOCKER_CONTEXT)" info >/dev/null 2>&1 || { echo "Docker context $(DOCKER_CONTEXT) недоступен" >&2; exit 1; }
