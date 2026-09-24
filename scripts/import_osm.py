#!/usr/bin/env python3
"""把 data/osm 下的真实 OSM GeoJSON 导入后端数据库（生成 SQL，交给 psql 执行）。

用法：
    python scripts/import_osm.py [--data-dir data/osm] [--out data/osm/import.sql]
    psql "postgres://campus@127.0.0.1:5433/campus?sslmode=disable" \
        -v ON_ERROR_STOP=1 -f data/osm/import.sql

    # 只补/重刷通用地物（不动建筑与路网，适合线上增量）：
    python scripts/import_osm.py --section features --out data/osm/import_features.sql

只依赖标准库。生成的事务幂等：重复执行会先清掉上一次 OSM 导入
（building_id LIKE 'OSM-%'、props->>'source'='osm-import'、关键词含
'osm-import' 的 POI、source='osm-import' 的通用地物），再重新写入。

导入内容：
- campus_cs        锚点（校园 bbox 中心，供本地米制坐标换算）
- buildings        buildings.geojson 全部 Polygon；height 标签一律按
                   estimated_floors 入库（README 约定：不得当实测），
                   缺 height 但有 building:levels 时按 3.2m/层估算
- nav_nodes/edges  roads.geojson 步行路网（way 顶点为节点、相邻顶点连边，
                   步道类型过滤高速/主干道/施工/自行车道；steps 标记为
                   楼梯并 is_accessible=false），成本最后统一由
                   nav_refresh_lengths() 按几何长度刷新
- 建筑出入口        pois.geojson 中 entrance=* 节点 → nav_nodes(kind=entrance)
                   + 就近连接路网 + building_entrances + POI
- pois             pois.geojson 中有名称的节点（含出入口）
- map_features     areas.geojson 非建筑面 + pois.geojson 里未被 POI 收走的
                   点（闸机/共享单车点/电梯…）→ 通用地物。命名、分类与
                   "哪些只进管理台"的规则见 classify_area/classify_point
                   与 PUBLISH_MAX_M2 的注释
"""
import argparse
import collections
import json
import math
import sys
from pathlib import Path

# 允许进入步行路网的 highway 类型（其余：motorway/trunk/cycleway/construction 等排除）
WALKABLE_HIGHWAY = {
    "footway", "path", "steps", "pedestrian", "service", "residential",
    "living_street", "unclassified", "tertiary", "tertiary_link",
    "secondary", "secondary_link", "primary", "primary_link", "track",
}
LEVEL_METERS = 3.2  # 无 height 标签时按 building:levels 估算的层高

# 校园 bbox（与 configs/config.example.yaml 的 map.bounds 一致）
CAMPUS_BBOX = (118.6910, 32.1956, 118.7229, 32.2099)

# 通用地物：超过这个面积的面（5 公顷）默认只进管理台（status=draft）。
# 校园里还有 1.36 km² 的校园轮廓、1.56 km² 的龙王山、十几个 10 万 m² 级的
# 住宅区用地——它们当"地物"发到 App 只会把地图铺满半透明色块并挡住点击。
PUBLISH_MAX_M2 = 50000.0

# 运动场地：sport=* → 中文名（无名场地按它命名）
SPORT_NAMES = {
    "basketball": "篮球场", "tennis": "网球场", "volleyball": "排球场",
    "soccer": "足球场", "football": "足球场", "badminton": "羽毛球场",
    "table_tennis": "乒乓球场", "athletics": "田径场", "running": "跑道",
    "gymnastics": "体操场地", "fitness": "健身场地", "multi": "综合运动场",
    "shooting": "射击场", "archery": "射箭场", "swimming": "游泳池",
}


def esc(s: str) -> str:
    """SQL 单引号字面量转义。"""
    return s.replace("'", "''")


def num(v) -> str:
    """数值字面量（整数不带小数点，避免落库变成奇怪精度）。"""
    f = float(v)
    return str(int(f)) if f == int(f) else repr(f)


def q(text: str) -> str:
    return "'" + esc(text) + "'"


def load(path: Path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)["features"]


def osm_id_parts(ref: str):
    """'way/582802805' -> ('W', 582802805)；relation 用负数 osm_id 区分。"""
    kind, _, idstr = ref.partition("/")
    return kind.capitalize(), int(idstr)


