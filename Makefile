.PHONY: run build test lint docker-up docker-down coverage tidy sec migrate migrate-down migrate-version migrate-force

run:
	go run cmd/api/main.go

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/api cmd/api/main.go

test:
	go test -race ./...

# Schema changes go through embedded SQL (migrations/*.sql).
migrate:
	go run ./cmd/migrate -dir up

migrate-down:
	go run ./cmd/migrate -dir down -steps 1

migrate-version:
	go run ./cmd/migrate -dir version

migrate-force:
	@test -n "$(V)" || (echo "usage: make migrate-force V=<version>"; exit 1)
	go run ./cmd/migrate -dir force -version $(V)

coverage:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	go tool cover -func=coverage.out | tail -1

lint:
	golangci-lint run ./...

docker-up:
	docker-compose up -d

docker-down:
	docker-compose down

tidy:
	go mod tidy

sec:
	gosec ./...
	govulncheck ./...
