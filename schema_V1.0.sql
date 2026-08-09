-- ============================================================
-- 线上民主测评系统 · 数据库结构 V1.0
-- 目标：SQLite 3.35+ / modernc.org/sqlite
--
-- 匿名性相关的硬约束（对应 NFR-ANO-011/020/021/031）：
--   1. token 与 answer_record 两表必须 WITHOUT ROWID + 随机 UUID 主键
--   2. 这两张表不得出现任何时间列
--   3. 两表之间不得有外键或任何可连接字段
-- 上述三条由 CI 断言脚本强制检查，见 LLD 附录。
-- ============================================================

PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

-- ── 元数据与密钥 ────────────────────────────────────────────
CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value BLOB NOT NULL
) WITHOUT ROWID;
-- 约定键：schema_version / kek_salt / wrapped_dek / admin_pwd_hash
--         session_timeout_min / cert_not_after / device_ap_count

-- ── 测评项目 ────────────────────────────────────────────────
CREATE TABLE project (
    id                 BLOB(16) PRIMARY KEY,
    name               TEXT    NOT NULL,
    intro_enc          BLOB,                    -- 加密：测评说明
    status             TEXT    NOT NULL DEFAULT 'draft'
                       CHECK (status IN ('draft','published','running','closed','archived')),
    start_at           TEXT    NOT NULL,        -- RFC3339，项目级时间，非作答时间
    end_at             TEXT    NOT NULL,
    result_open_at     TEXT    NOT NULL,
    expected_count     INTEGER NOT NULL CHECK (expected_count > 0),  -- 应参加人数＝全部比率分母
    paper_entry_count  INTEGER NOT NULL DEFAULT 0,  -- 纸质补录份数（只记数量，不标记具体记录）
    ap_count           INTEGER NOT NULL DEFAULT 1 CHECK (ap_count BETWEEN 1 AND 6),
    grade_scores       TEXT    NOT NULL DEFAULT '[100,80,60,0]',  -- 档位赋分
    created_at         TEXT    NOT NULL,
    published_at       TEXT,
    closed_at          TEXT,
    CHECK (start_at < end_at),
    CHECK (end_at <= result_open_at)
) WITHOUT ROWID;

CREATE INDEX idx_project_status ON project(status);

-- ── 测评表模板（不含人员与作答，长期保留，不受擦除约束）──────
CREATE TABLE form_template (
    id         BLOB(16) PRIMARY KEY,
    name       TEXT NOT NULL,
    body       TEXT NOT NULL,          -- JSON：题组/题目/选项/档位配置，无对象无名单
    builtin    INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
) WITHOUT ROWID;

-- ── 题组 / 题目 / 选项 ──────────────────────────────────────
CREATE TABLE question_group (
    id         BLOB(16) PRIMARY KEY,
    project_id BLOB(16) NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    title      TEXT NOT NULL,
    intro      TEXT,
    sort_no    INTEGER NOT NULL
) WITHOUT ROWID;

CREATE INDEX idx_qgroup_project ON question_group(project_id, sort_no);

