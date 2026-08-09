// Package wipe 实现安全擦除（FR-SYS-031）。
//
// 步骤：删除记录 → wal_checkpoint(TRUNCATE) + VACUUM 重建文件 →
// 覆写空闲空间 → 残留校验。
//
// **边界必须说清**（LLD 10.3）：本包覆盖的是数据库文件内部的残留。
// SSD 的磨损均衡与 TRIM 机制可能在物理介质上保留旧页面副本，应用层
// 无法控制。若考察材料密级要求更高，须依靠介质级措施（全盘加密、
// 物理销毁）。擦除功能不得给出超出其能力的安全承诺。
package wipe

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"fmt"
	"os"
)

// Run 执行擦除后处理。sample 是擦除前取下的作答密文特征串，
// 用于事后残留校验；命中则硬失败。
func Run(db *sql.DB, path string, sample [][]byte) error {
	// ① 截断 WAL 并重建文件，回收已删除记录所占页面
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("WAL 截断失败: %w", err)
	}
	if _, err := db.Exec(`VACUUM`); err != nil {
		return fmt.Errorf("VACUUM 失败: %w", err)
	}

	// ② 覆写空闲空间。
	// VACUUM 只是把文件缩小，被释放的扇区里仍然留着旧页面的字节。
	// 这里造一张临时表填随机数据把文件撑大，覆盖那些扇区，再删表重建。
	before, err := fileSize(path)
	if err != nil {
		return err
	}
	if err := overwriteFreeSpace(db, before); err != nil {
		return fmt.Errorf("覆写空闲空间失败: %w", err)
	}
	if _, err := db.Exec(`VACUUM`); err != nil {
		return fmt.Errorf("覆写后 VACUUM 失败: %w", err)
	}

	// ③ 残留校验：以二进制方式扫描数据文件，搜索擦除前记下的特征串。
	// 命中即硬失败——不得在残留未清除的情况下标记为已归档。
	if err := verifyNoResidue(path, sample); err != nil {
		return err
	}
	return nil
}

func overwriteFreeSpace(db *sql.DB, targetBytes int64) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS __wipe(id INTEGER PRIMARY KEY, b BLOB)`); err != nil {
		return err
	}
	defer db.Exec(`DROP TABLE IF EXISTS __wipe`)

	// 撑到原始大小的 1.2 倍，至少 1 MB。
	target := targetBytes * 12 / 10
	if target < 1<<20 {
		target = 1 << 20
	}
	chunk := make([]byte, 64*1024)
	var written int64
	for written < target {
		if _, err := rand.Read(chunk); err != nil {
			return err
		}
		if _, err := db.Exec(`INSERT INTO __wipe(b) VALUES(?)`, chunk); err != nil {
			return err
		}
		written += int64(len(chunk))
	}
	if _, err := db.Exec(`DELETE FROM __wipe`); err != nil {
		return err
	}
	_, err := db.Exec(`DROP TABLE __wipe`)
	return err
}

func verifyNoResidue(path string, sample [][]byte) error {
	if len(sample) == 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("残留校验无法读取数据文件: %w", err)
	}
	for _, s := range sample {
		if len(s) >= 16 && bytes.Contains(data, s) {
			return fmt.Errorf("残留校验未通过：数据文件中仍能命中作答记录特征串，" +
				"擦除不彻底，项目不得标记为已归档")
		}
	}
	return nil
}

func fileSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}
