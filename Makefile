VERSION ?= $(shell v=$$(git tag --points-at HEAD 2>/dev/null | head -1); echo $${v:-dev})
IMAGE   ?= malico/docker-release
NGINX_IMAGE ?= $(IMAGE)-nginx
ANGIE_IMAGE ?= $(IMAGE)-angie
CADDY_IMAGE ?= $(IMAGE)-caddy
HAPROXY_IMAGE ?= $(IMAGE)-haproxy
TRAEFIK_IMAGE ?= $(IMAGE)-traefik

PROXIES := nginx angie caddy haproxy traefik
# Test stacks under tests/ that have up-<name> and down-<name> targets.
STACKS := nginx nginx-bundled angie-bundled caddy-bundled haproxy-bundled traefik-bundled angie traefik nginx-proxy caddy haproxy

BUILD_TARGETS := $(addprefix build-,$(PROXIES))
UP_TARGETS := $(addprefix up-,$(STACKS))
DOWN_TARGETS := $(addprefix down-,$(STACKS))

# bundle_image <proxy>: the image name for a bundled proxy, e.g. NGINX_IMAGE.
bundle_image = $($(shell echo $(1) | tr a-z A-Z)_IMAGE)

# Bump type passed as a goal: make publish major|minor|fix
BUMP := $(filter major minor fix,$(MAKECMDGOALS))

.PHONY: dev dev-remove test lint build build-regular publish tag buildx-builder \
	major minor fix print-version $(BUILD_TARGETS) $(UP_TARGETS) $(DOWN_TARGETS)

build: build-regular $(BUILD_TARGETS)

build-regular:
	docker build \
		--build-arg VERSION=$(VERSION) \
		-t $(IMAGE):$(VERSION) \
		-t $(IMAGE):latest \
		.

$(BUILD_TARGETS): build-%:
	docker build \
		-f dockerfiles/$*.Dockerfile \
		--build-arg VERSION=$(VERSION) \
		-t $(call bundle_image,$*):$(VERSION) \
		-t $(call bundle_image,$*):latest \
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
	publish_image() { \
		docker buildx build \
			--builder docker-release-builder \
			--platform linux/amd64,linux/arm64 \
			$$2 \
			--build-arg VERSION=$(VERSION) \
			-t $$1:$(VERSION) \
			-t $$1:latest \
			$${major:+-t $$1:$$major} \
			--push \
			.; \
	}; \
	publish_image $(IMAGE) "--target runtime" && \
	$(foreach p,$(PROXIES),publish_image $(call bundle_image,$(p)) "-f dockerfiles/$(p).Dockerfile" && ) \
	$(MAKE) tag
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
	go test -tags bundled_haproxy ./...
	go test -tags bundled_traefik ./...
	go -C packaging/caddy/runtime test -p 2 ./...

lint:
	@test -z "$$(gofmt -l $(if $(FILES),$(FILES),cmd internal packaging))"
	go vet ./...
	go vet -tags bundled_nginx ./...
	go vet -tags bundled_angie ./...
	go vet -tags bundled_caddy ./...
	go vet -tags bundled_haproxy ./...
	go vet -tags bundled_traefik ./...
	go -C packaging/caddy/runtime vet -p 2 ./...

$(UP_TARGETS): up-%:
	docker compose -f tests/$*/docker-compose.yml up --build

$(DOWN_TARGETS): down-%:
	docker compose -f tests/$*/docker-compose.yml down -v
