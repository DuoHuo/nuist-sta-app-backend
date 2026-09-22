# 明德楼服务器发布记录

2026-09-21 已部署并通过公网接口验收。

- 公网 API：`http://202.195.237.186:12345`
- 端口链路：公网 12345 → 虚拟机 8080 → API 容器 8080。
- 管理台：`http://202.195.237.186:12345/admin/`
- 查看器：`http://202.195.237.186:12345/admin/model-viewer.html?building_id=OSM-Way862952692&floor=all`
- 建筑 ID：`OSM-Way862952692`
- 模型版本：`4af7c02f97d36a5f55c78db167f2384f`
- 发布镜像：`campus-api:mingde-20260921`

## 服务器文件

项目：`/home/a350_ti/nuist-sta-app-backend`

`docker-compose.yml` 的 API 端口已改为 `8080:8080`；`docker-compose.override.yml` 指定本次镜像，并挂载 `./data/models:/models`，配置 `CAMPUS_MODEL_STORAGE_DIR=/models`。

GLB 持久化目录：`/home/a350_ti/nuist-sta-app-backend/data/models`

发布包与备份：`/home/a350_ti/campus-releases/mingde-20260921`

- `before.dump`：更新前 PostgreSQL 完整逻辑备份。
- `compose.before.yml`：更新前 Compose 配置（权限受限）。
- `image.before.txt`：更新前 API 镜像 ID。
- `payload/api`：Linux amd64 静态后端程序，包含管理台和本地 three.js。
- `payload/migrations`：数据库迁移。
- `payload/data/mingde`：GLB、manifest、GeoJSON、室内 SQL 与服务器验收结果。
- `payload/scripts`：生成、上传、验证脚本；不保存登录凭据。

未重建数据库或地图瓦片服务。已应用模型存储表迁移，并向原明德楼追加七层室内数据。服务器原明德楼无楼层，因此没有覆盖已有室内数据。

## API

- `POST /api/v1/admin/buildings/:id/model`：multipart `file`、`manifest`、可选 `floor_0` 等。沿用 `X-Collect-Token`。
- `GET /api/v1/buildings/:id/model`：当前模型版本、楼层和下载清单。
- 下载采用版本化 URL，支持校验哈希、ETag、Range。
- 上传 GLB 不自动改写导航数据；房间/导航由 `import-indoor.sql` 单独导入。

## 验证

- 后端 `go test ./...` 通过。
- Flutter：51 通过、1 跳过；analyze 无问题。未在 Android/iOS 真机安装验收。
- 整栋与七个分层 GLB 下载 SHA-256 与源文件一致。
- 公网 health、模型清单和查看器均 HTTP 200。
- 七层、60 个教室及 39 个设施区域可读，房间均有 POI/导航节点绑定。
- 同层、1F→7F、7F→1F、室外起点至7F均通过；上下楼各六次换层，无障碍请求不会走楼梯。
- 已修复 pgRouting 出边与节点错配导致的换层错位、距离漏算和逆向几何折返。

模型依照消防图估算，非实测；详细假设见 README.md。
