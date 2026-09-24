-- 公交线路/站点/车辆位置：按外键依赖逆序删除。
-- feature_geom() 属于 000004，不在这里删。
DROP TABLE IF EXISTS bus_positions;
DROP TABLE IF EXISTS bus_vehicles;
DROP TABLE IF EXISTS bus_route_stops;
DROP TABLE IF EXISTS bus_stops;
DROP TABLE IF EXISTS bus_routes;
