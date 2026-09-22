-- 建筑实拍图片：数据库只保存元数据，图片文件本体存放在照片存储目录
-- （photos.storage_dir，默认 data/photos，环境变量 CAMPUS_PHOTO_STORAGE_DIR 可覆盖）下。
-- 文件名为图片内容的 sha256 十六进制摘要前 128 位 + 小写扩展名，因此同一 URL 的内容永不改变；
-- 删除建筑时元数据随外键级联删除，磁盘文件由接口负责清理（不参与数据库事务）。
CREATE TABLE building_photos (
    photo_id    BIGSERIAL PRIMARY KEY,
    building_id TEXT NOT NULL REFERENCES buildings(building_id) ON DELETE CASCADE,
    file_name   TEXT NOT NULL CHECK (file_name ~ '^[0-9a-f]{32}\.(jpg|jpeg|png|webp)$'),
    caption     TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT '',
    taken_at    TIMESTAMPTZ,
    sort_order  INT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- 列表查询固定为“某建筑的图片 + 稳定排序”。
CREATE INDEX building_photos_order ON building_photos(building_id, sort_order, photo_id);
