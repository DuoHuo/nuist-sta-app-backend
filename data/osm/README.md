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
```

两个脚本都只用 Python 标准库。提取 bbox 可用 `--bbox minLon,minLat,maxLon,maxLat` 覆盖。

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
