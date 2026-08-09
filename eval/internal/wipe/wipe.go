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
	"path/filepath"
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

	// ③ **再截断一次 WAL**。这一步不能省，原因是 WAL 模式下 VACUUM 重建
	// 出来的页先写进 -wal，主库文件本身直到 checkpoint 才被改写——
	// 实测：删数据 → checkpoint → VACUUM → 覆写 → VACUUM 之后，
	// 主库文件里作答密文与令牌值**原样还在**，只有再补一次 checkpoint
	// 才真正消失。
	//
	// 少了这一步的后果不是"擦得不够干净"，而是**整个归档擦除永远失败**：
	// 下面的残留校验读的就是主库文件，必然命中、必然硬失败，项目永远
	// 标记不成已归档。CON-06 的收尾动作因此从来没有真正完成过。
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("覆写后 WAL 截断失败: %w", err)
	}

	// ④ 残留校验：以二进制方式扫描数据文件，搜索擦除前记下的特征串。
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

// verifyNoResidue 扫描主库文件**与其边车文件**。
//
// -wal 与 -shm 必须一起扫：它们和主库文件同进同出，拷贝数据目录时会被
// 一并带走，只扫主库等于漏掉一半的面。
func verifyNoResidue(path string, sample [][]byte) error {
	if len(sample) == 0 {
		return nil
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue // 边车文件可能已被截断删除，属正常
			}
			return fmt.Errorf("残留校验无法读取 %s: %w", p, err)
		}
		for _, s := range sample {
			if len(s) >= 16 && bytes.Contains(data, s) {
				return fmt.Errorf("残留校验未通过：%s 中仍能命中作答记录特征串，"+
					"擦除不彻底，项目不得标记为已归档", filepath.Base(p))
			}
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