def emit_header(out):
    out.write("-- 由 scripts/import_osm.py 生成：真实 OSM 数据导入（幂等）\n")
    out.write("BEGIN;\n")


def emit_campus_cs(out):
    # 坐标系锚点：校园 bbox 中心
    lon = (CAMPUS_BBOX[0] + CAMPUS_BBOX[2]) / 2
    lat = (CAMPUS_BBOX[1] + CAMPUS_BBOX[3]) / 2
    out.write(f"INSERT INTO campus_cs (id, origin_lon, origin_lat, rotation_deg, description)\n"
              f"VALUES (1, {num(lon)}, {num(lat)}, 0, 'OSM 真实数据锚点（校园 bbox 中心）')\n"
              f"ON CONFLICT (id) DO UPDATE SET origin_lon = EXCLUDED.origin_lon,\n"
              f"    origin_lat = EXCLUDED.origin_lat, description = EXCLUDED.description,\n"
              f"    updated_at = now();\n\n")


def emit_core_cleanup(out):
    # 清理上一次导入（顺序：先 POI/出入口，再节点（级联删边），最后建筑）
    out.write("DELETE FROM pois WHERE 'osm-import' = ANY(keywords);\n")
    out.write("DELETE FROM building_entrances WHERE building_id LIKE 'OSM-%';\n")
    out.write("DELETE FROM nav_nodes WHERE building_id IS NULL AND props->>'source' = 'osm-import';\n")
    out.write("DELETE FROM buildings WHERE building_id LIKE 'OSM-%';\n")


def close_ring(ring):
    if len(ring) >= 3 and ring[0] != ring[-1]:
        ring = ring + [ring[0]]
    return ring


# ---------------------------------------------------------------------------
# 通用地物：建筑与道路以外的东西（水面、绿地、运动场地、停车场、闸机…）
# ---------------------------------------------------------------------------

# 只列举会影响分类的键，用于生成"类别说明"与 props 里的原始标签
CLASS_KEYS = ("leisure", "landuse", "natural", "water", "amenity", "sport", "parking",
              "shop", "man_made", "barrier", "tourism", "historic", "railway",
              "public_transport", "highway")

POINT_NAMES = {
    "bicycle_rental": "共享单车点", "bicycle_repair_station": "自行车维修点",
    "drinking_water": "饮水点", "toilets": "公共卫生间",
    "charging_station": "充电站", "shelter": "雨棚", "post_box": "邮筒",
}


def tag_summary(p: dict) -> str:
    return ", ".join(f"{k}={p[k]}" for k in CLASS_KEYS if p.get(k))


def ring_area_m2(ring) -> float:
    """等距圆柱近似的多边形面积（校园尺度足够，不引投影库）。"""
    if len(ring) < 4:
        return 0.0
    lat0 = sum(c[1] for c in ring) / len(ring)
    k = math.cos(math.radians(lat0))
    radius = 6371000.0
    pts = [((c[0] - ring[0][0]) * math.radians(1) * radius * k,
            (c[1] - ring[0][1]) * math.radians(1) * radius) for c in ring]
    s = 0.0
    for (x1, y1), (x2, y2) in zip(pts, pts[1:]):
        s += x1 * y2 - x2 * y1
    return abs(s) / 2


def geom_size_m2(geom: dict) -> float:
    polys = [geom["coordinates"]] if geom["type"] == "Polygon" else geom["coordinates"]
    return sum(ring_area_m2(p[0]) for p in polys)


def geom_centroid(geom: dict):
    ring = geom["coordinates"][0] if geom["type"] == "Polygon" else geom["coordinates"][0][0]
    return (sum(c[0] for c in ring) / len(ring), sum(c[1] for c in ring) / len(ring))


def inside_campus(lon: float, lat: float) -> bool:
    return CAMPUS_BBOX[0] <= lon <= CAMPUS_BBOX[2] and CAMPUS_BBOX[1] <= lat <= CAMPUS_BBOX[3]


