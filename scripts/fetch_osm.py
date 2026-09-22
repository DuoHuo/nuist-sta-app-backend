#!/usr/bin/env python3
"""从 Overpass API 下载南京信息工程大学主校区的 OSM 数据。

产出（写入 data/osm/）：
  nuist-campus.osm        提取范围内全部 OSM 要素（OSM XML，带 meta，可进 QGIS/JOSM/osmium）
  campus-boundary.geojson 校园边界参考（relation/13070911 外环；注意该多边形在
                          OSM 里本身不完整，仅作参考，不用于提取过滤）

提取方式：bbox 矩形（默认为实测校园范围四周外扩 ~300m，与 docs/deploy.md
的 osmium 裁剪坐标一致）。不用校园 relation 多边形过滤——该 multipolygon
在 OSM 中残缺（3 段外环互不相邻），按它过滤会丢掉大半校园的要素
（实测只有 53 栋建筑，bbox 方式为 440+）。

用法：
  python scripts/fetch_osm.py            # 交互运行
  python scripts/fetch_osm.py --out data/osm
  python scripts/fetch_osm.py --bbox 118.688,32.193,118.726,32.213

镜像轮询 + 重试；Overpass 免费服务有限流，失败多试几次即可。
数据 © OpenStreetMap contributors, ODbL。
"""
from __future__ import annotations

import argparse
import json
import sys
import time
import urllib.parse
import urllib.request
from pathlib import Path

# 南京信息工程大学（主校区，南京浦口盘城），OSM 对象：relation 13070911
CAMPUS_RELATION_ID = 13070911

# 默认提取范围：实测校园 bbox 四周外扩约 300m（与 docs/deploy.md 一致）
DEFAULT_BBOX = (118.688, 32.193, 118.726, 32.213)

MIRRORS = [
    "https://overpass-api.de/api/interpreter",
    "https://overpass.kumi.systems/api/interpreter",
    "https://overpass.private.coffee/api/interpreter",
    "https://overpass.osm.jp/api/interpreter",
    "https://maps.mail.ru/osm/tools/overpass/api/interpreter",
]

USER_AGENT = "nuist-sta-app-campus-data/1.0 (campus map backend; contact via repo)"


def run_overpass(query: str, timeout: int = 240) -> bytes:
    """POST 查询到 Overpass，依次尝试镜像，带退避重试。"""
    body = urllib.parse.urlencode({"data": query}).encode()
    last_err: Exception | None = None
    for attempt in range(1, 4):
        for mirror in MIRRORS:
            try:
                req = urllib.request.Request(
                    mirror,
                    data=body,
                    headers={
                        "Content-Type": "application/x-www-form-urlencoded",
                        "User-Agent": USER_AGENT,
                    },
                )
                with urllib.request.urlopen(req, timeout=timeout) as resp:
                    data = resp.read()
                head = data[:200].lstrip()
                if head.startswith(b"<") and b"error" in data[:2000].lower():
                    raise RuntimeError(f"{mirror} 返回错误页: {data[:300]!r}")
                if not data:
                    raise RuntimeError(f"{mirror} 返回空响应")
                return data
            except Exception as e:  # noqa: BLE001 —— 逐镜像尝试，最后统一抛出
                print(f"  ! {mirror}: {e}", file=sys.stderr)
                last_err = e
        if attempt < 3:
            wait = 15 * attempt
            print(f"  全部镜像失败，{wait}s 后重试（第 {attempt + 1}/3 轮）…", file=sys.stderr)
            time.sleep(wait)
    raise RuntimeError(f"Overpass 全部镜像失败: {last_err}")


def fetch_boundary() -> dict:
    """取校园边界 relation 的成员几何（JSON）。"""
    q = f"[out:json][timeout:120];\nrelation({CAMPUS_RELATION_ID});\nout geom;"
    return json.loads(run_overpass(q))


def outer_rings(rel_geom: dict) -> list[list[tuple[float, float]]]:
    """提取外环 (lat, lon) 序列（可能不止一个外环）。"""
    rings = []
    for m in rel_geom.get("members", []):
        if m.get("type") == "way" and m.get("role") == "outer" and m.get("geometry"):
            rings.append([(p["lat"], p["lon"]) for p in m["geometry"]])
    return rings


def stitch_rings(rings: list[list[tuple[float, float]]]) -> list[list[tuple[float, float]]]:
    """把首尾相接的多个 outer way 段拼成完整闭合环。"""
    stitched: list[list[tuple[float, float]]] = []
    pending = [list(r) for r in rings]
    while pending:
        ring = pending.pop()
        if ring[0] == ring[-1]:
            stitched.append(ring)
            continue
        for i, other in enumerate(pending):
            if other[0] == ring[-1]:  # ring 尾接 other 头
                ring += other[1:]
                pending.pop(i)
                break
            if other[-1] == ring[-1]:  # ring 尾接 other 尾（反向）
                ring += list(reversed(other))[1:]
                pending.pop(i)
                break
            if other[-1] == ring[0]:  # other 尾接 ring 头
                ring = other[:-1] + ring
                pending.pop(i)
                break
            if other[0] == ring[0]:  # other 头接 ring 头（反向）
                ring = list(reversed(other))[:-1] + ring
                pending.pop(i)
                break
        else:
            # 接不上（几何缺口），按现状闭合收尾
            ring.append(ring[0])
            stitched.append(ring)
            continue
        pending.append(ring)  # 还没闭合，继续拼
    return stitched


