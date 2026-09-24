FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
# 中国大陆服务器访问不了 proxy.golang.org，走 goproxy.cn（对境外构建也与直连等效）
RUN GOPROXY=https://goproxy.cn,direct go mod download
COPY . .
RUN go test ./...   # 构建即验证：测试挂了就不出镜像
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/server

# 运行阶段用 alpine 而非 gcr.io/distroless：部署服务器（校园网）访问不了 gcr.io。
# 65534:65534 = nobody，与 distroless 的 nonroot 等价。
# 若部署环境能访问 gcr.io，可换回 distroless/static-debian12:nonroot（体积更小、无 shell）。
FROM alpine:3.21
USER 65534:65534
WORKDIR /
COPY --from=build /out/api /api
COPY migrations /migrations
ENTRYPOINT ["/api"]
