"""Fit the Mingde model footprint to the OSM building outline.

Compares the current 1F footprint (data/mingde/mingde-1F.geojson) with the
OSM polygon for way/862952692 (data/osm/buildings.geojson) and searches the
rotation delta and translation that minimise the symmetric boundary distance.
Prints the corrected ORIGIN / ANGLE for generate_mingde.py as JSON.

Lengths are meters in a local equirectangular frame around the current
ORIGIN. Only shapely + numpy are needed.
"""
from __future__ import annotations
import json
import math
from pathlib import Path

import numpy as np
from shapely.geometry import Polygon, MultiPolygon, Point
from shapely.ops import unary_union

ROOT = Path(__file__).resolve().parents[1]
ORIGIN = (118.71294883038847, 32.20703416673561)   # keep in sync with generate_mingde.py
ANGLE = math.radians(11.266947222244033)
WAY = 'way/862952692'


def meters_per_deg(lat):
    lat = math.radians(lat)
    mx = 111412.84 * math.cos(lat) - 93.5 * math.cos(3 * lat) + .118 * math.cos(5 * lat)
    my = 111132.92 - 559.82 * math.cos(2 * lat) + 1.175 * math.cos(4 * lat)
    return mx, my


MX, MY = meters_per_deg(ORIGIN[1])


def to_m(lon, lat):
    return ((lon - ORIGIN[0]) * MX, (lat - ORIGIN[1]) * MY)


def load_geoms():
    osm = json.loads((ROOT / 'data/osm/buildings.geojson').read_text(encoding='utf-8'))
    target = None
    for f in osm['features']:
        if f.get('properties', {}).get('osm_id') == WAY:
            ring = f['geometry']['coordinates'][0]
            target = Polygon([to_m(*c) for c in ring])
            break
    assert target is not None, 'OSM way not found'
    model = json.loads((ROOT / 'data/mingde/mingde-1F.geojson').read_text(encoding='utf-8'))
    polys = []
    for f in model['features']:
        g = f['geometry']
        rings = g['coordinates'] if g['type'] == 'Polygon' else None
        for poly in ([g['coordinates']] if g['type'] == 'Polygon' else g['coordinates']):
            polys.append(Polygon([to_m(*c) for c in poly[0]]))
    return unary_union(polys), target


def sample_boundary(geom, step=0.5):
    lines = []
    geoms = geom.geoms if hasattr(geom, 'geoms') else [geom]
    for g in geoms:
        lines.append(g.exterior)
        lines.extend(g.interiors)
    pts = []
    for ln in lines:
        n = max(2, int(ln.length / step) + 1)
        for i in range(n):
            p = ln.interpolate(i / (n - 1), normalized=True)
            pts.append((p.x, p.y))
    return np.array(pts)


def transform(pts, delta, te, tn):
    c, s = math.cos(delta), math.sin(delta)
    x = pts[:, 0] * c - pts[:, 1] * s + te
    y = pts[:, 0] * s + pts[:, 1] * c + tn
    return np.column_stack((x, y))


def apply(geom, delta, te, tn):
    from shapely import affinity
    # shapely affinity.rotate uses degrees CCW around origin
    g = affinity.rotate(geom, math.degrees(delta), origin=(0, 0), use_radians=False)
    return affinity.translate(g, xoff=te, yoff=tn)


def symmetric_chamfer(geom_a, geom_b, step=0.5):
    pa, pb = sample_boundary(geom_a, step), sample_boundary(geom_b, step)
    ua = unary_union([Point(*p) for p in pb])
    ub = unary_union([Point(*p) for p in pa])
    da = np.array([ua.distance(Point(*p)) for p in pa])
    db = np.array([ub.distance(Point(*p)) for p in pb])
    return float(np.mean(da) + np.mean(db)) / 2


def iou(a, b):
    inter = a.intersection(b).area
    union = a.union(b).area
    return inter / union if union else 0.0


def nelder_mead(f, x0, step):
    n = len(x0)
    simplex = [np.array(x0, float)]
    for i in range(n):
        p = np.array(x0, float)
        p[i] += step[i]
        simplex.append(p)
    vals = [f(p) for p in simplex]
    for _ in range(400):
        order = np.argsort(vals)
        simplex = [simplex[i] for i in order]
        vals = [vals[i] for i in order]
        if np.std(vals) < 1e-9:
            break
        centroid = np.mean(simplex[:-1], axis=0)
        worst = simplex[-1]
        xr = centroid + (centroid - worst)
        fr = f(xr)
        if vals[0] <= fr < vals[-2]:
            simplex[-1], vals[-1] = xr, fr
            continue
        if fr < vals[0]:
            xe = centroid + 2 * (centroid - worst)
            fe = f(xe)
            simplex[-1], vals[-1] = (xe, fe) if fe < fr else (xr, fr)
            continue
        xc = centroid + 0.5 * (worst - centroid)
        fc = f(xc)
        if fc < vals[-1]:
            simplex[-1], vals[-1] = xc, fc
            continue
        best = simplex[0]
        simplex = [best] + [best + 0.5 * (p - best) for p in simplex[1:]]
        vals = [vals[0]] + [f(p) for p in simplex[1:]]
    order = np.argsort(vals)
    return simplex[order[0]], vals[order[0]]


def main():
    model, osm = load_geoms()
    base_chamfer = symmetric_chamfer(model, osm)
    base_iou = iou(model, osm)

    mc = np.array(model.centroid.coords[0])
    oc = np.array(osm.centroid.coords[0])

    def cost(p):
        delta, te, tn = p
        return symmetric_chamfer(apply(model, delta, te, tn), osm, step=1.0)

    # coarse grid on rotation with centroid-aligned translation
    best = None
    for deg in np.arange(-8, 8.01, 0.25):
        d = math.radians(deg)
        c, s = math.cos(d), math.sin(d)
        rc = np.array([mc[0] * c - mc[1] * s, mc[0] * s + mc[1] * c])
        t = oc - rc
        v = cost((d, t[0], t[1]))
        if best is None or v < best[0]:
            best = (v, (d, t[0], t[1]))
    x0 = np.array(best[1])
    xopt, vopt = nelder_mead(lambda p: cost(p), x0, (math.radians(0.5), 0.5, 0.5))
    delta, te, tn = (float(v) for v in xopt)

    fitted = apply(model, delta, te, tn)
    result = {
        'before': {'chamfer_m': round(base_chamfer, 3), 'iou': round(base_iou, 4)},
        'after': {'chamfer_m': round(symmetric_chamfer(fitted, osm), 3),
                  'iou': round(iou(fitted, osm), 4)},
        'delta_deg': round(math.degrees(delta), 4),
        'shift_m': [round(te, 3), round(tn, 3)],
        'new_angle_deg': round(math.degrees(ANGLE) + math.degrees(delta), 4),
        'new_origin': [ORIGIN[0] + te / MX, ORIGIN[1] + tn / MY],
    }
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
