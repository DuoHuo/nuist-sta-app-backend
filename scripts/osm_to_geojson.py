#!/usr/bin/env python3
"""把 fetch_osm.py 下载的 nuist-campus.osm 转成分类 GeoJSON 图层。

产出（写入 data/osm/）：
  buildings.geojson  建筑面（含全部 OSM 标签；有 height 时附 height_m）
  roads.geojson      道路/人行道线（highway=*）
  pois.geojson       有标签的节点（食堂/门/设施等）
  areas.geojson      非建筑面（水域/绿地/操场/停车场面等）

无第三方依赖，纯标准库。数据 © OpenStreetMap contributors, ODbL。
"""
from __future__ import annotations

import argparse
import json
import xml.etree.ElementTree as ET
from pathlib import Path

AREA_KEYS = ("leisure", "landuse", "natural", "amenity", "highway", "barrier", "man_made", "area")

# Overpass 的 (._;>;) 会把仅有一个节点落在校园内的大对象（如跨省管道
# man_made=pipeline、长途 route）连着全部远端节点带出来；转图层时按
# 校园 bbox 外扩 margin 过滤。兜底值 ≈ 南信大主校区实测范围。
FALLBACK_BBOX = (118.6910, 32.1956, 118.7229, 32.2099)


def tags_of(el: ET.Element) -> dict:
    return {t.attrib["k"]: t.attrib["v"] for t in el.findall("tag")}


def load_osm(path: Path):
    root = ET.parse(path).getroot()
    nodes, ways, rels = {}, {}, {}
    for n in root.findall("node"):
        nodes[n.attrib["id"]] = (float(n.attrib["lon"]), float(n.attrib["lat"]), tags_of(n))
    for w in root.findall("way"):
        refs = [nd.attrib["ref"] for nd in w.findall("nd")]
        ways[w.attrib["id"]] = (refs, tags_of(w))
    for r in root.findall("relation"):
        members = [(m.attrib["type"], m.attrib["ref"], m.attrib.get("role", "")) for m in m_iter(r)]
        rels[r.attrib["id"]] = (members, tags_of(r))
    return nodes, ways, rels


def m_iter(rel: ET.Element):
    yield from rel.findall("member")


def way_coords(refs: list[str], nodes: dict) -> list[tuple[float, float]] | None:
    out = []
    for r in refs:
        n = nodes.get(r)
        if n is None:
            return None  # 引用不完整（被裁剪），跳过
        out.append((n[0], n[1]))
    return out


def is_area(tags: dict, refs: list[str]) -> bool:
    if refs and refs[0] == refs[-1]:
        return True
    return tags.get("area") == "yes"


def stitch(ref_lists: list[list[tuple[float, float]]]) -> list[list[tuple[float, float]]]:
    """把若干线段按首尾拼成闭合环（用于 multipolygon 成员）。"""
    rings, pending = [], [list(c) for c in ref_lists]
    while pending:
        ring = pending.pop()
        if ring[0] == ring[-1] and len(ring) >= 4:
            rings.append(ring)
            continue
        for i, other in enumerate(pending):
            joined = None
            if other[0] == ring[-1]:
                joined = ring + other[1:]
            elif other[-1] == ring[-1]:
                joined = ring + list(reversed(other))[1:]
            elif other[-1] == ring[0]:
                joined = other[:-1] + ring
            elif other[0] == ring[0]:
                joined = list(reversed(other))[:-1] + ring
            if joined is not None:
                pending.pop(i)
                pending.append(joined)
                break
        else:
            ring.append(ring[0])  # 几何缺口，硬闭合
            rings.append(ring)
    return rings


def relation_polygon(rel_id: str, rels: dict, ways: dict, nodes: dict):
    members, tags = rels[rel_id]
    outers, inners = [], []
    for mtype, mref, role in members:
        if mtype != "way" or mref not in ways:
            continue
        refs, wtags = ways[mref]
        coords = way_coords(refs, nodes)
        if coords is None:
            continue
        if role == "outer":
            outers.append(coords)
        elif role == "inner":
            inners.append(coords)
    if not outers:
        return None
    out_rings = stitch(outers)
    in_rings = stitch(inners)
    # 简单归属：内环全部挂到第一个外环（校园尺度足够）
    polygons = [[ring] for ring in out_rings]
    if polygons:
        for ir in in_rings:
            polygons[0].append(ir)
    geom = (
        {"type": "Polygon", "coordinates": [[(lon, lat) for lon, lat in r] for r in polygons[0]]}
        if len(polygons) == 1
        else {
            "type": "MultiPolygon",
            "coordinates": [[[(lon, lat) for lon, lat in r] for r in poly] for poly in polygons],
        }
    )
    return geom, tags


def feat(props: dict, geom: dict) -> dict:
    ordered = {k: v for k, v in props.items() if k != "geometry"}
    return {"type": "Feature", "properties": ordered, "geometry": geom}


