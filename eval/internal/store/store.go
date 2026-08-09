// Package store 是数据访问层：只做事务性读写，不承担统计聚合。
//
// 统计走内存管线（ADR-003）——作答内容是 AES-GCM 密文，SQL 无法对其
// GROUP BY / COUNT / AVG。这条边界必须守住：一旦有人在这里写了聚合 SQL，
// 就意味着他把某些字段留成了明文。
//
// 不使用 ORM（ADR-006）。多数 ORM 的默认行为——自增主键、自动时间戳、
// 软删除标记——每一条都会在作答记录表上制造匿名性泄露，靠"记得关掉"
// 防守不可靠。此处用手写 SQL，表结构由 schema.sql 显式定义。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"time"

	"github.com/pressly/goose/v3"

	"odes/internal/anon"
	"odes/migrations"

	_ "modernc.org/sqlite" // 纯 Go 实现，无 CGO（CON-07）
)

// Schema 供测试与 CI 断言读取：返回迁移脚本里的建表语句原文。
func Schema() string {
	b, err := fs.ReadFile(migrations.FS, "00001_init.sql")
	if err != nil {
		return ""
	}
	return string(b)
}

type DB struct {
	*sql.DB
	path string
}

// Open 打开数据库并确保表结构就绪。
//
// 注意 DSN 里的 _pragma=foreign_keys(1)：foreign_keys 是**连接级**开关，
// 不随数据库文件持久化。写在 schema.sql 里的那一行只对执行建表的那条
// 连接生效，之后 database/sql 从池里取的新连接默认是关闭的。漏掉这个参数，
// 全部 ON DELETE CASCADE 会静默失效，而安全擦除的级联删除直接依赖它。
func Open(path string) (*DB, error) {
	// _txlock=immediate 对应 LLD 5.1 的 BEGIN IMMEDIATE。
	//
	// database/sql 的 BeginTx 默认发的是 BEGIN DEFERRED，那样写锁要到第一条
	// 写语句才去获取；并发提交时后到者不是被排队串行化，而是在升级锁的瞬间
	// 拿到 SQLITE_BUSY。加上这个参数后 BEGIN 立刻取写锁，并发写按到达顺序
	// 排队，这正是提交事务需要的语义。
	dsn := path + "?_txlock=immediate" +
		"&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite 同一时刻只允许一个写事务；限制连接数可避免大量 SQLITE_BUSY
	// 重试。200 人提交高峰下单次事务约 1–3ms，串行化完全够用（LLD 5.2）。
	sdb.SetMaxOpenConns(4)

	db := &DB{DB: sdb, path: path}
	if err := db.migrate(); err != nil {
		sdb.Close()
		return nil, err
	}
	if err := db.assertAnonymityInvariants(); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("匿名性断言失败，拒绝启动: %w", err)
	}
	return db, nil
}

func (db *DB) Path() string { return db.path }

