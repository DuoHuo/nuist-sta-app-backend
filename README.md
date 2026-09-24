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
- 校园公交：线路/站点/车辆位置存 `bus_*` 表（migration 000005），App 走 `/bus/*` 与 `/bus/geometry`
  自绘；要并进底图时由 Martin 从 PostGIS 直发这两张表（`configs/style.bus-layers.json` 是预留样式片段，
  默认不开，步骤见 deploy.md 5.1）。车辆位置是轮询接口，不进瓦片；
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
  modules/bus      校园公交：线路、站点、站序、车辆注册与实时位置
  modules/routing  pgRouting 最短路径 + 路线分段（室外/室内/换层）
  modules/locate   指纹入库 + WKNN 定位基线
  seed/          演示数据（一栋楼 × 两层 × 完整路网 × 指纹样本 × 一条公交环线）
scripts/         数据工具：fetch_osm.py（Overpass 抓取南信大校园数据）、osm_to_geojson.py（分层转换）
data/osm/        南信大主校区真实 OSM 数据（边界/建筑/路网/POI，说明见 data/osm/README.md）
configs/         config.example.yaml、martin 配置、最小演示样式、公交图层预留片段
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
建筑、地点、**通用地物**与**校园公交**（线路/站点/车辆）的编辑 / 指纹记录浏览。
地图上可直接绘制并提交：点地物、面地物（道路/绿地/广场等）、建筑轮廓与
公交线路走向；几何入库前会校验类型、坐标范围、`ST_IsValid` 与面积上限，
拒绝时给出 PostGIS 的原始原因。

写接口与指纹采集共用 `X-Collect-Token`，**未配置令牌时写接口一律返回 503**
（fail-closed，不再静默放行）；令牌可写成 `名字:令牌`，名字会记入
`created_by`，管理台「提交人」列由此而来。管理台左下角可设置令牌。
管理端点：`GET /admin/stats`、`GET /admin/{buildings/geometry,graph,pois/geometry,features,features/geometry}`、
`PATCH /admin/buildings/:id`、`POST /admin/buildings`、`PATCH /admin/buildings/:id/geometry`、
`PATCH|DELETE /admin/pois/:id`、`POST|PATCH|DELETE /admin/features[/:id]`、
`GET /admin/bus/{routes,routes/:id,stops,vehicles,geometry}`（公交，读接口开放，含草稿）、
`POST|PATCH|DELETE /admin/bus/{routes[/:id],stops[/:id],vehicles[/:vehicleId]}` 与
`PUT /admin/bus/routes/:id/stops`（整体替换站序）。

### 管理台扩展点

管理台是手写的无构建 SPA（`internal/webui/dist/`，原生 JS + 本地 vendor 的 MapLibre，
`go:embed` 进二进制）。加业务**不要改 app.js 的骨架**，按三种注册接入——`admin-bus.js`
就是范例（它一个文件提供 #/bus 页面 + 地图三层 + 两个绘制任务，app.js 里没有一行公交代码）：

| 想加什么 | 怎么注册 | 参考 |
| ---- | ---- | ---- |
| 一个页面 | `AdminKit.registerView("bus", view)`，视图签名 `async view(root, params)`；导航项写在 `index.html` 的 `#nav` | `admin-bus.js` 的 `view()` |
| 一层地图数据 | `AdminKit.registerMapPlugin({ id, dataKey, label, color, on, load(), layers[], popup(), remove(), focusId(), refreshMs })` | 内置的 buildings/pois/features 与公交三层 |
| 一个绘制任务 | `AdminKit.registerDrawTask(id, params => ({ type:"point"\|"line"\|"polygon", hint, minPoints, onFinish(geometry) }))`，入口 `#/map?draw=<id>` | `bus-route` / `bus-stop` |

两条硬约定：

- **数据变了只调 `AdminKit.dataChanged(<dataKey>)`**（公交三层共用 `"bus"`，全量用 `"all"`）。
  它负责清列表缓存、失效几何取数、并让已挂载的地图就地重取该图层；模块自己的列表缓存用
  `AdminKit.onDataChanged(hit => …)` 挂进来。不要各自去 null 缓存——历史上就是四处各清一遍才出的岔子。
