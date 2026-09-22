#!/usr/bin/env python3
"""把 data/osm 下的真实 OSM GeoJSON 导入后端数据库（生成 SQL，交给 psql 执行）。

用法：
    python scripts/import_osm.py [--data-dir data/osm] [--out data/osm/import.sql]
    psql "postgres://campus@127.0.0.1:5433/campus?sslmode=disable" \
        -v ON_ERROR_STOP=1 -f data/osm/import.sql

只依赖标准库。生成的事务幂等：重复执行会先清掉上一次 OSM 导入
（building_id LIKE 'OSM-%'、props->>'source'='osm-import'、关键词含
'osm-import' 的 POI），再重新写入。

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
"""
import argparse
import json
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
    w = out.write
    w("-- 由 scripts/import_osm.py 生成：真实 OSM 数据导入（幂等）\n")
    w("BEGIN;\n")
    # 坐标系锚点：校园 bbox 中心
    lon = (CAMPUS_BBOX[0] + CAMPUS_BBOX[2]) / 2
    lat = (CAMPUS_BBOX[1] + CAMPUS_BBOX[3]) / 2
    w(f"INSERT INTO campus_cs (id, origin_lon, origin_lat, rotation_deg, description)\n"
      f"VALUES (1, {num(lon)}, {num(lat)}, 0, 'OSM 真实数据锚点（校园 bbox 中心）')\n"
      f"ON CONFLICT (id) DO UPDATE SET origin_lon = EXCLUDED.origin_lon,\n"
      f"    origin_lat = EXCLUDED.origin_lat, description = EXCLUDED.description,\n"
      f"    updated_at = now();\n\n")
    # 清理上一次导入（顺序：先 POI/出入口，再节点（级联删边），最后建筑）
    w("DELETE FROM pois WHERE 'osm-import' = ANY(keywords);\n")
    w("DELETE FROM building_entrances WHERE building_id LIKE 'OSM-%';\n")
    w("DELETE FROM nav_nodes WHERE building_id IS NULL AND props->>'source' = 'osm-import';\n")
    w("DELETE FROM buildings WHERE building_id LIKE 'OSM-%';\n")


def close_ring(ring):
    if len(ring) >= 3 and ring[0] != ring[-1]:
        ring = ring + [ring[0]]
    return ring


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
    args = ap.parse_args()

    d = Path(args.data_dir)
    out = sys.stdout if args.out == "-" else open(args.out, "w", encoding="utf-8", newline="\n")

    emit_header(out)
    nb = emit_buildings(out, load(d / "buildings.geojson"))
    nn, ne = emit_road_graph(out, load(d / "roads.geojson"))
    ne_, np_ = emit_entrances_and_pois(out, load(d / "pois.geojson"))
    out.write("-- 按几何长度统一刷新通行成本（迁移自带的维护函数）\n")
    out.write("SELECT nav_refresh_lengths();\n")
    out.write("ANALYZE nav_nodes;\nANALYZE nav_edges;\n")
    out.write("COMMIT;\n")
    if out is not sys.stdout:
        out.close()
    print(f"生成完毕：建筑 {nb}，路网节点 {nn}，路网边 {ne}，"
          f"出入口 {ne_}，POI {np_}", file=sys.stderr)


if __name__ == "__main__":
    main()