// migrate 用 goose 执行内嵌的迁移脚本（HLD 技术选型表）。
//
// 脚本 embed 进二进制、启动时自动执行——现场不存在"先跑一遍迁移工具"
// 这一步。goose 自己维护 goose_db_version 表记录已应用的版本，重复启动
// 是幂等的。
func (db *DB) migrate() error {
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger()) // 迁移细节不进控制台，失败会由 error 带出
	if err := goose.SetDialect("sqlite3"); err != nil {
		return err
	}
	if err := goose.Up(db.DB, "."); err != nil {
		return fmt.Errorf("执行数据库迁移失败: %w", err)
	}
	var n int
	if err := db.QueryRow(
		`SELECT count(*) FROM meta WHERE key='schema_version'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := db.Exec(
			`INSERT INTO meta(key,value) VALUES('schema_version',?)`, []byte("1")); err != nil {
			return err
		}
	}
	return nil
}

// ── 匿名性断言（LLD 2.5 / AT-02）────────────────────────────────────
//
// 这三条在**启动时**对实际建好的库执行，而不是静态扫描 SQL 文本——
// 后者可被格式变化绕过。任一失败即拒绝启动：匿名性是本系统的核心承诺，
// 带着破损的表结构继续运行，比起不启动是更坏的结果。
// 同一组断言另有一份跑在 CI 里（store_test.go）。

var forbiddenColumn = regexp.MustCompile(`(_at$|time|seq|order|token_id|roster_id|^ip$|^mac$)`)

// anonymityTables 是受约束的两张表。新增表若进入匿名隔离区，须登记在此。
var anonymityTables = []string{"token", "answer_record"}

func (db *DB) assertAnonymityInvariants() error {
	for _, t := range anonymityTables {
		// 断言一：必须是 WITHOUT ROWID。
		// 普通表以隐式 rowid 聚簇，插入顺序即物理顺序，ORDER BY rowid
		// 就能还原提交序列——这正是 V1.0 的关键缺陷（NFR-ANO-020）。
		var ddl string
		if err := db.QueryRow(
			`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, t).Scan(&ddl); err != nil {
			return fmt.Errorf("读取 %s 建表语句失败: %w", t, err)
		}
		if !strings.Contains(strings.ToUpper(strings.ReplaceAll(ddl, "\n", " ")), "WITHOUT ROWID") {
			return fmt.Errorf("表 %s 缺少 WITHOUT ROWID：物理存储顺序会暴露插入顺序", t)
		}

		// 断言二：不得出现时间列、序号列或指向对方的外键列。
		rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", t))
		if err != nil {
			return err
		}
		for rows.Next() {
			var cid int
			var name, typ string
			var notnull int
			var dflt sql.NullString
			var pk int
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				return err
			}
			if forbiddenColumn.MatchString(strings.ToLower(name)) {
				rows.Close()
				return fmt.Errorf("表 %s 出现被禁止的列 %q：该列会重新打开时序关联通道", t, name)
			}
		}
		rows.Close()
	}

	// 断言三：answer_record 的外键不得指向 token。
	// 两表之间不能存在任何关联路径（NFR-ANO-010）。
	rows, err := db.Query("PRAGMA foreign_key_list(answer_record)")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		cols, err := rows.Columns()
		if err != nil {
			return err
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		for i, c := range cols {
			if c == "table" {
				if s, ok := vals[i].(string); ok && s == "token" {
					return fmt.Errorf("answer_record 存在指向 token 的外键：两表之间不得有任何关联路径")
				}
			}
		}
	}
	return nil
}

// ── 事务封装 ────────────────────────────────────────────────────────

// Tx 在 BEGIN IMMEDIATE 下执行 fn（锁模式由 Open 的 _txlock=immediate 决定）。
func (db *DB) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Checkpoint 截断 WAL 并按主键重建数据库文件。
//
// 这两条是 NFR-ANO-031 的实现，是"进入已截止状态"这一迁移的组成部分，
// 不是可选优化（LLD 7.2）：
//   - WAL 帧按事务提交先后排列，解析 WAL 可还原提交序列；
//   - VACUUM 按随机主键重建文件，使物理布局与提交顺序彻底无关。
//
// 执行失败必须告警——此时匿名性承诺尚未成立。
func (db *DB) Checkpoint() error {
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("WAL 截断失败（提交顺序痕迹未清除）: %w", err)
	}
	if _, err := db.Exec(`VACUUM`); err != nil {
		return fmt.Errorf("VACUUM 失败（物理顺序未随机化）: %w", err)
	}
	return nil
}

// ── meta 表 ─────────────────────────────────────────────────────────

func (db *DB) GetMeta(key string) ([]byte, error) {
	var v []byte
	err := db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return v, err
}

func (db *DB) SetMeta(key string, val []byte) error {
	_, err := db.Exec(`INSERT INTO meta(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, val)
	return err
}

// ── 操作日志（FR-SYS-050）───────────────────────────────────────────
//
// 日志不得记录任何令牌值、IP 地址、作答内容。detail 字段由调用方保证。

func (db *DB) Log(projectID *anon.ID, action, result, detail string) error {
	var pid any
	if projectID != nil {
		pid = projectID.Bytes()
	}
	id := anon.NewID()
	_, err := db.Exec(`INSERT INTO op_log(id,project_id,action,result,detail,created_at)
		VALUES(?,?,?,?,?,?)`, id.Bytes(), pid, action, result, detail,
		time.Now().Format(time.RFC3339))
	return err
}

// OpLogEntry 供管理端展示操作日志。
type OpLogEntry struct {
	Action    string
	Result    string
	Detail    string
	CreatedAt string
}

func (db *DB) OpLogs(projectID anon.ID, limit int) ([]OpLogEntry, error) {
	rows, err := db.Query(`SELECT action,result,COALESCE(detail,''),created_at
		FROM op_log WHERE project_id=? ORDER BY created_at DESC LIMIT ?`,
		projectID.Bytes(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OpLogEntry
	for rows.Next() {
		var e OpLogEntry
		if err := rows.Scan(&e.Action, &e.Result, &e.Detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