def pt(lon: float, lat: float) -> dict:
    return {"type": "Point", "coordinates": [lon, lat]}


def iter_coords(geom: dict):
    t, c = geom["type"], geom["coordinates"]
    if t == "Point":
        yield c
    elif t == "LineString":
        yield from c
    elif t in ("MultiLineString", "Polygon"):
        for line in c:
            yield from line
    elif t == "MultiPolygon":
        for poly in c:
            for line in poly:
                yield from line


def make_bbox_filter(bbox: tuple[float, float, float, float], margin_m: float):
    """校园 bbox 外扩 margin_m 米；要素任一顶点落在其中才保留。"""
    import math

    lat0 = math.radians((bbox[1] + bbox[3]) / 2)
    m_lon = margin_m / (111_320.0 * math.cos(lat0))  # 本纬度 1° 经线 ≈ 94km
    m_lat = margin_m / 110_574.0
    min_lon, min_lat, max_lon, max_lat = bbox
    min_lon -= m_lon; max_lon += m_lon
    min_lat -= m_lat; max_lat += m_lat

    def keep(f: dict) -> bool:
        return any(
            min_lon <= lon <= max_lon and min_lat <= lat <= max_lat
            for lon, lat in iter_coords(f["geometry"])
        )

    return keep


def load_bbox(out: Path) -> tuple[float, float, float, float]:
    p = out / "campus-boundary.geojson"
    if p.exists():
        d = json.loads(p.read_text(encoding="utf-8"))
        lons, lats = [], []
        for f in d.get("features", []):
            for lon, lat in iter_coords(f["geometry"]):
                lons.append(lon)
                lats.append(lat)
        if lons:
            return min(lons), min(lats), max(lons), max(lats)
    return FALLBACK_BBOX


def line(coords: list[tuple[float, float]]) -> dict:
    return {"type": "LineString", "coordinates": [[lon, lat] for lon, lat in coords]}


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--osm", default="data/osm/nuist-campus.osm")
    ap.add_argument("--out", default="data/osm")
    ap.add_argument("--bbox-margin-m", type=float, default=200.0,
                    help="校园 bbox 外扩过滤容差（米，默认 200）")
    args = ap.parse_args()
    out = Path(args.out)

    keep = make_bbox_filter(load_bbox(out), args.bbox_margin_m)

    nodes, ways, rels = load_osm(Path(args.osm))
    buildings, roads, pois, areas = [], [], [], []

    # 关系（multipolygon 建筑 / 场地面）
    rel_used: set[str] = set()
    for rid, (members, tags) in rels.items():
        if tags.get("type") != "multipolygon":
            continue
        res = relation_polygon(rid, rels, ways, nodes)
        if res is None:
            continue
        geom, gtags = res
        props = {"osm_id": f"relation/{rid}", **gtags}
        if "building" in gtags:
            buildings.append(feat(props, geom))
            rel_used.add(rid)
        elif any(k in gtags for k in AREA_KEYS):
            areas.append(feat(props, geom))
            rel_used.add(rid)

    # 线与面
    for wid, (refs, tags) in ways.items():
        coords = way_coords(refs, nodes)
        if coords is None or len(coords) < 2:
            continue
        props = {"osm_id": f"way/{wid}", **tags}
        closed = refs[0] == refs[-1] if refs else False
        if "building" in tags and closed:
            geom = {"type": "Polygon", "coordinates": [[[lon, lat] for lon, lat in coords]]}
            buildings.append(feat(props, geom))
        elif "highway" in tags and not tags.get("area") == "yes":
            roads.append(feat(props, line(coords)))
        elif closed and any(k in tags for k in AREA_KEYS) and "building" not in tags:
            geom = {"type": "Polygon", "coordinates": [[[lon, lat] for lon, lat in coords]]}
            areas.append(feat(props, geom))

    # 有标签的节点 → POI
    for nid, (lon, lat, tags) in nodes.items():
        if not tags:
            continue
        pois.append(feat({"osm_id": f"node/{nid}", **tags}, pt(lon, lat)))

    layers = {
        "buildings.geojson": [f for f in buildings if keep(f)],
        "roads.geojson": [f for f in roads if keep(f)],
        "pois.geojson": [f for f in pois if keep(f)],
        "areas.geojson": [f for f in areas if keep(f)],
    }
    for fname, feats in layers.items():
        fc = {"type": "FeatureCollection", "features": feats}
        (out / fname).write_text(json.dumps(fc, ensure_ascii=False), encoding="utf-8")
        print(f"{fname:22s} {len(feats):5d} 个要素")

    named = [b for b in buildings if b["properties"].get("name")]
    heights = [b["properties"] for b in buildings if b["properties"].get("height")]
    print(f"\n命名建筑 {len(named)} 栋；带 height 标签 {len(heights)} 栋")


if __name__ == "__main__":
    main()
