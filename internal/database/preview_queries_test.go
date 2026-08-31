package database

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestPreviewQueries(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	defer db.Close()

	sqlDB := db.GetDB()
	// 插入测试数据：K:\photos 根目录 2 张 + sub 子文件夹 3 张
	for i, row := range []struct {
		id, path, rootPath, folder string
		lm                         int64
	}{
		{"r1", "K:\\photos\\a.png", `K:\photos`, "", 100},
		{"r2", "K:\\photos\\b.png", `K:\photos`, "", 200},
		{"s1", "K:\\photos\\sub\\c.png", `K:\photos`, "sub", 300},
		{"s2", "K:\\photos\\sub\\d.png", `K:\photos`, "sub", 400},
		{"s3", "K:\\photos\\sub\\e.png", `K:\photos`, "sub", 500},
	} {
		_ = i
		if _, err := sqlDB.Exec(`INSERT INTO image_cache (id, path, name, size, last_modified, created_at, folder, root_path, width, height, is_video)
			VALUES (?, ?, ?, 0, ?, 0, ?, ?, 0, 0, 0)`,
			row.id, row.path, filepath.Base(row.path), row.lm, row.folder, row.rootPath); err != nil {
			t.Fatalf("插入失败: %v", err)
		}
	}

	// CountImagesByFolder
	n, err := db.CountImagesByFolder(`K:\photos`, "")
	if err != nil || n != 2 {
		t.Fatalf("CountImagesByFolder(root,'') = %d, %v; want 2", n, err)
	}
	n, err = db.CountImagesByFolder(`K:\photos`, "sub")
	if err != nil || n != 3 {
		t.Fatalf("CountImagesByFolder(root,'sub') = %d, %v; want 3", n, err)
	}
	n, err = db.CountImagesByFolder(`K:\photos`, "none")
	if err != nil || n != 0 {
		t.Fatalf("CountImagesByFolder(root,'none') = %d, %v; want 0", n, err)
	}

	// GetImageByFolderOffset：ORDER BY id 稳定，offset 0..2 应为 s1/s2/s3 的某种确定顺序，且含 path
	seen := map[string]bool{}
	for off := 0; off < 3; off++ {
		id, lm, p, err := db.GetImageByFolderOffset(`K:\photos`, "sub", off)
		if err != nil {
			t.Fatalf("GetImageByFolderOffset(%d) 失败: %v", off, err)
		}
		if lm == 0 {
			t.Fatalf("expected non-zero lastModified at offset %d, got %d", off, lm)
		}
		if p == "" {
			t.Fatalf("expected non-empty path at offset %d", off)
		}
		if id == "" || seen[id] {
			t.Fatalf("expected distinct non-empty id at offset %d, got %q", off, id)
		}
		seen[id] = true
	}
}

// TestDeleteByRootBatch 验证分批删除能删干净指定 root 的全部记录，且不影响其它 root。
// 插入超过单批（1000）的行数，确保分批循环正确终止、无遗漏。
func TestDeleteByRootBatch(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	defer db.Close()
	sqlDB := db.GetDB()

	const rootA = `A:\`
	const rootB = `B:\`
	// 根 A 插 2500 行（跨多批），根 B 插 5 行（验证不受影响）
	for i := 0; i < 2500; i++ {
		if _, err := sqlDB.Exec(`INSERT INTO image_cache (id, path, name, size, last_modified, created_at, folder, root_path, width, height, is_video)
			VALUES (?, ?, ?, 0, 0, 0, '', ?, 0, 0, 0)`,
			fmt.Sprintf("a%d", i), fmt.Sprintf("A:\\img%d.png", i), "x", rootA); err != nil {
			t.Fatalf("插入 A 失败: %v", err)
		}
	}
	for i := 0; i < 5; i++ {
		if _, err := sqlDB.Exec(`INSERT INTO image_cache (id, path, name, size, last_modified, created_at, folder, root_path, width, height, is_video)
			VALUES (?, ?, ?, 0, 0, 0, '', ?, 0, 0, 0)`,
			fmt.Sprintf("b%d", i), fmt.Sprintf("B:\\img%d.png", i), "x", rootB); err != nil {
			t.Fatalf("插入 B 失败: %v", err)
		}
	}

	n, err := db.DeleteImageCacheByRoot(rootA)
	if err != nil {
		t.Fatalf("DeleteImageCacheByRoot 失败: %v", err)
	}
	if n != 2500 {
		t.Errorf("应删除 2500 行, got %d", n)
	}

	var cntA, cntB int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM image_cache WHERE root_path = ?`, rootA).Scan(&cntA); err != nil {
		t.Fatalf("查询 A 失败: %v", err)
	}
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM image_cache WHERE root_path = ?`, rootB).Scan(&cntB); err != nil {
		t.Fatalf("查询 B 失败: %v", err)
	}
	if cntA != 0 {
		t.Errorf("根 A 应清空, 剩 %d 行", cntA)
	}
	if cntB != 5 {
		t.Errorf("根 B 应保留 5 行, 剩 %d 行", cntB)
	}
}