def classify_area(p: dict):
    """OSM 面标签 → (kind, 无名默认名, 类别说明)；返回 (None, None, None) 表示不是地物。"""
    sport = (p.get("sport") or "").split(";")[0].strip()
    leisure, landuse, natural, amenity = (p.get("leisure"), p.get("landuse"),
                                          p.get("natural"), p.get("amenity"))
    if leisure in ("pitch", "sports_centre", "fitness_centre") or sport:
        return "sports", SPORT_NAMES.get(sport, "运动场地"), "运动场地"
    if leisure == "track":
        return "sports", "跑道", "运动场地"
    if (leisure in ("park", "garden", "playground", "common")
            or landuse in ("grass", "village_green", "recreation_ground", "forest", "meadow")
            or natural in ("wood", "scrub", "grassland", "tree_row", "heath")):
        return "green", "绿地", "绿地林地"
    if natural in ("water", "wetland") or p.get("water") or landuse in ("basin", "reservoir"):
        return "water", "水域", "水面"
    if amenity in ("parking", "motorcycle_parking", "bicycle_parking") or p.get("parking"):
        return "parking", "停车场", "停车场地"
    if (amenity in ("school", "university", "college", "kindergarten", "research_institute")
            or landuse == "education"):
        return "study", "校园区域", "教学科研用地"
    if landuse in ("commercial", "retail") or p.get("shop"):
        return "shop", "商业区", "商业用地"
    if (landuse in ("residential", "construction", "industrial", "quarry", "railway",
                    "cemetery", "farmland", "orchard", "allotments") or p.get("man_made")):
        return "other", "用地", "其它用地"
    return None, None, None


def classify_point(p: dict):
    """OSM 节点标签 → (kind, 默认名, 类别说明)。只收"点得着"的地物：
    过街点、信号灯、电线塔这类道路/电力部件不收（道路本身也不进通用地物）。"""
    barrier, amenity, highway = p.get("barrier"), p.get("amenity"), p.get("highway")
    if barrier in ("gate", "lift_gate", "swing_gate", "kissing_gate") or amenity == "gate":
        return "gate", "闸机", "校门/闸机"
    if highway == "bus_stop" or p.get("public_transport") == "platform":
        return "bus_stop", "公交站", "公交站台"
    if p.get("tourism") == "artwork" or p.get("historic"):
        return "sculpture", "景观小品", "景观/纪念物"
    if highway == "elevator":
        return "facility", "无障碍电梯", "公共设施"
    if amenity in POINT_NAMES:
        return "facility", POINT_NAMES[amenity], "公共设施"
    if amenity in ("parking", "motorcycle_parking", "bicycle_parking"):
        return "parking", "停车点", "停车场地"
    return None, None, None


