package database

import (
	"os"
	"path/filepath"
	"time"
	"testing"
)

// TestUnifiedMigration 用旧 schema 合成库验证迁移：增列、回填、FTS 切换、folders 播种。
// 仅本地手工验证用（go test ./internal/database/ -run TestUnifiedMigration），不进 CI。
func TestUnifiedMigration(t *testing.T) {
	src := os.Getenv("MIG_TEST_DB")
	if src == "" {
		t.Skip("MIG_TEST_DB not set")
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "images.db")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		t.Fatal(err)
	}

	idb, err := New(dst)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	defer idb.Close()

	// 1. 元数据回填
	rec := idb.GetImageRecord("id1")
	if rec == nil || rec.Prompt != "1girl, masterpiece" || rec.ParamsJSON != `{"Model":"SDX"}` {
		t.Fatalf("回填失败: %+v", rec)
	}
	// 2. folders 播种
	if n := idb.GetFolderSubtreeCount("C:/pics", ""); n != 2 {
		t.Fatalf("根 subtree_count = %d, want 2", n)
	}
	if n := idb.GetFolderSubtreeCount("C:/pics", "sub"); n != 1 {
		t.Fatalf("sub subtree_count = %d, want 1", n)
	}
	tree, err := idb.LoadFolderTree("C:/pics")
	if err != nil || len(tree) != 2 {
		t.Fatalf("LoadFolderTree = %v, %v", tree, err)
	}
	// 3. 搜索走 image_cache（FTS 后台异步重建，轮询等待）
	var res []SearchResult
	var total int
	for i := 0; i < 50; i++ {
		res, total, err = idb.SearchImagesBySubstring("masterpiece", "", 0, 10)
		if err != nil {
			t.Fatalf("搜索失败: %v", err)
		}
		if total == 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if total != 1 || len(res) != 1 || res[0].ID != "id1" {
		t.Fatalf("搜索失败: %v %d %+v", err, total, res)
	}
	// 4. 增量计数：新增一行
	e := ImageCacheEntry{ID: "id3", Path: "C:/pics/sub/c.jpg", Name: "c.jpg",
		Folder: "sub", RootPath: "C:/pics", LastModified: 130}
	if err := idb.SaveImageCacheBatchCounted("C:/pics", []ImageCacheEntry{e}); err != nil {
		t.Fatal(err)
	}
	if n := idb.GetFolderSubtreeCount("C:/pics", ""); n != 3 {
		t.Fatalf("upsert 后根 subtree_count = %d, want 3", n)
	}
	// 重复 upsert 不重复计数
	if err := idb.SaveImageCacheBatchCounted("C:/pics", []ImageCacheEntry{e}); err != nil {
		t.Fatal(err)
	}
	if n := idb.GetFolderSubtreeCount("C:/pics", ""); n != 3 {
		t.Fatalf("重复 upsert 后 subtree_count = %d, want 3", n)
	}
	// 5. upsert 保留元数据
	if err := idb.SaveImageCacheBatchCounted("C:/pics", []ImageCacheEntry{
		{ID: "id1", Path: "C:/pics/a.png", Name: "a.png", Folder: "", RootPath: "C:/pics", LastModified: 999}}); err != nil {
		t.Fatal(err)
	}
	rec = idb.GetImageRecord("id1")
	if rec.Prompt != "1girl, masterpiece" {
		t.Fatalf("upsert 覆盖了元数据: %+v", rec)
	}
	if rec.LastModified != 999 {
		t.Fatalf("基础字段未更新: %+v", rec)
	}
	// 6. 校准
	if err := idb.RecomputeFolderCountsForRoot("C:/pics", nil); err != nil {
		t.Fatal(err)
	}
	if n := idb.GetFolderSubtreeCount("C:/pics", ""); n != 3 {
		t.Fatalf("校准后 subtree_count = %d, want 3", n)
	}
}
