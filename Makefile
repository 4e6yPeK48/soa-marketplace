DOCKER_CONTEXT := colima-distsys

.PHONY: up down docker-setup

up: docker-setup
	docker --context "$(DOCKER_CONTEXT)" compose up --build -d

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
