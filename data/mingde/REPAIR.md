# 模型接口恢复记录（2026-09-21）

## 故障与修复

线上模型清单和GLB下载返回 `404 page not found`。服务器构建目录缺少 models 模块、注册、配置及000002迁移；后续从该目录重新构建覆盖了此前正确的镜像。

已补齐服务器 `/home/a350_ti/nuist-sta-app-backend` 下完整模型模块、路由注册、上传请求体限制、存储配置、迁移文件和测试；恢复导航出边配对与逆向几何修复、查看器房间筛选。逐文件比对后同步，保留服务器现有代理、管理台、Compose端口、数据库连接配置及Alpine/GOPROXY构建方式。

服务器 `deployments/Dockerfile` 在编译前执行 `RUN go test ./...`，包含模型路由注册测试。此次实际使用服务器源码执行 `docker compose build api`，全部测试通过后执行 `docker compose up -d --no-deps --no-build api`，不是仅替换一个预编译程序。

## 当前部署约定

- 公网 12345 → 虚拟机 8080 → 容器 8080。
- 服务器Compose保留 `8080:8080`；本地开发Compose配置不能整文件覆盖服务器配置。
- 服务器 `docker-compose.override.yml` 保留 `campus-api:mingde-20260921` 镜像和 `./data/models:/models` 持久化挂载。
- 以后重建前先同步并核对最新源码；构建失败不得更新运行容器。
- 模型版本仍为 `4af7c02f97d36a5f55c78db167f2384f`，没有重新上传或覆盖模型、房间和导航数据。

## 验证

- 服务器构建内 `go test ./...` 全部通过。
- 公网模型清单恢复200，整栋及七个分层文件下载 SHA-256 全部一致。
- 上传接口已注册，空请求返回结构化400而非路由404；失败请求不改变模型版本。
- 七层/60房间、同层导航、1F↔7F正确六次换层、室外坐标起点导航、无障碍跨层限制通过。

## 恢复前备份

`/home/a350_ti/campus-releases/model-repair-20260921/`

- `source-before.tar.gz`：恢复前源码和配置，权限受限。
- `db-before.dump`：数据库备份。
- `image-before.txt`：恢复前镜像标识。
- `build.log`：本次服务器测试构建日志。
- 旧镜像另保存为 `campus-api:before-model-repair-20260921`。

仅恢复API容器；数据库和地图瓦片容器未重建。
