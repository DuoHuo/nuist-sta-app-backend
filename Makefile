.PHONY: run test vet tidy fmt compose-up compose-down migrate-up migrate-down seed

run:
	go run ./cmd/server

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

fmt:
	gofmt -l -w .

compose-up:
	docker compose up -d --build

compose-down:
	docker compose down

migrate-up:
	go run ./cmd/migrate up

migrate-down:
	go run ./cmd/migrate down 1

seed:
	go run ./cmd/seed
