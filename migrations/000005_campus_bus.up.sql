-- ---------------------------------------------------------------------------
-- 校园公交（小公交/接驳车）：线路、站点、站序，以及"车辆 → 位置"的实时通道。
--
-- 与 map_features 的分工：那里的 bus_stop 是"底图上的一个点"（能被点到、有详情页），
-- 本组表是公交业务本身——线路有编号与颜色、站点有站序、车辆有位置。站台可以两边
-- 都存在，暂不强制关联（关联会逼着录入者先建 feature 才能建站，反而挡住数据）。
--
-- 设计取舍：
--   * 线路几何（MULTILINESTRING）与站序分开存：先建线路档案、后画线是常态，
--     所以 geom 允许为空；站序走 bus_route_stops.seq，允许环线首末同站。
--   * 车辆位置按时间序追加（bus_positions），读取时取每车最新一条，不建"当前位置"
--     单行表：单行表要处理并发更新与写放大，而时序表天然可查轨迹、可回放。
--   * 位置表只追加，不自动清理。按需定期删除（建议留 7 天）：
--     DELETE FROM bus_positions WHERE reported_at < now() - interval '7 days';
--   * 线路/站点沿用 map_features 的 status 约定：draft 只在管理台可见，published 才下发 App。
--   * 自由属性一律进 props JSONB（运营时间、班次间隔、票价、站台雨棚等），
--     不进强类型字段——这些字段的形态还没定，先别写进契约。
-- ---------------------------------------------------------------------------

CREATE TABLE bus_routes (
    route_id    BIGSERIAL PRIMARY KEY,
    code        TEXT NOT NULL CHECK (btrim(code) <> ''),   -- 线路编号/号牌，如 "1号线"、"A"
    name        TEXT NOT NULL CHECK (btrim(name) <> ''),
    description TEXT NOT NULL DEFAULT '',
    color       TEXT NOT NULL DEFAULT '' CHECK (color = '' OR color ~ '^#[0-9A-Fa-f]{6}$'),
    is_loop     BOOLEAN NOT NULL DEFAULT FALSE,
    geom        GEOMETRY(MULTILINESTRING, 4326),
    props       JSONB NOT NULL DEFAULT '{}',
    status      TEXT NOT NULL DEFAULT 'published' CHECK (status IN ('published', 'draft')),
    created_by  TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (code)
);
COMMENT ON TABLE bus_routes IS '校园公交线路：编号 + 名称 + 走向几何；站序见 bus_route_stops';
COMMENT ON COLUMN bus_routes.code IS '线路编号，全库唯一。App 与底图标注都用它，不显示 route_id';
COMMENT ON COLUMN bus_routes.color IS '#RRGGBB，App/底图据此给线上色；留空表示用客户端默认色';
COMMENT ON COLUMN bus_routes.is_loop IS '环线：首末站可同站（bus_route_stops 不限制同一站出现两次）';
COMMENT ON COLUMN bus_routes.geom IS '线路走向，可空——允许先建档案、后画线；入库按 MultiLineString 归一（ST_Multi）';
COMMENT ON COLUMN bus_routes.props IS '自由扩展：如 {"service_hours":"07:30-21:00","headway_min":10,"fare":"免费"}';
COMMENT ON COLUMN bus_routes.status IS 'published=下发 App；draft=仅管理台可见';
CREATE INDEX bus_routes_geom_gist ON bus_routes USING GIST (geom);
CREATE INDEX bus_routes_status_idx ON bus_routes (status);

