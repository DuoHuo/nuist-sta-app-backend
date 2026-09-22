-- 校园地图后端初始化：空间数据模型 + 导航图 + Wi-Fi 指纹
-- 依赖 PostGIS（空间类型）与 pgRouting（寻路）。

CREATE EXTENSION IF NOT EXISTS postgis;
CREATE EXTENSION IF NOT EXISTS pgrouting;

-- ---------------------------------------------------------------------------
-- 校园本地米制坐标系（WGS84 <-> 本地米制 变换的锚点，全库唯一）
-- 约定：交换用地理坐标 (WGS84/4326)；室内距离与定位计算用本地米制坐标。
-- ---------------------------------------------------------------------------
CREATE TABLE campus_cs (
    id           INT PRIMARY KEY CHECK (id = 1),
    origin_lon   DOUBLE PRECISION NOT NULL,
    origin_lat   DOUBLE PRECISION NOT NULL,
    rotation_deg DOUBLE PRECISION NOT NULL DEFAULT 0,
    description  TEXT,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
COMMENT ON TABLE campus_cs IS '本地米制坐标系锚点：x 向东、y 向北，单位米';

-- ---------------------------------------------------------------------------
-- 建筑档案。building_id 是本系统自有稳定编号，不绑定 OSM 对象；
-- osm_id 仅作为与底图数据的关联，可空、可变。
-- ---------------------------------------------------------------------------
CREATE TABLE buildings (
    building_id    TEXT PRIMARY KEY,
    osm_id         BIGINT,
    name           TEXT NOT NULL,
    aliases        TEXT[] NOT NULL DEFAULT '{}',
    footprint      GEOMETRY(POLYGON, 4326) NOT NULL,
    height_m       NUMERIC,
    height_source  TEXT CHECK (height_source IN ('measured', 'estimated_floors', 'display_default')),
    has_indoor_map BOOLEAN NOT NULL DEFAULT FALSE,
    splat_scene_id TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
COMMENT ON COLUMN buildings.height_source IS '高度来源：measured=实测, estimated_floors=按楼层估算, display_default=展示默认值。估计值不得当作测绘结果';
COMMENT ON COLUMN buildings.splat_scene_id IS '高斯街景场景编号，空=未开放（App 打开占位页）';
CREATE INDEX buildings_footprint_gist ON buildings USING GIST (footprint);
CREATE INDEX buildings_osm_id_idx ON buildings (osm_id);

-- ---------------------------------------------------------------------------
-- 楼层。内部序号、显示名、实际海拔三者分开保存，不得互相推导。
-- ---------------------------------------------------------------------------
CREATE TABLE floors (
    floor_id     BIGSERIAL PRIMARY KEY,
    building_id  TEXT NOT NULL REFERENCES buildings (building_id) ON DELETE CASCADE,
    level_index  INT NOT NULL,
    display_name TEXT NOT NULL,
    elevation_m  NUMERIC,
    sort_order   INT NOT NULL DEFAULT 0,
    UNIQUE (building_id, level_index),
    UNIQUE (building_id, display_name)
);
COMMENT ON COLUMN floors.level_index IS '内部楼层序号：地面层=0，向上+1，向下-1（可表达地下层/夹层）';
COMMENT ON COLUMN floors.display_name IS 'UI 显示名，如 B1 / 1F / 2F / M';
COMMENT ON COLUMN floors.elevation_m IS '楼层地面相对建筑±0 的高度（米），可空';

-- ---------------------------------------------------------------------------
-- 统一导航图节点：室外路网与室内各楼层在同一张图里。
-- building_id/floor_id 同为 NULL 表示室外节点。
-- ---------------------------------------------------------------------------
CREATE TABLE nav_nodes (
    node_id     BIGSERIAL PRIMARY KEY,
    building_id TEXT REFERENCES buildings (building_id) ON DELETE CASCADE,
    floor_id    BIGINT REFERENCES floors (floor_id) ON DELETE CASCADE,
    kind        TEXT NOT NULL DEFAULT 'junction',
    geom        GEOMETRY(POINT, 4326) NOT NULL,
    props       JSONB NOT NULL DEFAULT '{}',
    CHECK ((building_id IS NULL) = (floor_id IS NULL))
);
COMMENT ON COLUMN nav_nodes.kind IS 'junction|entrance|door|room_door|stair|elevator';
CREATE INDEX nav_nodes_geom_gist ON nav_nodes USING GIST (geom);
CREATE INDEX nav_nodes_floor_idx ON nav_nodes (floor_id);

-- 建筑出入口（引导"进入哪扇门"）
CREATE TABLE building_entrances (
    entrance_id   BIGSERIAL PRIMARY KEY,
    building_id   TEXT NOT NULL REFERENCES buildings (building_id) ON DELETE CASCADE,
    name          TEXT,
    is_accessible BOOLEAN NOT NULL DEFAULT TRUE,
    geom          GEOMETRY(POINT, 4326) NOT NULL,
    nav_node_id   BIGINT REFERENCES nav_nodes (node_id) ON DELETE SET NULL
);
CREATE INDEX building_entrances_building_idx ON building_entrances (building_id);

-- ---------------------------------------------------------------------------
-- 室内要素：房间 / 走廊 / 门 / 楼梯 / 电梯 / 设施，按楼层组织。
-- 供 App 以 GeoJSON 拉取当前楼层渲染（首版室内数据不走瓦片）。
-- ---------------------------------------------------------------------------
CREATE TABLE indoor_features (
    feature_id BIGSERIAL PRIMARY KEY,
    floor_id   BIGINT NOT NULL REFERENCES floors (floor_id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    category   TEXT,
    name       TEXT,
    room_code  TEXT,
    geom       GEOMETRY(GEOMETRY, 4326) NOT NULL,
    props      JSONB NOT NULL DEFAULT '{}'
);
COMMENT ON COLUMN indoor_features.kind IS 'room|corridor|door|stair|elevator|amenity|area';
CREATE INDEX indoor_features_floor_idx ON indoor_features (floor_id);
CREATE INDEX indoor_features_geom_gist ON indoor_features USING GIST (geom);

-- 可搜索地点（室内 + 室外统一）
CREATE TABLE pois (
    poi_id            BIGSERIAL PRIMARY KEY,
    name              TEXT NOT NULL,
    category          TEXT,
    keywords          TEXT[] NOT NULL DEFAULT '{}',
    building_id       TEXT REFERENCES buildings (building_id) ON DELETE CASCADE,
    floor_id          BIGINT REFERENCES floors (floor_id) ON DELETE CASCADE,
    indoor_feature_id BIGINT REFERENCES indoor_features (feature_id) ON DELETE SET NULL,
    location          GEOMETRY(POINT, 4326) NOT NULL,
    nav_node_id       BIGINT REFERENCES nav_nodes (node_id) ON DELETE SET NULL,
    CHECK ((building_id IS NULL) = (floor_id IS NULL))
);
COMMENT ON COLUMN pois.nav_node_id IS '导航锚点；空则用 location 就近吸附路网';
CREATE INDEX pois_location_gist ON pois USING GIST (location);
CREATE INDEX pois_building_idx ON pois (building_id);

-- ---------------------------------------------------------------------------
-- 统一导航图边：室内外连通。跨层必须经由 floor_change 边（楼梯/电梯/坡道），
-- 不允许仅凭二维坐标相同跨层。
-- ---------------------------------------------------------------------------
CREATE TABLE nav_edges (
    edge_id         BIGSERIAL PRIMARY KEY,
    source          BIGINT NOT NULL REFERENCES nav_nodes (node_id) ON DELETE CASCADE,
    target          BIGINT NOT NULL REFERENCES nav_nodes (node_id) ON DELETE CASCADE,
    cost_m          NUMERIC NOT NULL CHECK (cost_m >= 0),
    reverse_cost_m  NUMERIC NOT NULL CHECK (reverse_cost_m >= 0 OR reverse_cost_m = -1),
    edge_kind       TEXT NOT NULL DEFAULT 'walkway',
    name            TEXT,
    geom            GEOMETRY(LINESTRING, 4326),
    is_open         BOOLEAN NOT NULL DEFAULT TRUE,
    is_accessible   BOOLEAN NOT NULL DEFAULT TRUE,
    floor_change    BOOLEAN NOT NULL DEFAULT FALSE,
    CHECK (source <> target)
);
COMMENT ON COLUMN nav_edges.cost_m IS '通行成本（米）。楼梯可加垂直折算惩罚';
COMMENT ON COLUMN nav_edges.reverse_cost_m IS '反向成本；-1 = 禁止逆行';
COMMENT ON COLUMN nav_edges.edge_kind IS 'walkway|corridor|door|stair|elevator|ramp';
COMMENT ON COLUMN nav_edges.floor_change IS '跨层边（楼梯/电梯/坡道）。二维坐标相同但楼层不同的节点必须经此连接';
CREATE INDEX nav_edges_source_idx ON nav_edges (source);
CREATE INDEX nav_edges_target_idx ON nav_edges (target);
CREATE INDEX nav_edges_kind_idx ON nav_edges (edge_kind);

-- 数据导入后按几何长度刷新成本（供 QGIS 草稿数据整理用）
CREATE OR REPLACE FUNCTION nav_refresh_lengths() RETURNS void AS $$
BEGIN
    UPDATE nav_edges e
    SET cost_m         = sub.len,
        reverse_cost_m = CASE WHEN e.reverse_cost_m = -1 THEN -1 ELSE sub.len END
    FROM (
        SELECT e2.edge_id,
               ST_Length(ST_MakeLine(n1.geom, n2.geom)::geography) AS len
        FROM nav_edges e2
        JOIN nav_nodes n1 ON n1.node_id = e2.source
        JOIN nav_nodes n2 ON n2.node_id = e2.target
    ) sub
    WHERE e.edge_id = sub.edge_id AND NOT e.floor_change;
END;
$$ LANGUAGE plpgsql;

-- ---------------------------------------------------------------------------
-- Wi-Fi RSSI 指纹：采集会话 + 每会话多个 AP 观测。
-- 定位使用 BSSID（AP 唯一标识），SSID 仅供人工核对。
-- ---------------------------------------------------------------------------
CREATE TABLE fp_sessions (
    session_id     BIGSERIAL PRIMARY KEY,
    building_id    TEXT REFERENCES buildings (building_id) ON DELETE CASCADE,
    floor_id       BIGINT REFERENCES floors (floor_id) ON DELETE CASCADE,
    x_m            NUMERIC,
    y_m            NUMERIC,
    geom           GEOMETRY(POINT, 4326),
    captured_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    device_model   TEXT,
    orientation_deg NUMERIC,
    note           TEXT
);
COMMENT ON TABLE fp_sessions IS '指纹采集会话：已知位置 + 该处可见的一组 (BSSID, RSSI)';
CREATE INDEX fp_sessions_floor_idx ON fp_sessions (floor_id);
CREATE INDEX fp_sessions_captured_idx ON fp_sessions (captured_at);

CREATE TABLE fp_observations (
    session_id BIGINT NOT NULL REFERENCES fp_sessions (session_id) ON DELETE CASCADE,
    bssid      TEXT NOT NULL,
    ssid       TEXT,
    rssi       INT NOT NULL CHECK (rssi <= 0 AND rssi >= -100),
    freq_mhz   INT,
    PRIMARY KEY (session_id, bssid)
);
CREATE INDEX fp_observations_bssid_idx ON fp_observations (bssid);