def emit_features(out, areas, pois) -> tuple[int, int]:
    """建筑与道路以外的地物 → map_features。返回 (发布数, 草稿数)。

    幂等键 source='osm-import'：管理台手绘的地物 source='admin'，不受影响。
    已进 pois 表的点（出入口、有名字的节点）不重复导入——App 已经能点它们。
    show_name：OSM 里本来就有名字的才在地图上标注；脚本生成的名字（"绿地 3"）
    不标（绿地、水面这类面标出来只会盖住地图），列表与详情页不受影响。
    """
    w = out.write
    w("-- ---------- map_features（建筑与道路以外的地物）----------\n")
    w("DELETE FROM map_features WHERE source = 'osm-import';\n")

    items = []  # [osm_id, kind, hint, descr, name, geom, props, status, named]
    skipped = 0

    for f in areas:
        p = f["properties"]
        kind, hint, label = classify_area(p)
        if kind is None:
            print(f"跳过未归类面 {p.get('osm_id')}: {tag_summary(p)}", file=sys.stderr)
            skipped += 1
            continue
        size = geom_size_m2(f["geometry"])
        # 田径类里的小面是场地里的子要素（投掷圈、跳远沙坑），别叫成"田径场"
        if kind == "sports" and hint == "田径场" and size < 2000:
            hint = "田径设施"
        reason = ""
        if size > PUBLISH_MAX_M2:
            reason = f"面积 {size / 10000:.1f} 公顷，超过 {PUBLISH_MAX_M2 / 10000:.0f} 公顷上限，默认只在管理台可见"
        elif not inside_campus(*geom_centroid(f["geometry"])):
            reason = "位于校园范围外（bbox 外扩 200m 内的邻近要素），默认只在管理台可见"
        props = {"source": "osm-import", "osm_id": p["osm_id"],
                 "osm_tags": {k: v for k, v in p.items() if k != "osm_id"},
                 "area_m2": round(size)}
        if reason:
            props["draft_reason"] = reason
        items.append([p["osm_id"], kind, hint, f"OSM 自动导入 · {label}（{tag_summary(p)}）",
                      p.get("name"), f["geometry"], props, "draft" if reason else "published",
                      bool(p.get("name"))])

    for f in pois:
        p = f["properties"]
        if "entrance" in p or p.get("name"):
            continue  # 出入口与有名字的节点已经进了 pois 表
        kind, hint, label = classify_point(p)
        if kind is None:
            continue
        lon, lat = f["geometry"]["coordinates"][:2]
        reason = "" if inside_campus(lon, lat) else "位于校园范围外，默认只在管理台可见"
        props = {"source": "osm-import", "osm_id": p["osm_id"],
                 "osm_tags": {k: v for k, v in p.items() if k != "osm_id"}}
        if reason:
            props["draft_reason"] = reason
        items.append([p["osm_id"], kind, hint, f"OSM 自动导入 · {label}（{tag_summary(p)}）",
                      p.get("name"), f["geometry"], props, "draft" if reason else "published",
                      bool(p.get("name"))])

    # 无名地物按类别编号（篮球场 1 / 篮球场 2 …）：同名几十条在列表里没法分辨。
    # 顺序取 osm_id，保证同样的输入每次生成同样的名字。
    groups = collections.defaultdict(list)
    for it in items:
        if not it[4]:
            groups[it[2]].append(it)
    for hint, group in groups.items():
        ordered = sorted(group, key=lambda x: x[0])
        for i, it in enumerate(ordered, 1):
            it[4] = hint if len(group) == 1 else f"{hint} {i}"

    if items:
        rows = []
        for osm_id, kind, hint, descr, name, geom, props, status, named in items:
            rows.append("  (" + ", ".join([
                q(kind), q(name), q(descr),
                q(json.dumps(geom, ensure_ascii=False, separators=(",", ":"))),
                q(json.dumps(props, ensure_ascii=False)) + "::jsonb",
                q(status), "TRUE" if named else "FALSE",
            ]) + ")")
        w("INSERT INTO map_features (kind, name, description, geom, props, status, source, show_name)\n"
          "SELECT v.kind, v.name, v.descr, feature_geom(v.geojson), v.props, v.status, 'osm-import', v.show_name\n"
          "FROM (VALUES\n" + ",\n".join(rows) + "\n"
          ") AS v(kind, name, descr, geojson, props, status, show_name);\n")
        w("ANALYZE map_features;\n")
    w("\n")

    published = sum(1 for it in items if it[7] == "published")
    return published, len(items) - published


def emit_buildings(out, features) -> int:
    w = out.write
    w("-- ---------- buildings ----------\n")
    n = 0
    for f in features:
        geom = f["geometry"]
        p = f["properties"]
        if geom["type"] != "Polygon":
            print(f"跳过非 Polygon 建筑 {p.get('osm_id')}: {geom['type']}", file=sys.stderr)
            continue
        rings = [close_ring(r) for r in geom["coordinates"]]
        if any(len(r) < 4 for r in rings):
            print(f"跳过退化建筑 {p.get('osm_id')}", file=sys.stderr)
            continue
        geom = {"type": "Polygon", "coordinates": rings}

        kind, oid = osm_id_parts(p["osm_id"])
        bid = f"OSM-{kind}{oid}"
        osm_id = -oid if kind == "R" else oid
        name = p.get("name") or f"未命名建筑 {oid}"
        aliases = [p["name:en"]] if p.get("name:en") and p["name:en"] != name else []

        height = height_source = None
        if p.get("height"):
            try:
                height, height_source = float(p["height"]), "estimated_floors"
            except ValueError:
                pass
        elif p.get("building:levels"):
            try:
                height = float(p["building:levels"]) * LEVEL_METERS
                height_source = "estimated_floors"
            except ValueError:
                pass

        w("INSERT INTO buildings (building_id, osm_id, name, aliases, footprint,"
          " height_m, height_source, has_indoor_map)\n"
          f"VALUES ({q(bid)}, {osm_id}, {q(name)}, ARRAY[{','.join(q(a) for a in aliases)}]::TEXT[],\n"
          f"        ST_SetSRID(ST_GeomFromGeoJSON({q(json.dumps(geom, ensure_ascii=False))}), 4326),\n"
          f"        {num(height) if height is not None else 'NULL'},"
          f" {q(height_source) if height_source else 'NULL'}, FALSE);\n")
        n += 1
    w("\n")
    return n


