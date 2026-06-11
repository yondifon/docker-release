VERSION ?= $(shell v=$$(git tag --points-at HEAD 2>/dev/null | head -1); echo $${v:-dev})
IMAGE   ?= malico/docker-release

.PHONY: dev dev-remove test build publish tag buildx-builder \
	up-nginx up-angie up-traefik up-nginx-proxy up-caddy up-haproxy \
	down-nginx down-angie down-traefik down-nginx-proxy down-caddy down-haproxy

build: buildx-builder
	docker buildx build \
		--builder docker-release-builder \
		--platform linux/amd64,linux/arm64 \
		--build-arg VERSION=$(VERSION) \
		-t $(IMAGE):$(VERSION) \
		-t $(IMAGE):latest \
		.

tag:
	@test "$(VERSION)" != "dev" || (echo "ERROR: set VERSION=x.y.z"; exit 1)
	@git fetch --tags origin 2>/dev/null; \
	if git tag -l | grep -q "^v$(VERSION)$$"; then \
		echo "ERROR: tag v$(VERSION) already exists"; exit 1; \
	fi
	@git tag -a v$(VERSION) -m "Release v$(VERSION)"
	@git push origin v$(VERSION)
	@echo "Pushing sub-tags and latest..."
	@echo "$(VERSION)" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$$' && { \
		MAJOR=$$(echo "$(VERSION)" | cut -d. -f1); \
		MINOR=$$(echo "$(VERSION)" | cut -d. -f1-2); \
		git push origin v$${MAJOR} v$${MINOR} 2>/dev/null || true; \
	} || true
	@echo "Updating latest tag..."
	@git tag -d latest 2>/dev/null || true; \
	git tag -a latest -m "Latest release v$(VERSION)"; \
	git push origin latest --force 2>/dev/null || true

publish:
	@test "$(VERSION)" != "dev" || (echo "ERROR: set VERSION=x.y.z"; exit 1)
	$(MAKE) buildx-builder
	@echo "$(VERSION)" | grep -qE '^[0-9]+\.[0-9]+(\.[0-9]+)?$$' && { \
		MAJOR=$$(echo "$(VERSION)" | cut -d. -f1); \
	} || { MAJOR=; }
	@docker buildx build \
		--builder docker-release-builder \
		--platform linux/amd64,linux/arm64 \
		--build-arg VERSION=$(VERSION) \
		-t $(IMAGE):$(VERSION) \
		-t $(IMAGE):latest \
		$(if $(MAJOR),-t $(IMAGE):$(MAJOR)) \
		--push \
		. && $(MAKE) tag

buildx-builder:
	@docker buildx inspect docker-release-builder >/dev/null 2>&1 || \
		docker buildx create --name docker-release-builder --driver docker-container --use

# Install the Docker CLI plugin (docker release <cmd>)
dev:
	mkdir -p ~/.docker/cli-plugins
	ln -sf $(PWD)/scripts/docker-release ~/.docker/cli-plugins/docker-release
	chmod +x scripts/docker-release
	@echo "Done — run: docker release <service>"

dev-remove:
	rm -f ~/.docker/cli-plugins/docker-release
	@echo "Removed docker release dev plugin"

test:
	go test ./...

up-nginx:
	docker compose -f tests/nginx/docker-compose.yml up --build

up-angie:
	docker compose -f tests/angie/docker-compose.yml up --build

up-traefik:
	docker compose -f tests/traefik/docker-compose.yml up --build

up-nginx-proxy:
	docker compose -f tests/nginx-proxy/docker-compose.yml up --build

up-caddy:
	docker compose -f tests/caddy/docker-compose.yml up --build

up-haproxy:
	docker compose -f tests/haproxy/docker-compose.yml up --build

down-nginx:
	docker compose -f tests/nginx/docker-compose.yml down -v

down-angie:
	docker compose -f tests/angie/docker-compose.yml down -v

down-traefik:
	docker compose -f tests/traefik/docker-compose.yml down -v

down-nginx-proxy:
	docker compose -f tests/nginx-proxy/docker-compose.yml down -v

down-caddy:
	docker compose -f tests/caddy/docker-compose.yml down -v

down-haproxy:
	docker compose -f tests/haproxy/docker-compose.yml down -v