CREATE TABLE question (
    id          BLOB(16) PRIMARY KEY,
    project_id  BLOB(16) NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    group_id    BLOB(16) NOT NULL REFERENCES question_group(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL
                CHECK (kind IN ('grade','score','single','multi','text')),
    title       TEXT NOT NULL,
    hint        TEXT,
    required    INTEGER NOT NULL DEFAULT 1,
    sort_no     INTEGER NOT NULL,
    -- 各题型配置：grade 档位名 / score 分值域 / multi 最少最多 / text 字数上限
    config      TEXT NOT NULL DEFAULT '{}'
) WITHOUT ROWID;

CREATE INDEX idx_question_project ON question(project_id, sort_no);

CREATE TABLE question_option (
    id          BLOB(16) PRIMARY KEY,
    question_id BLOB(16) NOT NULL REFERENCES question(id) ON DELETE CASCADE,
    label       TEXT NOT NULL,
    sort_no     INTEGER NOT NULL
) WITHOUT ROWID;

-- ── 测评对象 ────────────────────────────────────────────────
CREATE TABLE subject (
    id         BLOB(16) PRIMARY KEY,
    project_id BLOB(16) NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    name_enc   BLOB NOT NULL,          -- 加密：姓名
    duty_enc   BLOB,                   -- 加密：职务
    tag        TEXT,                   -- 分组标签，不加密（用于统计筛选）
    sort_no    INTEGER NOT NULL
) WITHOUT ROWID;

CREATE INDEX idx_subject_project ON subject(project_id, sort_no);

-- 题目 ↔ 对象 关联（矩阵展开的依据）
CREATE TABLE question_subject (
    question_id BLOB(16) NOT NULL REFERENCES question(id) ON DELETE CASCADE,
    subject_id  BLOB(16) NOT NULL REFERENCES subject(id) ON DELETE CASCADE,
    PRIMARY KEY (question_id, subject_id)
) WITHOUT ROWID;

-- ── 人员名单（仅用于确定令牌数量，分发后可整表删除）─────────
CREATE TABLE roster (
    id         BLOB(16) PRIMARY KEY,
    project_id BLOB(16) NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    label_enc  BLOB NOT NULL           -- 加密：姓名或编号
) WITHOUT ROWID;

-- ============================================================
-- ★ 匿名隔离区 ★
--   以下两张表之间不存在任何关联路径：无外键、无序号、无时间。
--   两表均 WITHOUT ROWID + 随机主键，物理存储顺序与插入顺序无关。
-- ============================================================

CREATE TABLE token (
    id         BLOB(16) PRIMARY KEY,   -- 随机 UUIDv4，非自增
    project_id BLOB(16) NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    value      TEXT    NOT NULL UNIQUE,-- 令牌值，≥128 位熵
    short_code TEXT    NOT NULL UNIQUE,-- 8 位人工可读短码（含校验位）
    batch_no   INTEGER NOT NULL DEFAULT 1,
    ap_index   INTEGER NOT NULL DEFAULT 1,  -- 印在令牌单上的 SSID 序号
    is_spare   INTEGER NOT NULL DEFAULT 0,  -- 备用令牌
    used       INTEGER NOT NULL DEFAULT 0
    -- ✗ 禁止：used_at、created_at、seq、roster_id、ip、user_agent
) WITHOUT ROWID;

CREATE INDEX idx_token_value  ON token(value);
CREATE INDEX idx_token_short  ON token(short_code);
CREATE INDEX idx_token_batch  ON token(project_id, batch_no, used);

CREATE TABLE answer_record (
    id         BLOB(16) PRIMARY KEY,   -- 随机 UUIDv4，非自增
    project_id BLOB(16) NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    batch_no   INTEGER NOT NULL DEFAULT 1,
    payload    BLOB    NOT NULL        -- 整份作答的 AES-256-GCM 密文（JSON 明文）
    -- ✗ 禁止：token_id、roster_id、submitted_at、seq、ip、is_paper_entry
) WITHOUT ROWID;

CREATE INDEX idx_answer_project ON answer_record(project_id);

-- ── 试填数据（发布时清空，不进统计不进导出）─────────────────
CREATE TABLE trial_answer (
    id         BLOB(16) PRIMARY KEY,
    project_id BLOB(16) NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    payload    BLOB NOT NULL,
    created_at TEXT NOT NULL           -- 试填数据可以有时间，它不参与匿名性
) WITHOUT ROWID;

CREATE TABLE trial_token (
    id         BLOB(16) PRIMARY KEY,
    project_id BLOB(16) NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    value      TEXT NOT NULL UNIQUE
) WITHOUT ROWID;

-- ── 操作日志（不含令牌值、IP、作答内容）──────────────────────
CREATE TABLE op_log (
    id         BLOB(16) PRIMARY KEY,
    project_id BLOB(16),
    action     TEXT NOT NULL,
    result     TEXT NOT NULL,
    detail     TEXT,
    created_at TEXT NOT NULL
) WITHOUT ROWID;

-- ── 归档元信息（擦除后保留，用于列表展示与核验）─────────────
CREATE TABLE archive_meta (
    project_id      BLOB(16) PRIMARY KEY,
    project_name    TEXT NOT NULL,
    expected_count  INTEGER NOT NULL,
    submitted_count INTEGER NOT NULL,
    package_sha256  TEXT NOT NULL,
    verify_code     TEXT NOT NULL,     -- 印在 PDF 页脚的核验编号
    archived_at     TEXT NOT NULL
) WITHOUT ROWID;
