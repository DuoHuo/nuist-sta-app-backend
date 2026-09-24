# 南京信息工程大学（主校区）OSM 数据

来源：OpenStreetMap（© OpenStreetMap contributors，ODbL）。
校园在 OSM 中的对象：**relation/13070911**（amenity=university，南京信息工程大学）。
首次抓取：2026-09-19（Overpass API，数据时间戳见 `nuist-campus.osm` 的 `<meta>`）。

## 文件

| 文件 | 内容 |
| ---- | ---- |
| `nuist-campus.osm` | 提取范围（118.688,32.193 ~ 118.726,32.213，校园外扩 ~300m）内全部 OSM 要素（OSM XML，带版本/meta，QGIS/JOSM/osmium 可直接打开，约 23 MiB / 10 万元素） |
| `campus-boundary.geojson` | 校园边界参考多边形（relation/13070911 外环；该多边形在 OSM 中不完整，仅作参考） |
| `buildings.geojson` | 建筑面 **248 个**（命名 95：教学搂 + 文园20/沁园18/硕园6/晖园4 宿舍群；height 标签仅 4 栋） |
| `roads.geojson` | 道路 325 条（校园 service/footway/steps + 周边城市道路、高速） |
| `pois.geojson` | 有标签节点 127 个（出入口、设施等） |
| `areas.geojson` | 非建筑面 84 个（水域、操场、绿地、龙王山林地等） |
| `preview.png` | 渲染自检图 |

校园 bbox：`118.6910,32.1956,118.7229,32.2099`（已同步到 `configs/config.example.yaml`
的 `map.bounds` 与 `docs/deploy.md` 的 osmium 裁剪示例）。

## 重新抓取 / 重建图层

```bash
python scripts/fetch_osm.py       # Overpass 下载（bbox 提取，镜像轮询）
python scripts/osm_to_geojson.py  # OSM XML → buildings/roads/pois/areas 四个 GeoJSON
python scripts/import_osm.py --out data/osm/import.sql   # GeoJSON → SQL（幂等）
psql "postgres://campus@127.0.0.1:5433/campus?sslmode=disable" -v ON_ERROR_STOP=1 -f data/osm/import.sql

# 只补/重刷「通用地物」（不动建筑与路网，线上增量用这条）
python scripts/import_osm.py --section features --out data/osm/import_features.sql
```

两个脚本都只用 Python 标准库。提取 bbox 可用 `--bbox minLon,minLat,maxLon,maxLat` 覆盖。

## 通用地物（建筑与道路以外的东西）

`areas.geojson` 的非建筑面 + `pois.geojson` 里没被 POI 收走的点，导入 `map_features`
表，App 走 `/api/v1/features` 点击查看。规则（改规则请改 `scripts/import_osm.py` 里
的 `classify_area` / `classify_point` / `PUBLISH_MAX_M2`）：

| OSM 标签 | kind | 无名时的默认名 |
| ---- | ---- | ---- |
| `leisure=pitch/track`、`sport=*` | `sports` | 篮球场 / 网球场 / 田径场 / 跑道…（< 2000 m² 的田径小面叫"田径设施"） |
| `leisure=park`、`landuse=forest/grass`、`natural=wood` | `green` | 绿地（有名字的用 OSM 名字，如"龙王山"） |
| `natural=water`、`water=*` | `water` | 水域 |
| `amenity=parking`、`parking=*` | `parking` | 停车场 |
| `amenity=school/university/research_institute` | `study` | 校园区域 |
| `landuse=commercial/retail`、`shop=*` | `shop` | 商业区 |
| `barrier=gate/lift_gate` | `gate` | 闸机 |
| `tourism=artwork`、`historic=*` | `sculpture` | 景观小品 |
| `amenity=bicycle_rental/toilets/...`、`highway=elevator` | `facility` | 共享单车点 / 公共卫生间 / 无障碍电梯… |
| 其余用地（`landuse=residential/construction/…`） | `other` | 用地 |

两条重要的边界：

- **只发"点得着"的东西**：过街点（40 个）、信号灯、电线塔（38 个）、过细的街道家具
  都不导入；**道路本身（`highway=*`）也不进通用地物**——底图瓦片里已有，用户明确只要
  "建筑和道路以外"的部分。有名字的节点已经进了 `pois` 表（地点），不重复导入。
- **超大面只进管理台**：面积 > 5 公顷（`PUBLISH_MAX_M2`）或中心点在校园 bbox 之外的面，
  以 `status='draft'` 入库（如整个校园轮廓 136 公顷、龙王山 156 公顷、周边住宅区、
  19 公顷的大水面）。App 只收 `published`，否则半透明色块会把地图铺满并挡住点击；
  管理台能看到、能改、能按需发布（`props.draft_reason` 记了原因）。

命名与溯源：无名字的按类别编号（"篮球场 1/2/3"），说明字段写 `OSM 自动导入 · <类别>（原始标签）`，
`props` 里保留 `osm_id`、`osm_tags`、`area_m2`。导入是**重建式**的
（`DELETE ... WHERE source='osm-import'` 再插），所以 `feature_id` 每次重导都会变——
管理台手绘的地物 `source='admin'`，不受影响。

**名称标注（`show_name`）**：绿地、水面、停车场这些面把名字画到地图上只会盖住地图，
所以导入时按"名字从哪来"决定——**OSM 里本来就有名字的才标**（西苑篮球场、中苑老田径场、
藕舫园、龙王山…共 17 条），**脚本生成的名字不标**（"篮球场 3""停车场 7"，共 87 条）。
不标只是不画文字标签，列表、搜索、详情页照旧显示名称；管理台「地物管理」里每条都能
单独开关（列表有「名称标注」列与筛选），App 侧读 `/features` 返回的 `show_name` 决定是否绘制标签。

## 重要教训与已知坑

- **不能用校园 relation 多边形做提取过滤**。relation/13070911 的 multipolygon
  在 OSM 中残缺（3 段外环互不相邻、内部覆盖支离破碎），按它过滤只能拿到
  53 栋建筑；改用 bbox 提取后为 248 栋（校园 bbox 内 OSM 实有建筑 153 栋，
  其余为紧邻校园的周边建筑）。精确校园复核命令：
  `way["building"](32.1956,118.6910,32.2099,118.7229); out count;` → 153。
- Overpass 的 `(._;>;)` 递归会把**恰好穿校园的大对象**（如西气东输天然气管道
  `man_made=pipeline`、跨省 `route` 关系）连同远端节点（新疆、甘肃）带回来；
  `osm_to_geojson.py` 按校园 bbox 外扩 200m 过滤剔除。
- OSM 对该校园覆盖不均：宿舍区与主要教学楼完整，但**建筑 height 标签几乎缺失**
  （248 栋里只有 4 栋）；入库时 `height_source` 只能标 `estimated_floors` 或
  `display_default`，不得当作实测。
- 校园最西带（118.691~118.696）是龙王山林地，无建筑属正常地理，不是缺数据。
- 室内数据（楼层/房间/走廊）OSM 没有，仍需人工采集后走 `indoor_features` 表。
