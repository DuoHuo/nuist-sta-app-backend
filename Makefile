.PHONY: run test vet tidy fmt compose-up compose-down migrate-up migrate-down seed deploy deploy-status

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

# 线上服务器部署：手册见 .agents/skills/backend-deploy/SKILL.md
# make deploy-status 体检 / make deploy 上传后重建 api
DEPLOY := python ../.agents/skills/backend-deploy/scripts/deploy.py

deploy-status:
	$(DEPLOY) status

deploy:
	$(DEPLOY) rebuild api
