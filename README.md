# nuist-sta-app-backend（地图后端）

校园地图专用后端：**建筑/楼层/地点服务、室内外一体化路线规划、Wi-Fi 指纹定位基线**。
其余业务模块（认证、通知等）已移出，旧代码保留在 `archive/pre-map-rewrite` 分支。

技术栈：Go / Gin · PostgreSQL + PostGIS + pgRouting · Martin（矢量瓦片，独立容器）。
App 端使用 Flutter + `maplibre_gl`，2.5D 建筑用 `fill-extrusion` 渲染。

**部署到服务器请看 [docs/deploy.md](docs/deploy.md)。**

## 架构

```
Flutter App ──业务接口──▶ api (Go/Gin :8080) ──▶ PostgreSQL+PostGIS+pgRouting
       └────矢量瓦片────▶ martin (:3000) ──▶ data/tiles/campus.mbtiles
```

- 底图：OSM 原始数据 → pyosmium 转 PBF → tilemaker 生成 MBTiles → Martin 发布（实测命令见 deploy.md 第 5 节）；
- 样式：`configs/style.osm-bright.json`（基于 openmaptiles/osm-bright-gl-style，字形/图标走 OpenFreeMap），
  瓦片源为相对路径，martin 从任意 host:端口被访问都自动指向自身；api 另提供同源代理 `/martin/*`；
- 室内数据：首版直接走本仓库 GeoJSON 接口（每次只加载当前楼层），不做室内瓦片；
- 高斯街景：`buildings.splat_scene_id` 预留，空值时 App 打开占位页。

## 目录

```
cmd/server       HTTP 服务入口（-auto-migrate / -seed-demo）
cmd/migrate      迁移工具（up / down N / status）
cmd/seed         演示数据种子（幂等）
migrations/      SQL 迁移（数据模型 + 注释即文档）
internal/
  geo/           WGS84 <-> 本地米制坐标变换、GeoJSON 构造
  modules/mapdata  建筑、楼层、室内要素、POI、地图配置
  modules/routing  pgRouting 最短路径 + 路线分段（室外/室内/换层）
  modules/locate   指纹入库 + WKNN 定位基线
  seed/          演示数据（一栋楼 × 两层 × 完整路网 × 指纹样本）
scripts/         数据工具：fetch_osm.py（Overpass 抓取南信大校园数据）、osm_to_geojson.py（分层转换）
data/osm/        南信大主校区真实 OSM 数据（边界/建筑/路网/POI，说明见 data/osm/README.md）
configs/         config.example.yaml、martin 配置、最小演示样式
deployments/     api 与 postgres(含 pgRouting) 的 Dockerfile
docs/deploy.md   部署档案
```

## 本地开发

```bash
cp configs/config.example.yaml configs/config.yaml   # 按需修改
make run          # 需要先有数据库；或直接 docker compose up -d db
make test
```

## API 一览（前缀 /api/v1，响应包裹 {code,message,data}）

**管理台**：`/admin`（api 服务器内嵌静态页面，无需额外部署）——
数据概览 / 地图图层总览（MapLibre，资源已本地化，无 CDN 依赖）/
建筑与地点档案编辑 / 指纹记录浏览。写接口与指纹采集共用
`X-Collect-Token`（配置 `collect_token` 后生效，管理台左下角可设置令牌）。
管理端点：`GET /admin/stats`、`GET /admin/{buildings/geometry,graph,pois/geometry}`、
`PATCH /admin/buildings/:id`、`PATCH /admin/pois/:id`。

