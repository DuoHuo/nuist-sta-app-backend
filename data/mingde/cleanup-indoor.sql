-- Remove the previously imported Mingde indoor dataset so import-indoor.sql
-- can run again. Deleting floors cascades to indoor_features, floor-scoped
-- nav_nodes (and their nav_edges), pois and fp_sessions. The estimated
-- entrance, the outdoor anchor junction and its three connector edges have
-- no floor_id and must be removed explicitly.
BEGIN;
DELETE FROM building_entrances WHERE building_id = 'OSM-Way862952692';
DELETE FROM nav_edges WHERE name = '明德楼测试入口连接';
DELETE FROM nav_nodes
 WHERE floor_id IS NULL AND building_id IS NULL
   AND props->>'source' = 'mingde-plans-v1';
DELETE FROM floors WHERE building_id = 'OSM-Way862952692';
COMMIT;
