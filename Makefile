BINARY_NAME := calbooker
GO := go
# The goweb checkout that runs PostgreSQL, Hydra, and the provider locally.
GOWEB_DIR ?= ../goweb

.PHONY: build clean fmt-check test test-integration vet assets check run dev-setup e2e demo-seed

build:
	mkdir -p bin
	$(GO) build -o bin/$(BINARY_NAME) ./cmd/web

assets:
	npm run build:css
	npm run check:assets

clean:
	rm -rf bin coverage.out coverage.html

fmt-check:
	test -z "$$(gofmt -l .)"

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

check: assets fmt-check vet test

# Creates the calbooker database in goweb's PostgreSQL and registers the
# development client in goweb's Hydra; writes the git-ignored .env.local (the
# client secret changes on every run).
dev-setup:
	@umask 077; GOWEB_DIR=$(GOWEB_DIR) scripts/dev-setup.sh > .env.local
	@echo "wrote .env.local; start calbooker with: make run"

# .env holds local defaults, .env.local the database and client settings.
run:
	@set -a; if [ -f .env ]; then . ./.env; fi; if [ -f .env.local ]; then . ./.env.local; fi; set +a; $(GO) run ./cmd/web

# Integration tests in throwaway schemas of the database from .env.local.
test-integration:
	@set -a; . ./.env.local; set +a; $(GO) test -tags=integration -count=1 ./...

# Browser specs against the running stack: goweb's Hydra and provider, and
# calbooker (make run).
e2e:
	@set -a; if [ -f .env ]; then . ./.env; fi; if [ -f .env.local ]; then . ./.env.local; fi; set +a; \
	CALBOOKER_SSO_E2E=1 npx playwright test

# Wipes this checkout's database (from .env.local) and fills it with demo
# data; creates the demo accounts at the provider on first use.
demo-seed:
	@set -a; if [ -f .env ]; then . ./.env; fi; if [ -f .env.local ]; then . ./.env.local; fi; set +a; $(GO) run ./cmd/demo-seed