- 模块文件在 app.js **之后**加载（见 `index.html` 的 script 顺序），只用 `window.AdminKit`
  暴露的 API；视图注册后若当前 URL 已指向该视图，调一次 `AdminKit.reroute()`。


| 方法 | 路径 | 说明 |
| ---- | ---- | ---- |
| GET | /healthz | 存活检查（含数据库 ping） |
| GET | /map/config | 瓦片/样式地址、范围、OSM 署名、公交数据源与实时轮询契约 |
| GET | /buildings?q=&bbox= | 建筑列表（名称/别名搜索、范围过滤） |
| GET | /buildings/:id | 详情：楼层清单、出入口、高度及来源、splat_scene_id |
| GET | /buildings/:id/geometry | 轮廓 + 出入口 FeatureCollection |
| GET | /buildings/:id/floors | 楼层列表（level_index / display_name / elevation 独立） |
| GET | /floors/:floorId/features?kinds=room,door | 当前楼层室内要素 GeoJSON |
| GET | /pois?q=&building_id=&category= | 地点搜索（名称/分类/关键词） |
| GET | /pois/:id | 地点详情 |
| GET | /features?q=&kind=&limit= | 通用地物列表（道路/绿地/广场等，含几何；只出 published；`show_name=false` 表示地图上别标名称） |
| GET | /features/:id | 通用地物详情（App 详情页按编号取数） |
| POST | /admin/features | 新建通用地物（GeoJSON 点/线/面，需 X-Collect-Token） |
| PATCH | /admin/features/:id | 改档案或几何、切换 published/draft |
| DELETE | /admin/features/:id | 删除通用地物 |
| GET | /bus/routes?q=&geometry=1 | 公交线路列表（编号/名称/颜色/环线标记，可带走向） |
| GET | /bus/routes/:id | 线路详情：走向几何 + 按 seq 排好的站点 |
| GET | /bus/stops?q=&route_id=&lng=&lat= | 站点列表；给 lng+lat 时按距离升序并带 distance_m |
| GET | /bus/geometry | 线路（线）+ 站点（点）FeatureCollection，App 自绘图层直接用 |
| GET | /bus/vehicles?route_id=&max_age_s= | 实时位置：每车最新一条，默认只出 120 秒内有更新的车 |
| POST | /bus/positions | 车载设备上报位置（需 X-Collect-Token；车辆须先注册） |
| POST | /admin/bus/routes | 新建线路（可先不带走向；color 形如 #0b7285） |
| PATCH | /admin/bus/routes/:id | 改线路档案/走向/颜色/环线标记、切换 published/draft |
| DELETE | /admin/bus/routes/:id | 删除线路（站序随线路删除） |
| PUT | /admin/bus/routes/:id/stops | 整体替换站序：`{"stop_ids":[3,4,5,3]}`，seq 按下标生成；空数组清空 |
| POST | /admin/bus/stops | 新建站点（GeoJSON 点） |
| PATCH | /admin/bus/stops/:id | 改站名/位置/属性/状态 |
| DELETE | /admin/bus/stops/:id | 删除站点（线路里的站序一并删除） |
| POST | /admin/bus/vehicles | 注册车辆（vehicle_id 由车载设备自带，位置上报的前提） |
| PATCH | /admin/bus/vehicles/:vehicleId | 改显示名/值勤线路/启停（`{"enabled":false}` 即停运；`{"route_id":0}` 表示清空排班） |
| DELETE | /admin/bus/vehicles/:vehicleId | 删除车辆（位置历史随之删除；只想停运用 PATCH） |
| GET | /admin/bus/routes?status=&q= | 公交线路列表（管理台：含草稿、提交人、时间戳） |
| GET | /admin/bus/routes/:id | 管理台线路详情：含草稿线路与草稿站点、按 seq 排好的站点（排站序弹窗用） |
| GET | /admin/bus/stops?status=&q= | 公交站点列表（管理台：含草稿） |
| GET | /admin/bus/vehicles | 车辆注册表（含停运车辆与各自最新位置） |
| GET | /admin/bus/geometry | 线路 + 站点 FeatureCollection（含草稿，供管理台地图叠加） |
| POST | /admin/buildings | 新建建筑（轮廓 GeoJSON，编号留空自动生成 ADM-xxxxxxxx） |
| PATCH | /admin/buildings/:id/geometry | 重画建筑轮廓 |
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

