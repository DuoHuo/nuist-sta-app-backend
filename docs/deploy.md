# 地图后端部署档案

> 适用于 `nuist-sta-app-backend`（地图专用后端：建筑/楼层/POI 服务、路线规划、Wi-Fi 指纹定位基线）。
> 目标环境：一台 Linux 服务器（公网或校园网均可）。

---

## 1. 服务拓扑

```
Flutter App (maplibre_gl)
   │  业务接口 (JSON/GeoJSON)          矢量瓦片 (MVT)
   ▼                                   ▼
┌──────────────┐    ┌──────────────────────────┐
│ api  :8080   │    │ martin :3000              │
│ Go / Gin     │    │ 发布 data/tiles/*.mbtiles │
└──────┬───────┘    └──────────────────────────┘
       │ pgx
       ▼
┌─────────────────────────────────────────────┐
│ db :5432 (仅本机回环)                        │
│ PostgreSQL + PostGIS + pgRouting             │
└─────────────────────────────────────────────┘
```

| 服务 | 端口 | 作用 | 是否对外 |
| ---- | ---- | ---- | ---- |
| api | 8080 | 建筑/楼层/POI/路线/定位接口 | 是（建议经反向代理加 HTTPS） |
| martin | 3000 | 校园矢量底图瓦片 | 是（建议同上） |
| db | 5432 | 空间数据库 | **否**，只绑定 127.0.0.1 |

## 2. 服务器要求

- Linux x86_64 / arm64，2 核 4G 起步（校园规模足够）
- Docker 24+ 及 docker compose 插件
- 磁盘：系统+镜像 ≈ 5G；校园瓦片 `campus.mbtiles` 通常 < 500M
- 开放端口：8080（api）、3000（martin）；数据库不要开放

## 3. 方式一：Docker Compose 部署（推荐）

### 3.1 获取代码

```bash
git clone <你的仓库地址> nuist-sta-app-backend
cd nuist-sta-app-backend
```

### 3.2 修改配置（必做）

编辑 `docker-compose.yml`：

1. **改数据库密码**：`POSTGRES_PASSWORD` 与 `CAMPUS_DATABASE_DSN` 中的密码保持一致；
2. **改瓦片地址**：把 `CAMPUS_MAP_TILE_URL` / `CAMPUS_MAP_STYLE_URL` 中的 `localhost` 换成服务器对外域名或 IP（App 要能访问）；
3. 正式部署时把 api 的 command 中 `-seed-demo` 去掉（演示数据仅用于联调）：

```yaml
command: ["/api", "-migrations-path", "/migrations", "-auto-migrate"]
```

### 3.3 启动

```bash
docker compose up -d --build
```

api 容器首次启动会自动：
1. 执行 `migrations/` 建表并启用 postgis / pgrouting；
2. （若带 `-seed-demo`）写入一栋示范教学楼演示数据。

### 3.4 验证

在服务器上：

```bash
# 存活 + 数据库连通
curl -s http://127.0.0.1:8080/healthz

# 地图配置（确认瓦片地址正确）
curl -s http://127.0.0.1:8080/api/v1/map/config

# 建筑列表（有 -seed-demo 时能看到"示范教学楼（演示数据）"）
curl -s http://127.0.0.1:8080/api/v1/buildings

# 地点搜索
curl -s "http://127.0.0.1:8080/api/v1/pois?q=201"

# 路线规划：东门(演示) -> 201 教室
# 先拿两个 POI 的 id：
curl -s "http://127.0.0.1:8080/api/v1/pois?q=东门"
curl -s "http://127.0.0.1:8080/api/v1/pois?q=201"
# 用返回的 poi_id 请求（示例假设 8 和 4）：
curl -s -X POST http://127.0.0.1:8080/api/v1/route \
  -H "Content-Type: application/json" \
  -d '{"origin":{"poi_id":8},"destination":{"poi_id":4}}'
# 返回按 室外段 -> 室内1F -> 楼梯换层 -> 室内2F 分段的路线

# Wi-Fi 定位（演示指纹库）：
curl -s -X POST http://127.0.0.1:8080/api/v1/locate/wifi \
  -H "Content-Type: application/json" \
  -d '{"observations":[
        {"bssid":"02:00:00:00:00:01","rssi":-46},
        {"bssid":"02:00:00:00:00:02","rssi":-88},
        {"bssid":"02:00:00:00:00:03","rssi":-60},
        {"bssid":"02:00:00:00:00:04","rssi":-90}]}'
```

Martin 瓦片服务（放入 mbtiles 后）：

```bash
curl -s http://127.0.0.1:3000/campus/14/13425/6771   # 某个具体瓦片
curl -s http://127.0.0.1:3000/health                 # martin 健康检查
```

## 4. 方式二：裸机部署（不用 Docker 跑 api）

数据库仍建议用 compose 里的 db；api 直接跑二进制：