def ring_to_geojson_polygon(rings: list[list[tuple[float, float]]]) -> dict:
    """外环（可含多个不相邻多边形）转 Polygon/MultiPolygon GeoJSON。"""
    polys = []
    for ring in rings:
        coords = [[(lon, lat) for lat, lon in ring]]
        polys.append(coords)
    geom = (
        {"type": "Polygon", "coordinates": polys[0]}
        if len(polys) == 1
        else {"type": "MultiPolygon", "coordinates": [[p[0]] for p in polys]}
    )
    return {
        "type": "Feature",
        "properties": {
            "osm_type": "relation",
            "osm_id": CAMPUS_RELATION_ID,
            "name": "南京信息工程大学",
            "amenity": "university",
        },
        "geometry": geom,
    }


def simplify(ring: list[tuple[float, float]], tol_deg: float) -> list[tuple[float, float]]:
    """道格拉斯-普克简化，控制 poly 字符串长度（Overpass 无硬限制，但求稳）。"""
    if len(ring) < 3:
        return ring

    def perp(p, a, b) -> float:
        ax, ay = a
        bx, by = b
        px, py = p
        dx, dy = bx - ax, by - ay
        if dx == dy == 0:
            return ((px - ax) ** 2 + (py - ay) ** 2) ** 0.5
        return abs(dx * (ay - py) - (ax - px) * dy) / (dx * dx + dy * dy) ** 0.5

    def dp(pts, lo, hi, keep):
        if hi <= lo + 1:
            return
        best, dist = -1, -1.0
        for i in range(lo + 1, hi):
            d = perp(pts[i], pts[lo], pts[hi])
            if d > dist:
                best, dist = i, d
        if dist > tol_deg:
            keep.add(best)
            dp(pts, lo, best, keep)
            dp(pts, best, hi, keep)

    keep = {0, len(ring) - 1}
    dp(ring, 0, len(ring) - 1, keep)
    return [ring[i] for i in sorted(keep)]


def parse_bbox(s: str) -> tuple[float, float, float, float]:
    parts = [float(x) for x in s.split(",")]
    if len(parts) != 4:
        raise argparse.ArgumentTypeError("bbox 需要 4 个数：minLon,minLat,maxLon,maxLat")
    return parts[0], parts[1], parts[2], parts[3]


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--out", default="data/osm", help="输出目录（默认 data/osm）")
    ap.add_argument("--bbox", type=parse_bbox, default=DEFAULT_BBOX,
                    help="提取范围 minLon,minLat,maxLon,maxLat（默认校园外扩 ~300m）")
    ap.add_argument("--simplify-m", type=float, default=8.0,
                    help="边界多边形简化容差（米，默认 8；设 0 关闭简化）")
    args = ap.parse_args()
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)

    print(f"[1/3] 下载校园边界 relation {CAMPUS_RELATION_ID} …")
    bd = fetch_boundary()
    els = bd.get("elements", [])
    if len(els) != 1:
        raise SystemExit(f"边界查询异常：返回 {len(els)} 个元素")
    rel = els[0]
    rings_raw = outer_rings(rel)
    print(f"      外环 way 数：{len(rings_raw)}，总点数：{sum(len(r) for r in rings_raw)}")
    rings = stitch_rings(rings_raw)
    for r in rings:
        if r[0] != r[-1]:
            r.append(r[0])
    # lat 简化容差：1° ≈ 111km，8m ≈ 7.2e-5°
    tol = args.simplify_m / 111_000.0 if args.simplify_m > 0 else 0.0
    rings_simple = [simplify(r, tol) if tol else r for r in rings]
    print(f"      拼接为 {len(rings_simple)} 个闭合外环，简化后点数：{sum(len(r) for r in rings_simple)}")

    boundary_fc = {
        "type": "FeatureCollection",
        "features": [ring_to_geojson_polygon(rings_simple)],
    }
    (out / "campus-boundary.geojson").write_text(
        json.dumps(boundary_fc, ensure_ascii=False, indent=1), encoding="utf-8"
    )

    all_pts = [p for ring in rings_simple for p in ring]
    lons = [p[1] for p in all_pts]
    lats = [p[0] for p in all_pts]
    campus_bbox = (min(lons), min(lats), max(lons), max(lats))
    print(f"      校园 bbox：{campus_bbox[0]:.5f},{campus_bbox[1]:.5f},{campus_bbox[2]:.5f},{campus_bbox[3]:.5f}")
    print("      注意：该 relation 多边形在 OSM 中不完整，仅作参考；提取用 bbox 矩形。")

    print(f"[2/3] 下载 bbox 内全部 OSM 要素：{args.bbox} …")
    min_lon, min_lat, max_lon, max_lat = args.bbox
    q = (
        "[out:xml][timeout:300];\n"
        f"(\n"
        f"  node({min_lat},{min_lon},{max_lat},{max_lon});\n"
        f"  way({min_lat},{min_lon},{max_lat},{max_lon});\n"
        f"  relation({min_lat},{min_lon},{max_lat},{max_lon});\n"
        f");\n"
        "(._;>;);\n"
        "out meta qt;"
    )
    xml = run_overpass(q, timeout=600)
    osm_path = out / "nuist-campus.osm"
    osm_path.write_bytes(xml)
    n_elem = xml.count(b"<node") + xml.count(b"<way") + xml.count(b"<relation")
    print(f"      已写入 {osm_path}（{len(xml) / 1_048_576:.1f} MiB，约 {n_elem} 个元素）")

    print("[3/3] 完成。数据 © OpenStreetMap contributors（ODbL）。")


if __name__ == "__main__":
    main()
