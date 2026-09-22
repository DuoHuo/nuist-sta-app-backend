#!/usr/bin/env python3
"""把 data/osm/ 下的 GeoJSON 图层渲染成校园预览图 preview.png。

依赖 matplotlib（可选工具，仅用于人工检查数据，不属于服务端必需）。
Windows 下自动使用微软雅黑显示中文；缺字体时自动退化为无标注。

用法：
  python scripts/render_preview.py [--out data/osm/preview.png] [--dpi 150]
"""
from __future__ import annotations

import argparse
import json
import math
from pathlib import Path

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib import font_manager
from matplotlib.patheffects import Normal, Stroke

CAMPUS_BBOX = (118.6910, 32.1956, 118.7229, 32.2099)

ROAD_STYLES = {
    "motorway": ("#e892a2", 2.2, 5),
    "secondary": ("#e8a33d", 2.0, 5),
    "tertiary": ("#f0c987", 1.7, 4),
    "residential": ("#f5e3c0", 1.4, 4),
    "unclassified": ("#efe0cf", 1.2, 3),
    "service": ("#ffffff", 1.1, 3),
    "pedestrian": ("#e8d8c8", 1.2, 3),
    "footway": ("#b9d6b0", 0.8, 2),
    "steps": ("#c9a06a", 0.8, 2),
}
DEFAULT_ROAD = ("#cccccc", 1.0, 3)

# 单独标注的主要建筑（过密会糊，宿舍区用区域名标注）
LANDMARKS = [
    "气象楼", "明德楼", "文德楼", "长望楼", "藕舫楼", "揽江楼", "阅江楼",
    "滨江楼", "北辰楼", "启明楼", "明远楼", "基嘉楼", "尚贤楼", "行政楼",
    "信息科技大楼", "风云剧场", "大学生活动中心", "雷丁学院", "水云方",
    "1960街区", "西苑观测场", "中苑新食堂",
]
ZONE_PREFIXES = ["硕园", "沁园", "晖园", "文园", "藤园"]


def cjk_font():
    for p in (
        r"C:\Windows\Fonts\msyh.ttc",
        r"C:\Windows\Fonts\msyhbd.ttc",
        r"C:\Windows\Fonts\simhei.ttf",
        "/usr/share/fonts/truetype/wqy/wqy-microhei.ttc",
    ):
        if Path(p).exists():
            try:
                font_manager.fontManager.addfont(p)
                name = font_manager.FontProperties(fname=p).get_name()
                matplotlib.rcParams["font.sans-serif"] = [name]
                matplotlib.rcParams["axes.unicode_minus"] = False
                return font_manager.FontProperties(fname=p)
            except Exception:
                continue
    return None


def rings(geom):
    """统一返回 [[(lon,lat), ...], ...]：几何里所有环的平铺列表。"""
    t = geom["type"]
    c = geom["coordinates"]
    if t == "Polygon":
        return c
    if t == "MultiPolygon":
        return [ring for poly in c for ring in poly]
    if t == "LineString":
        return [c]
    if t == "MultiLineString":
        return c
    return []


def centroid(geom):
    pts = []
    for ring in rings(geom):
        pts.extend(ring)
    if not pts:
        return None
    return (sum(p[0] for p in pts) / len(pts), sum(p[1] for p in pts) / len(pts))