```bash
# 构建（在任意有 Go 1.26 的机器上）
CGO_ENABLED=0 go build -o campus-api ./cmd/server

# 迁移 + 启动（环境变量配置见第 6 节）
CAMPUS_DATABASE_DSN='postgres://campus:密码@127.0.0.1:5432/campus?sslmode=disable' \
  ./campus-api -auto-migrate -addr :8080
```

systemd 单元示例 `/etc/systemd/system/campus-api.service`：

```ini
[Unit]
Description=NUIST campus map backend
After=network.target

[Service]
WorkingDirectory=/opt/campus-backend
ExecStart=/opt/campus-api -migrations-path /opt/campus-backend/migrations -auto-migrate
Environment=CAMPUS_DATABASE_DSN=postgres://campus:密码@127.0.0.1:5432/campus?sslmode=disable
Environment=CAMPUS_MAP_TILE_URL=https://你的域名/tiles/campus/{z}/{x}/{y}
Restart=always
User=campus

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload && sudo systemctl enable --now campus-api
```

> 裸机数据库需自行安装 PostgreSQL 16/17 + PostGIS + pgRouting 扩展包
> （Debian/Ubuntu：`postgresql-17-postgis-3` 与 `postgresql-17-pgrouting`）。

## 5. 校园底图瓦片生产（Martin 的数据从哪来）

流程：**OSM 原始数据 → 转 PBF → tilemaker 生成 MBTiles → 放入 data/tiles/ 重启 martin**

> 注意：应下载 OSM 原始数据自行制瓦片，禁止批量抓取 OSM 官方在线瓦片服务。
> 以下命令已在服务器（Docker 环境）实测通过；ghcr.io 走 `ghcr.nju.edu.cn` 镜像。

```bash
# 0) 准备 tilemaster 配置（OpenMapTiles schema，与镜像版本配套）
#    从 https://cdn.jsdelivr.net/gh/systemed/tilemaker@master/resources/ 下载
#    config-openmaptiles.json 与 process-openmaptiles.lua

# 1) OSM XML 转 PBF（tilemaker 只认 PBF；服务器上 pip 装 pyosmium 即可）
python3 -m venv ~/osmenv && ~/osmenv/bin/pip install osmium
~/osmenv/bin/python - <<'EOF'
import osmium
w = osmium.SimpleWriter('data/osm/campus.osm.pbf')
for obj in osmium.FileProcessor('data/osm/nuist-campus.osm'):
    w.add(obj)
w.close()
EOF

# 2) tilemaker 生成矢量瓦片（master 镜像只有 master tag；--bbox 必须给，
#    否则它尝试读全球 shapefile；输出目录需要对容器用户可写 chown 1000）
docker run --rm \
  -v $PWD/data:/work \
  -v $PWD/tilemaker-config.json:/cfg/config.json:ro \
  -v $PWD/tilemaker-process.lua:/cfg/process.lua:ro \
  ghcr.io/systemed/tilemaker:master \
  --input /work/osm/campus.osm.pbf --output /work/tiles/campus.mbtiles \
  --config /cfg/config.json --process /cfg/process.lua \
  --bbox 118.688,32.193,118.726,32.213

# 3) 重启 martin（compose 里 martin 以 CLI 参数运行：`/data --style /etc/martin/campus.json`，
#    自动发布 /data 下全部 mbtiles，并把样式挂载为 /styles/campus。
#    注意：Martin 1.x 配置文件格式与 0.x 不同且多变，CLI 参数最稳）
docker compose restart martin

# 4) 验证
curl -s http://127.0.0.1:3000/catalog          # 应看到 tiles.campus
curl -s http://127.0.0.1:3000/campus           # TileJSON（bounds/矢量图层清单）
curl -s -o /dev/null -w '%{http_code} %{size_download}B\n' \
  http://127.0.0.1:3000/campus/14/13594/6642   # 校园中心一张真实瓦片，应 200 且非空
```

样式：`configs/style.demo.json` 是最小可用样式（背景/水系/道路/建筑拉伸），
App 端可直接内嵌该 JSON 并把 sources.campus.url 指向 `https://你的域名/tiles/campus`。
需要中文地名标注时，需再部署字形（glyphs）资源并在样式中配置 glyphs 地址，
Martin 自 0.13 起支持发布字体字形，详见 https://maplibre.org/martin/。

## 6. 配置参考

优先级：默认值 < YAML（`-config` 指定） < 环境变量。

| 环境变量 | 默认值 | 说明 |
| ---- | ---- | ---- |
| `CAMPUS_SERVER_ADDR` | `:8080` | 监听地址 |
| `CAMPUS_SERVER_BASE_URL` | 空 | 对外基础 URL（生成绝对链接） |
| `CAMPUS_COLLECT_TOKEN` | 空 | 设置后写接口需带 `X-Collect-Token` 头 |
| `CAMPUS_DATABASE_DSN` | localhost 示例 | PostgreSQL 连接串 |
| `CAMPUS_MAP_TILE_URL` | localhost:3000 | 瓦片模板地址 |
| `CAMPUS_MAP_STYLE_URL` | 空 | 样式地址 |
| `CAMPUS_MAP_GLYPHS_URL` | 空 | 字形地址 |
| `CAMPUS_MARTIN_UPSTREAM` | `http://127.0.0.1:3000` | api 同源代理 `/martin/*` 的上游地址；compose 部署设为 `http://martin:3000` |

