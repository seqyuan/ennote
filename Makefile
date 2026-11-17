.PHONY: dev dev-worker build lint typecheck test test-go test-e2e check-toolchain clean

# Every target that needs Node or Go states it here rather than failing later with
# an error that looks like a broken test.
check-toolchain:
	@node scripts/check-toolchain.mjs --node --go

dev: check-toolchain
	npm run build:web
	cd ennoworker && ENNOTE_HOSTNAME=127.0.0.1 ENNOTE_STATIC_DIR="$(CURDIR)/out" ENNOTE_BUILTIN_SKILLS_DIR="$(CURDIR)/skills" go run ./cmd/ennogate

dev-worker: check-toolchain
	cd ennoworker && ENNOTE_BUILTIN_SKILLS_DIR="$(CURDIR)/skills" go run ./cmd/ennoworker

build: check-toolchain
	npm run build
	cd ennoworker && go build -o ennogate ./cmd/ennogate && go build -o ennoworker ./cmd/ennoworker

lint:
	npm run lint

typecheck:
	npm run typecheck

test: check-toolchain
	npm test

test-go: check-toolchain
	cd ennoworker && go test ./...

test-e2e: check-toolchain
	npm run test:e2e

clean:
	rm -rf .next node_modules ennoworker/ennogate ennoworker/ennoworker
