package main

import (
	"os"
	"path/filepath"
	"testing"

	"local-gallery/internal/database"
)

// TestFolderIndexLightCacheRoundtrip 验证轻量索引快照的保存→读取闭环：
// 保存的 (root_path, folder, count) 能原样读回，并重建出正确的 folderIndex/folderCount。
func TestFolderIndexLightCacheRoundtrip(t *testing.T) {
	a := &App{userDataDir: t.TempDir()}

	entries := []database.ImageCacheEntry{
		{RootPath: `K:/bid`, Folder: "", Size: 5},
		{RootPath: `K:/bid`, Folder: "sub", Size: 3},
		{RootPath: `L:/x`, Folder: "", Size: 2},
	}

	a.saveFolderIndexLight(entries)

	got, ok := a.loadFolderIndexLightCache()
	if !ok {
		t.Fatalf("快照应该能读回")
	}
	if len(got) != len(entries) {
		t.Fatalf("读回的条目数不对: got %d want %d", len(got), len(entries))
	}
	for i, g := range got {
		w := entries[i]
		if g.RootPath != w.RootPath || g.Folder != w.Folder || g.Size != w.Size {
			t.Errorf("条目 %d 不一致: got %+v want %+v", i, g, w)
		}
	}

	a.populateFolderIndexFromEntries(got)

	if len(a.folderIndex) != 3 {
		t.Fatalf("folderIndex 应含 3 个键, got %d", len(a.folderIndex))
	}
	for _, key := range []string{"K:/bid", "K:/bid/sub", "L:/x"} {
		if _, ok := a.folderIndex[key]; !ok {
			t.Errorf("folderIndex 缺少键: %s", key)
		}
	}

	// K:/bid 根累计 = 5 + 3；K:/bid/sub = 3；L:/x = 2
	wantCount := map[string]int{
		"K:/bid":     8,
		"K:/bid/sub": 3,
		"L:/x":       2,
	}
	for k, v := range wantCount {
		if a.folderCount[k] != v {
			t.Errorf("folderCount[%s] 不对: got %d want %d", k, a.folderCount[k], v)
		}
	}
}

// TestFolderIndexLightCacheFallback 验证无缓存/损坏/空缓存都走 SQL 兜底（返回 ok=false）。
func TestFolderIndexLightCacheFallback(t *testing.T) {
	dir := t.TempDir()

	// 1) 无缓存文件
	a := &App{userDataDir: dir}
	if _, ok := a.loadFolderIndexLightCache(); ok {
		t.Fatal("无缓存文件时应返回 ok=false")
	}

	// 2) 损坏 JSON
	if err := os.WriteFile(filepath.Join(dir, folderIndexLightFileName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.loadFolderIndexLightCache(); ok {
		t.Fatal("损坏缓存文件时应返回 ok=false")
	}

	// 3) 空列表缓存
	if err := os.WriteFile(filepath.Join(dir, folderIndexLightFileName), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.loadFolderIndexLightCache(); ok {
		t.Fatal("空列表缓存应返回 ok=false")
	}

	// 4) userDataDir 为空
	if _, ok := (&App{}).loadFolderIndexLightCache(); ok {
		t.Fatal("userDataDir 为空时应返回 ok=false")
	}
}

// TestFolderIndexLightSaveEmpty 保存空条目列表后，下次读取应回退（ok=false）。
func TestFolderIndexLightSaveEmpty(t *testing.T) {
	a := &App{userDataDir: t.TempDir()}
	a.saveFolderIndexLight(nil)
	if _, ok := a.loadFolderIndexLightCache(); ok {
		t.Fatal("空条目保存后读取应返回 ok=false（视为无缓存）")
	}
}