完整字段见 `configs/config.example.yaml`（含 WKNN 参数 k / min_matched_aps /
missing_penalty_db / max_snap_m 与地图 bounds）。

## 7. 数据维护

迁移随 api 启动自动执行（`-auto-migrate`），无需单独操作。

```bash
# 重新写入演示数据（幂等；api 的 command 需带 -seed-demo）
docker compose restart api

# 手动管理迁移版本（在服务器代码目录执行；db 只绑本机回环，正好可达）
CAMPUS_DATABASE_DSN='postgres://campus:密码@127.0.0.1:5432/campus?sslmode=disable' \
  go run ./cmd/migrate status
CAMPUS_DATABASE_DSN='postgres://campus:密码@127.0.0.1:5432/campus?sslmode=disable' \
  go run ./cmd/migrate down 1    # 回滚最近一个版本（谨慎）

# 查看指纹采集记录
curl -s "http://127.0.0.1:8080/api/v1/fingerprints?building_id=B-DEMO-01"
```

正式数据入口：

- 建筑轮廓 / 室内房间 / 路网：QGIS 配准后导入 PostGIS（表结构见 `migrations/000001_map_init.up.sql` 注释）；
- 楼层约定：`level_index` 内部序号（地面=0）、`display_name` 显示名（1F/B1）、`elevation_m` 海拔三者独立；
- 跨层必须走 `nav_edges.floor_change=true` 的楼梯/电梯边，不得凭二维坐标相同跨层；
- 指纹采集：App 采集端调用 `POST /api/v1/fingerprints`（见 README API 表）。

## 8. 反向代理与 HTTPS（建议）

Caddy 示例（自动证书）：

```caddyfile
map.你的域名 {
    handle /tiles/* {
        uri strip_prefix /tiles
        reverse_proxy 127.0.0.1:3000
    }
    handle /api/* {
        reverse_proxy 127.0.0.1:8080
    }
    handle /healthz {
        reverse_proxy 127.0.0.1:8080
    }
}
```

对应把 App 侧配置改为：
`CAMPUS_MAP_TILE_URL=https://map.你的域名/tiles/campus/{z}/{x}/{y}`

## 9. 备份与恢复

```bash
# 备份（含空间数据）
docker compose exec -T db pg_dump -U campus campus | gzip > campus-$(date +%F).sql.gz

# 恢复
gunzip -c campus-日期.sql.gz | docker compose exec -T db psql -U campus campus
```

建议 crontab 每日备份一次，mbtiles 文件本身在 git/对象存储里留档。

## 10. 升级与回滚

```bash
git pull
docker compose up -d --build     # api 重启时 -auto-migrate 自动补新迁移
# 回滚代码：git checkout <旧tag> 后同样执行
```

迁移文件只增不改：已发布的 up 文件内容不可修改，修正一律写新版本。

## 11. 安全清单

- [ ] 修改 `POSTGRES_PASSWORD` 与 DSN（默认 campus/campus 仅供本地联调）
- [ ] db 端口保持 `127.0.0.1:5432` 绑定，不对外开放
- [ ] 正式部署去掉 `-seed-demo`
- [ ] 设置 `CAMPUS_COLLECT_TOKEN`，避免指纹写接口被乱写
- [ ] 对外服务走 HTTPS（反向代理）
- [ ] 服务器防火墙只放行 80/443（及必要的 8080/3000 调试端口）
- [ ] OSM 数据展示需保留署名 "© OpenStreetMap contributors"（map.config 默认返回）

## 12. 常见问题

**Q: api 日志提示缺少 pgrouting 扩展？**
用了自带 `deployments/postgres/Dockerfile` 不会出现；裸机部署需安装
`postgresql-XX-pgrouting` 包，迁移 SQL 会自动 `CREATE EXTENSION`。

**Q: Martin 返回 404？**
`data/tiles/campus.mbtiles` 不存在或文件名与 `configs/martin.config.yaml`
里的 `campus` 键不一致。放入文件后 `docker compose restart martin`。

**Q: 地图能出但建筑不拉伸？**
瓦片需含 `building` 图层且带 `render_height` 字段（tilemaker 的
OpenMapTiles 配置默认生成）；样式参考 `configs/style.demo.json`。

**Q: 想限制路线只走无障碍路径？**
`POST /api/v1/route` 传 `{"options":{"accessible_only":true}}`，
楼梯边会被排除。

## 13. 许可与署名

- 底图数据来自 OpenStreetMap（ODbL）：App 关于页与地图角落需署名
  "© OpenStreetMap contributors"；
- 校园楼层图、指纹数据的使用需学校授权，不要公开分发原始采集数据；
- 面向公众提供服务前，请另行核查测绘资质 / 地图审核等合规要求。
