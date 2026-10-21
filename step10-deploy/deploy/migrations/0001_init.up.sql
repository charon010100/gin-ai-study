-- 0001_init.up.sql
--
-- 迁移文件的命名和格式遵循 golang-migrate 的约定：
--   <版本号>_<描述>.<up|down>.sql
-- 版本号必须是整数且递增。golang-migrate 会自己维护一张 schema_migrations 表，
-- 记录已经执行到哪个版本，所以这些 SQL 只需要保证「在一个空库上按顺序执行能得到正确结构」。
--
-- 为什么用 SQL 迁移而不是 GORM 的 AutoMigrate：
--   1. AutoMigrate 只会「加」东西，不会删列、不会改类型、不会改索引名。
--      跑过几个月后，数据库结构和你的模型定义已经不一致，而你拿不到可信的结构快照。
--   2. 它没有版本概念，无法回答「线上现在是什么结构」，也就无法回滚。
--   3. 它要求应用有 ALTER 权限。生产账号通常只应该有 DML 权限，
--      结构变更走独立的、有人审过的路径。
--   4. 有损操作（删列、改类型）它要么不做、要么做得不安全，
--      在数据量大的表上会锁表。

CREATE TABLE IF NOT EXISTS articles (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    title      VARCHAR(200)    NOT NULL,
    slug       VARCHAR(200)    NOT NULL,
    created_at DATETIME(3)     NOT NULL,
    updated_at DATETIME(3)     NOT NULL,
    deleted_at DATETIME(3)     NULL DEFAULT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_articles_slug (slug),
    KEY idx_articles_created (created_at)
) ENGINE = InnoDB
  DEFAULT CHARSET = utf8mb4
  COLLATE = utf8mb4_unicode_ci;

-- 插入两条示例数据，便于验证「迁移执行过且应用能读到数据」。
-- 用 INSERT ... ON DUPLICATE KEY UPDATE 而不是直接 INSERT：
-- 迁移脚本应当可重入（虽然 golang-migrate 不会重复执行同一版本，
-- 但在本地反复 down/up 时，能避免主键或唯一键冲突导致失败）。
INSERT INTO articles (title, slug, created_at, updated_at)
VALUES ('第一篇示例文章', 'first-post', UTC_TIMESTAMP(3), UTC_TIMESTAMP(3))
ON DUPLICATE KEY UPDATE title = VALUES(title);

INSERT INTO articles (title, slug, created_at, updated_at)
VALUES ('第二篇示例文章', 'second-post', UTC_TIMESTAMP(3), UTC_TIMESTAMP(3))
ON DUPLICATE KEY UPDATE title = VALUES(title);
