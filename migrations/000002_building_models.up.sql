-- Immutable model versions and committed assets are separate from map geometry/floors.
CREATE TABLE building_model_versions (
    version TEXT PRIMARY KEY CHECK (version ~ '^[0-9a-f]{32}$'),
    building_id TEXT NOT NULL REFERENCES buildings(building_id) ON DELETE CASCADE,
    manifest JSONB NOT NULL CHECK (jsonb_typeof(manifest) = 'object'),
    active BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX building_model_one_active ON building_model_versions(building_id) WHERE active;
CREATE INDEX building_model_history ON building_model_versions(building_id, created_at DESC);
CREATE TABLE building_model_files (
    version TEXT NOT NULL REFERENCES building_model_versions(version) ON DELETE CASCADE,
    asset TEXT NOT NULL CHECK (asset = 'building.glb' OR asset ~ '^floor_-?[0-9]+\.glb$'),
    sha256 TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size BIGINT NOT NULL CHECK (size > 0 AND size <= 67108864),
    PRIMARY KEY (version, asset)
);