### 公交数据约定

- **两条渲染路径，默认第一条**：App 读 `/bus/geometry`（FeatureCollection，线路
  `properties.layer='bus_route'`、站点 `'bus_stop'`）自绘，录入后立刻可见；要把公交并进
  底图时再让 Martin 从 PostGIS 直发 `bus_routes` / `bus_stops`，把
  `configs/style.bus-layers.json` 的 sources+layers 合并进样式（步骤见 deploy.md 5.1）。
  两条路径的表、字段名、图层名都一致，切换不需要改数据；
- **站点有两处**：`map_features` 里的 `bus_stop` 是"底图上的一个点"（可点击、有详情页），
  `bus_stops` 是公交业务里的站（有站序、能算"下一站"）。**不强制同步**——录入者可以只建其一；
- **站序**：`PUT /admin/bus/routes/:id/stops` 整体替换，`seq` 按下标生成。少于 2 站、
  中间站重复会被 422 拒绝；首末同站只允许环线（`is_loop=true`）。空数组表示清空站序；
- **实时位置**：车载设备先注册（`POST /admin/bus/vehicles`）再上报
  （`POST /bus/positions`，带令牌）。App 轮询 `/bus/vehicles`：默认只下发
  `max_age_s`（120 秒）内有更新的车；`max_age_s=0` 关闭过滤、全量下发并带 `age_s`
  （由服务器计算，不要用客户端时钟去减 `reported_at`）。**位置不进瓦片**；
- **底图预留**：`bus_routes.geom` 存 MultiLineString（接口收 LineString，入库 `ST_Multi` 归一）、
  `bus_stops.geom` 存 Point，都带 GiST 索引，SRID 4326——Martin 直发与 QGIS 导入都直接可用。

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
- **公交**：`bus_routes`（编号唯一、走向可空、`color` 用于上色）/ `bus_stops` /
  `bus_route_stops`（`seq` 从 0 起，**环线允许首末同站**，中间站不得重复）/
  `bus_vehicles`（注册表，位置上报的前提）+ `bus_positions`（时序，只追加，取每车最新一条）。
  位置表按需清理（建议留 7 天）；线路/站点的 `status` 与地物同一套语义（draft 不下发 App）。
- **通用地物**：两个来源——管理台绘制（`source='admin'`）与 OSM 导入
  （`source='osm-import'`，见 `data/osm/README.md`）。OSM 侧覆盖"建筑和道路以外"的面与点
  （运动场地/绿地/水域/停车场/闸机/公共设施…），命名、分类与"超大面只进管理台"的口径
  都写在导入规则里；有名字的节点仍在 `pois`（地点）表，不重复。
  `show_name` 决定**地图上是否标注名称**（绿地、水面这类面标出来只会盖住地图）：
  false 只影响地图标签，列表/搜索/详情页照旧；管理台每条可单独开关，OSM 导入时按
  "名字是否来自 OSM"自动设好。

## 阶段对齐

| 阶段 | 本仓库现状 |
| ---- | ---- |
| 能力验证（Android Wi-Fi 扫描） | App 侧任务；后端已提供指纹上报/定位接口 |
| 校园地图（2.5D、点击建筑） | 建筑接口 + Martin 瓦片流水线（deploy.md 第 5 节）；校园真实 OSM 数据已就位（`data/osm/`，抓取脚本 `scripts/fetch_osm.py`） |
| 室内导航样板（一栋楼×两层） | `cmd/seed` 演示数据即样板，路线分段可验收 |
| 定位样板（指纹+WKNN） | locate 模块 + 演示指纹库 |
| 小公交位置实时显示（App 路线图） | 后端已就绪：`/bus/*` 线路/站点/车辆位置 + 演示环线（`cmd/seed`）；App 侧待接自绘图层与轮询 |
| 扩展覆盖 | 数据生产问题：QGIS 导入 PostGIS，表结构见迁移注释 |
