VERSION ?= $(shell v=$$(git tag --points-at HEAD 2>/dev/null | head -1); echo $${v:-dev})
IMAGE   ?= malico/docker-release
# Major part of VERSION when it is x.y or x.y.z; empty otherwise.
MAJOR := $(shell echo "$(VERSION)" | grep -E '^[0-9]+\.[0-9]+(\.[0-9]+)?$$' | cut -d. -f1)

PROXIES := nginx angie caddy haproxy traefik
# Test stacks under tests/ that have up-<name> and down-<name> targets.
STACKS := nginx nginx-bundled angie-bundled caddy-bundled haproxy-bundled traefik-bundled angie traefik nginx-proxy caddy haproxy

BUILD_TARGETS := $(addprefix build-,$(PROXIES))
UP_TARGETS := $(addprefix up-,$(STACKS))
DOWN_TARGETS := $(addprefix down-,$(STACKS))

# bundle_tags <proxy>: -t flags for a bundle, all in the one $(IMAGE) repo:
# <proxy> (newest), <version>-<proxy> and, for a release version, <major>-<proxy>.
bundle_tags = -t $(IMAGE):$(1) -t $(IMAGE):$(VERSION)-$(1) $(if $(MAJOR),-t $(IMAGE):$(MAJOR)-$(1))

# Bump type passed as a goal: make publish major|minor|fix
BUMP := $(filter major minor fix,$(MAKECMDGOALS))

# Images checked by `make scan`: the plain controller and the five bundles.
SCAN_TARGETS := scan-regular $(addprefix scan-,$(PROXIES))

.PHONY: dev dev-remove test lint build build-regular scan publish tag buildx-builder \
	major minor fix print-version $(SCAN_TARGETS) $(BUILD_TARGETS) $(UP_TARGETS) $(DOWN_TARGETS)

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
		$(call bundle_tags,$*) \
		.

# scan_image <name> <docker build args>: build the image locally, fail when it has
# a critical or high CVE that already has a fix, then remove the local image.
# Docker Scout scans the platform of this machine.
define scan_image
	docker build --pull $(2) -t $(IMAGE):scan-$(1) .
	@docker scout cves --only-severity critical,high --only-fixed --exit-code local://$(IMAGE):scan-$(1); \
	status=$$?; docker rmi $(IMAGE):scan-$(1) >/dev/null; exit $$status
endef

scan: $(SCAN_TARGETS)

scan-regular:
	$(call scan_image,regular,)

$(filter-out scan-regular,$(SCAN_TARGETS)): scan-%:
	$(call scan_image,$*,-f dockerfiles/$*.Dockerfile)

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
	$(MAKE) scan
	$(MAKE) buildx-builder
	@publish_image() { \
		docker buildx build \
			--builder docker-release-builder \
			--platform linux/amd64,linux/arm64 \
			$$1 \
			--build-arg VERSION=$(VERSION) \
			$$2 \
			--push \
			.; \
	}; \
	publish_image "--target runtime" "-t $(IMAGE):$(VERSION) -t $(IMAGE):latest $(if $(MAJOR),-t $(IMAGE):$(MAJOR))" && \
	$(foreach p,$(PROXIES),publish_image "-f dockerfiles/$(p).Dockerfile" "$(call bundle_tags,$(p))" && ) \
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