| 方法 | 路径 | 说明 |
| ---- | ---- | ---- |
| GET | /healthz | 存活检查（含数据库 ping） |
| GET | /map/config | 瓦片/样式地址、范围、OSM 署名 |
| GET | /buildings?q=&bbox= | 建筑列表（名称/别名搜索、范围过滤） |
| GET | /buildings/:id | 详情：楼层清单、出入口、高度及来源、splat_scene_id |
| GET | /buildings/:id/geometry | 轮廓 + 出入口 FeatureCollection |
| GET | /buildings/:id/floors | 楼层列表（level_index / display_name / elevation 独立） |
| GET | /floors/:floorId/features?kinds=room,door | 当前楼层室内要素 GeoJSON |
| GET | /pois?q=&building_id=&category= | 地点搜索（名称/分类/关键词） |
| GET | /pois/:id | 地点详情 |
| GET | /buildings/:id/photos | 建筑实拍图片列表（元数据，图床地址为 /photos/<file>） |
| POST | /admin/buildings/:id/photos | 上传实拍图片（multipart，需 X-Collect-Token） |
| DELETE | /admin/photos/:photoId | 删除实拍图片（需 X-Collect-Token） |
| GET | /photos/<file> | 实拍图片静态文件（内容哈希命名，可长缓存） |
| POST | /route | 路线规划（起点/终点支持 node_id / poi_id / lng+lat / building+level） |
| POST | /fingerprints | 指纹采集上报（可配 X-Collect-Token） |
| GET | /fingerprints?building_id=&floor_id= | 采集记录列表 |
| POST | /locate/wifi | WKNN 定位：返回楼栋/楼层/位置/可信度 |

### 路线规划示例

```bash
curl -X POST http://localhost:8080/api/v1/route \
  -H "Content-Type: application/json" \
  -d '{
    "origin":      {"poi_id": 8},
    "destination": {"poi_id": 4},
    "options":     {"accessible_only": false}
  }'
```

返回按 `outdoor`（室外段）/ `indoor`（某建筑某楼层段）/ `floor_change`
（楼梯/电梯/坡道换层动作）分段，每段含长度、文字指令与 GeoJSON LineString，
`steps` 为完整步骤列表。App 室内界面只渲染当前相关楼层的段。

### 定位返回约定

`/locate/wifi` 的 `estimate` 带 `confidence`（0~1）与 `label`
（high/medium/low）。**低可信度时 App 应引导用户确认位置，不要画精确蓝点**；
"用户正在浏览的楼层"与"定位所在楼层"是两个状态，由 App 维护。

## 数据模型关键约定

- **建筑**：自有稳定 `building_id`；`osm_id` 仅作关联可空。`height_m` 必须带
  `height_source`（measured / estimated_floors / display_default），估计值不得当测绘结果。
- **楼层**：`level_index`（内部序号，地面=0，向下-1）/ `display_name`（B1/1F/2F）/
  `elevation_m` 三者独立，UI 显示名不得由程序推导。
- **坐标**：接口交换一律 WGS84；室内距离与指纹用本地米制坐标，
  变换锚点存 `campus_cs`（全库唯一），Go 侧 `internal/geo` 实现同一套换算。
- **导航图**：室内外同图（`nav_nodes` / `nav_edges`）。跨层必须经
  `floor_change=true` 的楼梯/电梯/坡道边；边记录成本（米）、通行方向
  （reverse_cost=-1 禁逆行）、无障碍属性、开闭状态。
- **指纹**：`fp_sessions`（位置 + 时间 + 设备 + 朝向）+ `fp_observations`
  （BSSID→RSSI）。用 BSSID 区分 AP，SSID 仅人工核对。

## 阶段对齐

| 阶段 | 本仓库现状 |
| ---- | ---- |
| 能力验证（Android Wi-Fi 扫描） | App 侧任务；后端已提供指纹上报/定位接口 |
| 校园地图（2.5D、点击建筑） | 建筑接口 + Martin 瓦片流水线（deploy.md 第 5 节）；校园真实 OSM 数据已就位（`data/osm/`，抓取脚本 `scripts/fetch_osm.py`） |
| 室内导航样板（一栋楼×两层） | `cmd/seed` 演示数据即样板，路线分段可验收 |
| 定位样板（指纹+WKNN） | locate 模块 + 演示指纹库 |
| 扩展覆盖 | 数据生产问题：QGIS 导入 PostGIS，表结构见迁移注释 |
