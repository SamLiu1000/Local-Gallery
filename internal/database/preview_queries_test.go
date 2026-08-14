package database

import (
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