def load(out: Path, name: str) -> dict:
    return json.loads((out / name).read_text(encoding="utf-8"))


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--osm-dir", default="data/osm")
    ap.add_argument("--out", default="data/osm/preview.png")
    ap.add_argument("--dpi", type=int, default=150)
    args = ap.parse_args()
    d = Path(args.osm_dir)

    font = cjk_font()
    stroke = [Stroke(linewidth=2.5, foreground="white"), Normal()]

    fig, ax = plt.subplots(figsize=(20, 14), dpi=args.dpi)

    # 校园边界（参考）
    for f in load(d, "campus-boundary.geojson")["features"]:
        for ring in rings(f["geometry"]):
            xs = [c[0] for c in ring]
            ys = [c[1] for c in ring]
            ax.fill(xs, ys, color="#f2f5ec", zorder=0)
            ax.plot(xs, ys, color="#999999", lw=1.1, ls="--", zorder=1)

    # 面要素：水系 / 绿地 / 其他
    for f in load(d, "areas.geojson")["features"]:
        p = f["properties"]
        if p.get("natural") == "water":
            c = "#a7cfe8"
        elif (
            p.get("leisure") in ("pitch", "park", "garden", "sports_centre", "track")
            or p.get("landuse") in ("grass", "forest", "meadow")
            or p.get("natural") in ("wood", "scrub")
        ):
            c = "#b5d6a8"
        else:
            c = "#e3ddd2"
        for ring in rings(f["geometry"]):
            try:
                ax.fill(*zip(*[(pt[0], pt[1]) for pt in ring]),
                        color=c, alpha=0.9, zorder=2)
            except Exception:
                pass

    # 道路
    for f in load(d, "roads.geojson")["features"]:
        hw = f["properties"].get("highway")
        color, lw, z = ROAD_STYLES.get(hw, DEFAULT_ROAD)
        for line in rings(f["geometry"]):
            xs = [c[0] for c in line]
            ys = [c[1] for c in line]
            ax.plot(xs, ys, color=color, lw=lw, zorder=z,
                    solid_capstyle="round")

    # 建筑
    for f in load(d, "buildings.geojson")["features"]:
        for ring in rings(f["geometry"]):
            xs = [c[0] for c in ring]
            ys = [c[1] for c in ring]
            ax.fill(xs, ys, color="#cf9084", edgecolor="#8a524a",
                    lw=0.35, zorder=6)

    # 标注
    if font is not None:
        zones: dict[str, list[tuple[float, float]]] = {}
        for f in load(d, "buildings.geojson")["features"]:
            name = f["properties"].get("name", "")
            c = centroid(f["geometry"])
            if c is None:
                continue
            if name in LANDMARKS:
                ax.text(c[0], c[1], name, fontproperties=font, fontsize=8.5,
                        ha="center", va="center", color="#3a2320",
                        path_effects=stroke, zorder=8)
            for pre in ZONE_PREFIXES:
                if name.startswith(pre):
                    zones.setdefault(pre, []).append(c)
                    break
        for zone, pts in zones.items():
            lon = sum(p[0] for p in pts) / len(pts)
            lat = sum(p[1] for p in pts) / len(pts)
            ax.text(lon, lat, f"{zone}宿舍区", fontproperties=font, fontsize=13,
                    fontweight="bold", ha="center", va="center", color="#1f4d2b",
                    path_effects=stroke, zorder=8)
        # 自然地物
        for f in load(d, "areas.geojson")["features"]:
            name = f["properties"].get("name", "")
            if name in ("龙王山",) and font is not None:
                c = centroid(f["geometry"])
                if c:
                    ax.text(c[0], c[1], name, fontproperties=font, fontsize=12,
                            style="italic", ha="center", va="center", color="#4a6b3f",
                            path_effects=stroke, zorder=8)
    else:
        print("未找到中文字体，跳过文字标注", flush=True)

    # 视野：校园 bbox 外扩一点
    pad_lon, pad_lat = 0.0018, 0.0016
    ax.set_xlim(CAMPUS_BBOX[0] - pad_lon, CAMPUS_BBOX[2] + pad_lon)
    ax.set_ylim(CAMPUS_BBOX[1] - pad_lat, CAMPUS_BBOX[3] + pad_lat)
    lat0 = math.radians((CAMPUS_BBOX[1] + CAMPUS_BBOX[3]) / 2)
    ax.set_aspect(1 / math.cos(lat0))

    # 比例尺 500m
    m_per_deg_lon = 111_320 * math.cos(lat0)
    bar_deg = 500 / m_per_deg_lon
    x0 = CAMPUS_BBOX[0] + 0.35 * pad_lon
    y0 = CAMPUS_BBOX[1] - 0.55 * pad_lat
    ax.plot([x0, x0 + bar_deg], [y0, y0], color="#333", lw=2.5, zorder=9)
    ax.text(x0 + bar_deg / 2, y0 + 0.00012, "500 m", fontsize=9,
            ha="center", va="bottom", color="#333", zorder=9)

    ax.set_title("南京信息工程大学主校区 — OSM 数据预览（248 栋建筑 / 325 条道路）",
                 fontsize=16, pad=12)
    ax.set_xlabel("经度 (°E)", fontsize=10)
    ax.set_ylabel("纬度 (°N)", fontsize=10)
    ax.text(0.995, 0.005, "© OpenStreetMap contributors",
            transform=ax.transAxes, fontsize=8, ha="right", va="bottom",
            color="#666")

    fig.tight_layout()
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    fig.savefig(out, bbox_inches="tight")
    print(f"已写入 {out}")
    if font is None:
        print("提示：安装/存在中文字体后重跑可获得标注版")


if __name__ == "__main__":
    main()
