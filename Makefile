VERSION ?= $(shell v=$$(git tag --points-at HEAD 2>/dev/null | head -1); echo $${v:-dev})
IMAGE   ?= malico/docker-release
NGINX_IMAGE ?= $(IMAGE)-nginx
ANGIE_IMAGE ?= $(IMAGE)-angie
CADDY_IMAGE ?= $(IMAGE)-caddy

# Bump type passed as a goal: make publish major|minor|fix
BUMP := $(filter major minor fix,$(MAKECMDGOALS))

.PHONY: dev dev-remove test lint build build-regular build-nginx build-angie build-caddy publish tag buildx-builder \
	major minor fix print-version \
	up-nginx up-nginx-bundled up-angie-bundled up-caddy-bundled up-angie up-traefik up-nginx-proxy up-caddy up-haproxy \
	down-nginx down-nginx-bundled down-angie-bundled down-caddy-bundled down-angie down-traefik down-nginx-proxy down-caddy down-haproxy

build: build-regular build-nginx build-angie build-caddy

build-regular:
	docker build \
		--build-arg VERSION=$(VERSION) \
		-t $(IMAGE):$(VERSION) \
		-t $(IMAGE):latest \
		.

build-nginx: BUNDLE_IMAGE = $(NGINX_IMAGE)
build-angie: BUNDLE_IMAGE = $(ANGIE_IMAGE)
build-caddy: BUNDLE_IMAGE = $(CADDY_IMAGE)
build-nginx build-angie build-caddy:
	docker build \
		-f dockerfiles/$(patsubst build-%,%,$@).Dockerfile \
		--build-arg VERSION=$(VERSION) \
		-t $(BUNDLE_IMAGE):$(VERSION) \
		-t $(BUNDLE_IMAGE):latest \
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

# Swallow bump goals so `make publish minor` doesn't error on `minor`.
major minor fix:
	@:

# next-version: bump latest v<semver> git tag by $(BUMP). 0.0.0 if no tags.
define next_version
	git fetch --tags origin 2>/dev/null; \
	latest=$$(git tag -l 'v[0-9]*.[0-9]*.[0-9]*' | sort -V | tail -1); \
	cur=$${latest#v}; cur=$${cur:-0.0.0}; \
	major=$$(echo "$$cur" | cut -d. -f1); \
	minor=$$(echo "$$cur" | cut -d. -f2); \
	patch=$$(echo "$$cur" | cut -d. -f3); \
	case "$(BUMP)" in \
		major) next=$$((major+1)).0.0 ;; \
		minor) next=$$major.$$((minor+1)).0 ;; \
		fix)   next=$$major.$$minor.$$((patch+1)) ;; \
	esac
endef

print-version:
	@if [ -n "$(BUMP)" ]; then \
		$(next_version); \
		echo "current: $$cur  next ($(BUMP)): $$next"; \
	else \
		echo "$(VERSION)"; \
	fi

publish:
ifneq ($(BUMP),)
	@$(next_version); \
	echo "Publishing v$$next (current: v$$cur, bump: $(BUMP))"; \
	$(MAKE) publish VERSION=$$next
else
	@test "$(VERSION)" != "dev" || (echo "ERROR: set VERSION=x.y.z or use: make publish major|minor|fix"; exit 1)
	$(MAKE) buildx-builder
	@major=$$(echo "$(VERSION)" | grep -E '^[0-9]+\.[0-9]+(\.[0-9]+)?$$' | cut -d. -f1); \
	docker buildx build \
		--builder docker-release-builder \
		--platform linux/amd64,linux/arm64 \
		--target runtime \
		--build-arg VERSION=$(VERSION) \
		-t $(IMAGE):$(VERSION) \
		-t $(IMAGE):latest \
		$${major:+-t $(IMAGE):$$major} \
		--push \
		. && \
	docker buildx build \
		--builder docker-release-builder \
		--platform linux/amd64,linux/arm64 \
		-f dockerfiles/nginx.Dockerfile \
		--build-arg VERSION=$(VERSION) \
		-t $(NGINX_IMAGE):$(VERSION) \
		-t $(NGINX_IMAGE):latest \
		$${major:+-t $(NGINX_IMAGE):$$major} \
		--push \
		. && \
	docker buildx build \
		--builder docker-release-builder \
		--platform linux/amd64,linux/arm64 \
		-f dockerfiles/angie.Dockerfile \
		--build-arg VERSION=$(VERSION) \
		-t $(ANGIE_IMAGE):$(VERSION) \
		-t $(ANGIE_IMAGE):latest \
		$${major:+-t $(ANGIE_IMAGE):$$major} \
		--push \
		. && \
	docker buildx build \
		--builder docker-release-builder \
		--platform linux/amd64,linux/arm64 \
		-f dockerfiles/caddy.Dockerfile \
		--build-arg VERSION=$(VERSION) \
		-t $(CADDY_IMAGE):$(VERSION) \
		-t $(CADDY_IMAGE):latest \
		$${major:+-t $(CADDY_IMAGE):$$major} \
		--push \
		. && $(MAKE) tag
endif

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
	go test -tags bundled_nginx ./...
	go test -tags bundled_angie ./...
	go test -tags bundled_caddy ./...
	go -C packaging/caddy/runtime test -p 2 ./...

lint:
	@test -z "$$(gofmt -l $(if $(FILES),$(FILES),cmd internal packaging))"
	go vet ./...
	go vet -tags bundled_nginx ./...
	go vet -tags bundled_angie ./...
	go vet -tags bundled_caddy ./...
	go -C packaging/caddy/runtime vet -p 2 ./...

up-nginx:
	docker compose -f tests/nginx/docker-compose.yml up --build

up-nginx-bundled up-angie-bundled up-caddy-bundled:
	docker compose -f tests/$(patsubst up-%,%,$@)/docker-compose.yml up --build

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

down-nginx-bundled down-angie-bundled down-caddy-bundled:
	docker compose -f tests/$(patsubst down-%,%,$@)/docker-compose.yml down -v

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
