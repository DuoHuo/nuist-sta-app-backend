-- ---------------------------------------------------------------------------
-- 通用地物：建筑与 POI 之外的校园地物（道路、绿地、水系、广场、校门、停车点、
-- 雕塑等）。建筑/POI 仍保留各自的强类型表——它们有楼层、室内、路径、模型、
-- 照片等下游契约；本表只承载"没有室内结构"的地物，让它们也有稳定编号，
-- 从而能在 App 里被点到、被搜索、有自己的详情页。
--
-- 数据来源：管理台在地图上绘制后提交（source='admin'），QGIS 批量导入
-- 走 source='qgis'；created_by 记录提交人标签（见 configs 的 collect_token）。
-- ---------------------------------------------------------------------------
CREATE TABLE map_features (
    feature_id  BIGSERIAL PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN (
                    'road', 'path', 'green', 'water', 'square', 'sports',
                    'gate', 'bus_stop', 'parking', 'food', 'shop', 'study',
                    'service', 'sculpture', 'facility', 'other')),
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    geom        GEOMETRY(GEOMETRY, 4326) NOT NULL,
    props       JSONB NOT NULL DEFAULT '{}',
    status      TEXT NOT NULL DEFAULT 'published' CHECK (status IN ('published', 'draft')),
    created_by  TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
COMMENT ON TABLE map_features IS '通用地物：无室内结构的校园地物，由管理台绘制提交';
COMMENT ON COLUMN map_features.kind IS '地物类别：road|path|green|water|square|sports|gate|bus_stop|parking|food|shop|study|service|sculpture|facility|other';
COMMENT ON COLUMN map_features.geom IS '点/线/面均可；提交时校验 WGS84 范围、ST_IsValid、顶点数与面积上限';
COMMENT ON COLUMN map_features.props IS '自由扩展属性（开放时间、别名、标签等），不进契约的强类型字段';
COMMENT ON COLUMN map_features.status IS 'published=下发到 App；draft=仅管理台可见（预留审核流程，App 只读 published）';
COMMENT ON COLUMN map_features.created_by IS '提交人标签，来自管理令牌的名字部分；脚本导入留空';
CREATE INDEX map_features_geom_gist ON map_features USING GIST (geom);
CREATE INDEX map_features_kind_idx ON map_features (kind);
CREATE INDEX map_features_status_idx ON map_features (status);

-- 提交路径的统一入口：GeoJSON -> 2D WGS84 几何。
-- Z 值在本模型里没有意义（高度一律走 height_m/elevation_m），这里直接丢掉，
-- 否则三维坐标会撞上列类型的二维限制。写入前另由 ST_IsValid 判定有效性。
CREATE OR REPLACE FUNCTION feature_geom(geojson TEXT) RETURNS geometry AS $$
    SELECT ST_Force2D(ST_SetSRID(ST_GeomFromGeoJSON(geojson), 4326));
$$ LANGUAGE sql IMMUTABLE;

-- ---------------------------------------------------------------------------
-- 溯源字段：此前只有 buildings 有 created_at/updated_at，且没有任何表记录
-- 提交人。管理台开始承担数据录入后，"谁在什么时候提交了什么"必须可查。
-- ---------------------------------------------------------------------------
ALTER TABLE buildings ADD COLUMN created_by TEXT NOT NULL DEFAULT '';
ALTER TABLE buildings ADD COLUMN source     TEXT NOT NULL DEFAULT '';

ALTER TABLE pois ADD COLUMN created_by TEXT NOT NULL DEFAULT '';
ALTER TABLE pois ADD COLUMN source     TEXT NOT NULL DEFAULT '';
ALTER TABLE pois ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE pois ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
