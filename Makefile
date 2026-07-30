.PHONY: dev dev-worker build lint typecheck test test-go test-e2e clean

dev:
	npm run build:web
	cd ennoworker && ENNOTE_HOSTNAME=127.0.0.1 ENNOTE_STATIC_DIR="$(CURDIR)/out" go run ./cmd/ennogate

dev-worker:
	cd ennoworker && go run ./cmd/ennoworker

build:
	npm run build
	cd ennoworker && go build -o ennogate ./cmd/ennogate && go build -o ennoworker ./cmd/ennoworker

lint:
	npm run lint

typecheck:
	npm run typecheck

test:
	npm test

test-go:
	cd ennoworker && go test ./...

test-e2e:
	npm run test:e2e

clean:
	rm -rf .next node_modules ennoworker/ennogate ennoworker/ennoworker