def emit_road_graph(out, features):
    """顶点去重建节点、相邻顶点连边。返回 (节点数, 边数)。"""
    w = out.write
    w("-- ---------- outdoor nav graph (roads) ----------\n")
    nodes = {}   # (lon,lat) -> None，保持插入顺序
    edges = {}   # frozenset({p,q}) -> (highway, name)
    for f in features:
        p = f["properties"]
        hw = p.get("highway")
        if hw not in WALKABLE_HIGHWAY:
            continue
        coords = f["geometry"]["coordinates"]
        for c in coords:
            key = (round(c[0], 7), round(c[1], 7))
            nodes.setdefault(key, None)
        for a, b in zip(coords, coords[1:]):
            ka, kb = (round(a[0], 7), round(a[1], 7)), (round(b[0], 7), round(b[1], 7))
            if ka == kb:
                continue
            ekey = frozenset((ka, kb))
            if ekey not in edges:
                edges[ekey] = (hw, p.get("name"))

    values = []
    for (lon, lat) in nodes:
        values.append(f"(ST_SetSRID(ST_MakePoint({lon!r},{lat!r}),4326))")
    w("INSERT INTO nav_nodes (building_id, floor_id, kind, geom, props)\n"
      "SELECT NULL, NULL, 'junction', g, '{\"source\":\"osm-import\"}'::jsonb\n"
      "FROM (VALUES\n  " + ",\n  ".join(values) + ") AS v(g);\n")

    step = 0
    for ekey, (hw, name) in edges.items():
        (lon1, lat1), (lon2, lat2) = sorted(ekey)  # frozenset 顺序不定，先排序稳定输出
        is_steps = hw == "steps"
        kind = "stair" if is_steps else "walkway"
        nm = f", {q(name)}" if name else ", NULL"
        w("INSERT INTO nav_edges (source, target, cost_m, reverse_cost_m, edge_kind,"
          " name, is_open, is_accessible, floor_change, geom)\n"
          "SELECT n1.node_id, n2.node_id, 0.01, 0.01, " + q(kind) + nm +
          ", TRUE, " + ("FALSE" if is_steps else "TRUE") + ", FALSE,\n"
          f"        ST_MakeLine(ST_SetSRID(ST_MakePoint({lon1!r},{lat1!r}),4326),"
          f" ST_SetSRID(ST_MakePoint({lon2!r},{lat2!r}),4326))\n"
          "FROM nav_nodes n1, nav_nodes n2\n"
          "WHERE n1.props->>'source'='osm-import' AND n1.kind='junction'\n"
          "  AND n2.props->>'source'='osm-import' AND n2.kind='junction'\n"
          f"  AND ST_X(n1.geom)={lon1!r} AND ST_Y(n1.geom)={lat1!r}\n"
          f"  AND ST_X(n2.geom)={lon2!r} AND ST_Y(n2.geom)={lat2!r};\n")
        step += 1
    w("\n")
    return len(nodes), step