CREATE TABLE bus_stops (
    stop_id     BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL CHECK (btrim(name) <> ''),
    description TEXT NOT NULL DEFAULT '',
    geom        GEOMETRY(POINT, 4326) NOT NULL,
    props       JSONB NOT NULL DEFAULT '{}',
    status      TEXT NOT NULL DEFAULT 'published' CHECK (status IN ('published', 'draft')),
    created_by  TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
COMMENT ON TABLE bus_stops IS '公交站点：线路上的停靠点，站序由 bus_route_stops.seq 决定';
COMMENT ON COLUMN bus_stops.geom IS '站台位置（点）。站台可同时是 map_features 里的 bus_stop 地物，两边各自编号，不强制同步';
COMMENT ON COLUMN bus_stops.props IS '自由扩展：如 {"shelter":true,"facing_deg":180,"osm_node_id":123}';
COMMENT ON COLUMN bus_stops.status IS 'published=下发 App；draft=仅管理台可见（未核对的站不要出现在 App 里）';
CREATE INDEX bus_stops_geom_gist ON bus_stops USING GIST (geom);
CREATE INDEX bus_stops_status_idx ON bus_stops (status);

-- 线路 × 站点：seq 从 0 开始，全序决定 App 的站序列表与"下一站"。
-- 刻意不建 (route_id, stop_id) 唯一约束：环线首末同站（起点即终点）是常态，
-- 中间站重复由应用层校验拒绝（见 internal/modules/bus 的 planStopSeq）。
CREATE TABLE bus_route_stops (
    route_id BIGINT NOT NULL REFERENCES bus_routes(route_id) ON DELETE CASCADE,
    stop_id  BIGINT NOT NULL REFERENCES bus_stops(stop_id) ON DELETE CASCADE,
    seq      INT NOT NULL CHECK (seq >= 0),
    props    JSONB NOT NULL DEFAULT '{}',
    PRIMARY KEY (route_id, seq)
);
COMMENT ON TABLE bus_route_stops IS '线路站序：seq 从 0 起连续，按序下发；环线允许首末同站';
COMMENT ON COLUMN bus_route_stops.props IS '该站在该线上的差异：如 {"only":"up"} 仅上行停靠';
CREATE INDEX bus_route_stops_stop_idx ON bus_route_stops (stop_id);

-- 车辆注册表：车载设备/司机端的位置上报必须先有车辆编号，否则位置无处挂靠。
CREATE TABLE bus_vehicles (
    vehicle_id TEXT PRIMARY KEY CHECK (btrim(vehicle_id) <> ''),
    label      TEXT NOT NULL DEFAULT '',
    route_id   BIGINT REFERENCES bus_routes(route_id) ON DELETE SET NULL,
    enabled    BOOLEAN NOT NULL DEFAULT TRUE,
    props      JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
COMMENT ON TABLE bus_vehicles IS '车辆注册：vehicle_id 由车载设备/司机端自带，位置上报以此为准';
COMMENT ON COLUMN bus_vehicles.label IS '显示名，如 "1号车"；留空则前端退回显示 vehicle_id';
COMMENT ON COLUMN bus_vehicles.route_id IS '当前值勤线路，可为空（未排班/机动）；线路删除时置空，不连带删除车辆';
COMMENT ON COLUMN bus_vehicles.enabled IS '停运车辆置 false：App 不下发，但保留历史轨迹与注册信息';
COMMENT ON COLUMN bus_vehicles.props IS '自由扩展：如 {"seats":14,"driver":"…"}';

CREATE TABLE bus_positions (
    position_id BIGSERIAL PRIMARY KEY,
    vehicle_id  TEXT NOT NULL REFERENCES bus_vehicles(vehicle_id) ON DELETE CASCADE,
    route_id    BIGINT REFERENCES bus_routes(route_id) ON DELETE SET NULL,
    geom        GEOMETRY(POINT, 4326) NOT NULL,
    heading_deg REAL CHECK (heading_deg IS NULL OR (heading_deg >= 0 AND heading_deg < 360)),
    speed_kmh   REAL CHECK (speed_kmh IS NULL OR speed_kmh >= 0),
    accuracy_m  REAL CHECK (accuracy_m IS NULL OR accuracy_m >= 0),
    reported_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    props       JSONB NOT NULL DEFAULT '{}',
    source      TEXT NOT NULL DEFAULT ''
);
COMMENT ON TABLE bus_positions IS '车辆位置时序：只追加。读取取每车最新一条，超过 max_age_s 视为离线';
COMMENT ON COLUMN bus_positions.route_id IS '上报时的值勤线路（可与车辆当前排班不同）；线路删除后置空，历史轨迹保留';
COMMENT ON COLUMN bus_positions.heading_deg IS '车头朝向，0=正北，顺时针递增，取值 [0,360)';
COMMENT ON COLUMN bus_positions.reported_at IS '设备时间优先（客户端可补传），缺省取服务器 now()；App 用返回的 age_s 判断新鲜度';
COMMENT ON COLUMN bus_positions.props IS '自由扩展：如 {"occupancy":"few","next_stop_id":12}';
CREATE INDEX bus_positions_vehicle_time_idx ON bus_positions (vehicle_id, reported_at DESC);
CREATE INDEX bus_positions_time_idx ON bus_positions (reported_at DESC);
CREATE INDEX bus_positions_geom_gist ON bus_positions USING GIST (geom);

-- 线路几何与站点位置走 000004 的 feature_geom()（GeoJSON -> 2D WGS84，丢掉 Z 值），
-- 线路额外套一层 ST_Multi：接口收 LineString，库里一律存 MultiLineString。