def emit_entrances_and_pois(out, features):
    """entrance=* 节点：nav_node + 连路边 + building_entrances + POI；其余有名字的节点进 POI。"""
    w = out.write
    w("-- ---------- entrances & pois ----------\n")
    w("CREATE TEMP TABLE _ent(osm_id TEXT, node_id BIGINT, geom GEOMETRY(POINT,4326));\n")
    n_ent = n_poi = 0
    for f in features:
        p = f["properties"]
        lon, lat = f["geometry"]["coordinates"][:2]
        ref = p["osm_id"]

        if "entrance" in p:
            name = p.get("name") or f"入口 {ref.split('/')[1]}"
            w("WITH i AS (INSERT INTO nav_nodes (kind, geom, props)\n"
              f"  VALUES ('entrance', ST_SetSRID(ST_MakePoint({lon!r},{lat!r}),4326),"
              " '{\"source\":\"osm-import\"}'::jsonb)\n"
              "  RETURNING node_id, geom)\n"
              f"INSERT INTO _ent SELECT {q(ref)}, node_id, geom FROM i;\n")
            # 出入口挂到包含/最近的本批建筑（30m 内），并连到最近路网节点
            w("INSERT INTO building_entrances (building_id, name, is_accessible, geom, nav_node_id)\n"
              "SELECT b.building_id, " + q(name) + ", TRUE, e.geom, e.node_id\n"
              "FROM _ent e\n"
              "JOIN LATERAL (SELECT building_id, footprint AS fp FROM buildings\n"
              "              WHERE building_id LIKE 'OSM-%'\n"
              "                AND ST_DWithin(footprint, e.geom, 30)\n"
              "              ORDER BY footprint <-> e.geom LIMIT 1) b ON TRUE\n"
              f"WHERE e.osm_id = {q(ref)}\n"
              "  AND ST_GeometryType(b.fp) = 'ST_Polygon';\n")
            w("INSERT INTO nav_edges (source, target, cost_m, reverse_cost_m, edge_kind, name, geom)\n"
              "SELECT j.node_id, e.node_id, 0.01, 0.01, 'walkway', '出入口连接',\n"
              "       ST_MakeLine(j.geom, e.geom)\n"
              "FROM _ent e\n"
              "JOIN LATERAL (SELECT node_id, geom FROM nav_nodes\n"
              "              WHERE kind='junction' AND building_id IS NULL\n"
              "                AND props->>'source'='osm-import'\n"
              "              ORDER BY geom <-> e.geom LIMIT 1) j ON TRUE\n"
              f"WHERE e.osm_id = {q(ref)};\n")
            w("INSERT INTO pois (name, category, keywords, location, nav_node_id)\n"
              "SELECT " + q(name) + ", '出入口', ARRAY['出入口','osm-import'],"
              f" ST_SetSRID(ST_MakePoint({lon!r},{lat!r}),4326), e.node_id\n"
              f"FROM _ent e WHERE e.osm_id = {q(ref)};\n")
            n_ent += 1
            continue

        if not p.get("name"):
            continue
        cat = (p.get("amenity") or p.get("leisure") or p.get("tourism")
               or ("公交站" if p.get("highway") == "bus_stop" else None)
               or ("轨道交通" if p.get("railway") else None)
               or p.get("public_transport") or "地点")
        kws = ["osm-import", p["name"]]
        if p.get("name:en"):
            kws.append(p["name:en"])
        w("INSERT INTO pois (name, category, keywords, location)\n"
          "VALUES (" + q(p["name"]) + ", " + q(str(cat)) + ", ARRAY[" +
          ",".join(q(k) for k in kws) + "]::TEXT[]," +
          f" ST_SetSRID(ST_MakePoint({lon!r},{lat!r}),4326));\n")
        n_poi += 1
    w("\n")
    return n_ent, n_poi


def main():
    ap = argparse.ArgumentParser(description="OSM GeoJSON -> SQL 导入脚本")
    ap.add_argument("--data-dir", default="data/osm", help="GeoJSON 目录（默认 data/osm）")
    ap.add_argument("--out", default="-", help="输出 SQL 文件路径，- 为 stdout")
    ap.add_argument("--section", default="all", choices=("all", "core", "features"),
                    help="all=全量（默认）；core=建筑/路网/出入口/POI；features=只补通用地物（线上增量用）")
    args = ap.parse_args()

    d = Path(args.data_dir)
    out = sys.stdout if args.out == "-" else open(args.out, "w", encoding="utf-8", newline="\n")
    facts = {}

    emit_header(out)
    if args.section in ("all", "core"):
        emit_campus_cs(out)
        emit_core_cleanup(out)
        facts["建筑"] = emit_buildings(out, load(d / "buildings.geojson"))
        facts["路网节点"], facts["路网边"] = emit_road_graph(out, load(d / "roads.geojson"))
        facts["出入口"], facts["POI"] = emit_entrances_and_pois(out, load(d / "pois.geojson"))
        out.write("-- 按几何长度统一刷新通行成本（迁移自带的维护函数）\n")
        out.write("SELECT nav_refresh_lengths();\n")
        out.write("ANALYZE nav_nodes;\nANALYZE nav_edges;\n")
    if args.section in ("all", "features"):
        facts["地物(发布)"], facts["地物(草稿)"] = emit_features(
            out, load(d / "areas.geojson"), load(d / "pois.geojson"))
    out.write("COMMIT;\n")
    if out is not sys.stdout:
        out.close()
    if facts:
        print("生成完毕：" + "，".join(f"{k} {v}" for k, v in facts.items()), file=sys.stderr)


if __name__ == "__main__":
    main()
