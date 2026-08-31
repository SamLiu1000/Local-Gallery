package main

import (
	"container/list"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"local-gallery/internal/database"
)

// TestRefreshLeafMtimeSkip 验证目录 mtime 缓存刷新优化：
//  1. 无变化时第二次刷新不误删（叶子目录被跳过，其文件仍算存在）；
//  2. 新增文件能检测到（目录 mtime 变化 → 不再跳过）；
//  3. 删除文件能检测到。
func TestRefreshLeafMtimeSkip(t *testing.T) {
	root := t.TempDir()
	leaf := filepath.Join(root, "leaf")
	if err := os.MkdirAll(leaf, 0755); err != nil {
		t.Fatal(err)
	}
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(leaf, name), []byte("fakeimage"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.jpg")
	write("b.jpg")

	db, err := database.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	a := &App{
		images:          make(map[string]*ImageEntry),
		folderIndex:     make(map[string][]string),
		folderCount:     make(map[string]int),
		folderLoaded:    make(map[string]bool),
		folderLRU:       list.New(),
		lruNodes:        make(map[string]*list.Element),
		registeredRoots: map[string]bool{root: true},
		folderTypes:     make(map[string]string),
		imageDB:         db,
		userDataDir:     t.TempDir(),
		dirMtimes:       map[string]int64{},
		mu:              sync.RWMutex{},
	}

	// 第一次刷新：全量，添加 2 张
	r1 := a.RefreshFolder(root)
	if !r1.Success || len(r1.Added) != 2 {
		t.Fatalf("第一次刷新应添加 2 张, success=%v added=%d", r1.Success, len(r1.Added))
	}

	// 第二次刷新（无变化）：应 0 增 0 删（叶子目录 mtime 跳过，不误删）
	r2 := a.RefreshFolder(root)
	if !r2.Success || len(r2.Added) != 0 || len(r2.Removed) != 0 {
		t.Fatalf("无变化刷新应 0 增 0 删, added=%d removed=%d", len(r2.Added), len(r2.Removed))
	}

	// 新增一张 → 应检测到 1 张新增
	write("c.jpg")
	r3 := a.RefreshFolder(root)
	if len(r3.Added) != 1 || len(r3.Removed) != 0 {
		t.Fatalf("新增后刷新应加 1 删 0, added=%d removed=%d", len(r3.Added), len(r3.Removed))
	}

	// 删除一张 → 应检测到 1 张删除
	if err := os.Remove(filepath.Join(leaf, "a.jpg")); err != nil {
		t.Fatal(err)
	}
	r4 := a.RefreshFolder(root)
	if len(r4.Removed) != 1 {
		t.Fatalf("删除后刷新应移除 1 张, removed=%d", len(r4.Removed))
	}
}

// TestRefreshLeafMtimeSkipUnloadedFolder 回归测试：folderIndex 键存在但值为 nil
// （"未加载"占位——重启后 loadFolderIndexLight 对每个键都设 nil，且 GetImages 的
// SQL 快路径不会回填 folderIndex），而 image_cache 已有该叶子目录的记录时，
// 无变化刷新不得把这些记录当作"已删除"清掉。
// 对应 bug：叶子目录 mtime 未变 → 命中跳过分支，而 dirIDMap 映射到"空集合"→
// 漏标 current，磁盘权威对比（oldIDs - currentIDs）把整目录图片删光，表现为
// "点击刷新后再点文件夹不出图"（image_cache / images 表记录被误删）。
func TestRefreshLeafMtimeSkipUnloadedFolder(t *testing.T) {
	root := t.TempDir()
	leaf := filepath.Join(root, "leaf")
	if err := os.MkdirAll(leaf, 0755); err != nil {
		t.Fatal(err)
	}
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(leaf, name), []byte("fakeimage"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.jpg")
	write("b.jpg")

	db, err := database.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	a := &App{
		images:          make(map[string]*ImageEntry),
		folderIndex:     make(map[string][]string),
		folderCount:     make(map[string]int),
		folderLoaded:    make(map[string]bool),
		folderLRU:       list.New(),
		lruNodes:        make(map[string]*list.Element),
		registeredRoots: map[string]bool{root: true},
		folderTypes:     make(map[string]string),
		imageDB:         db,
		userDataDir:     t.TempDir(),
		dirMtimes:       map[string]int64{},
		mu:              sync.RWMutex{},
	}

	// 第一次刷新：全量入库，folderIndex 带真实 ID
	r1 := a.RefreshFolder(root)
	if !r1.Success || len(r1.Added) != 2 {
		t.Fatalf("第一次刷新应添加 2 张, success=%v added=%d", r1.Success, len(r1.Added))
	}

	// 模拟重启后的"轻量索引"状态：folderIndex 键保留但值为 nil（未加载）、内存图片清空
	rootNorm := strings.ReplaceAll(root, "\\", "/")
	leafKey := rootNorm + "/leaf"
	a.mu.Lock()
	a.folderIndex[leafKey] = nil
	a.folderLoaded[leafKey] = false
	a.images = make(map[string]*ImageEntry)
	a.mu.Unlock()

	// 第二次刷新（磁盘无变化）：不得删除已有记录（修复前 removed=2 清空该目录）
	r2 := a.RefreshFolder(root)
	if len(r2.Removed) != 0 {
		t.Fatalf("未加载目录无变化刷新不应删除任何记录, removed=%d", len(r2.Removed))
	}
	n, err := db.CountImageCache()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("image_cache 应保留 2 条记录, got %d", n)
	}
	// 回退完整处理后应把漏标的文件重新入库（self-heal），内存索引恢复为真实 ID
	a.mu.RLock()
	ids := a.folderIndex[leafKey]
	a.mu.RUnlock()
	if len(ids) != 2 {
		t.Fatalf("刷新后 folderIndex 应恢复 2 个 ID, got %d", len(ids))
	}
}

// TestRefreshRecoversDeletedLeafFolder 验证"已误删"目录的自我修复：
// 旧 bug 已把某叶子目录的记录从 image_cache / images 表清光、folderIndex 无键
// （表现为标签点击 GetImages=0）。修复后的下次刷新应从磁盘重新索引（self-heal），
// 无需用户删除重导。对应真实场景：ComfyUI 输出根目录下的日期子目录被误删后，
// 右键刷新该根目录即可恢复。
func TestRefreshRecoversDeletedLeafFolder(t *testing.T) {
	root := t.TempDir()
	leaf := filepath.Join(root, "leaf")
	if err := os.MkdirAll(leaf, 0755); err != nil {
		t.Fatal(err)
	}
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(leaf, name), []byte("fakeimage"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.jpg")
	write("b.jpg")

	db, err := database.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	a := &App{
		images:          make(map[string]*ImageEntry),
		folderIndex:     make(map[string][]string),
		folderCount:     make(map[string]int),
		folderLoaded:    make(map[string]bool),
		folderLRU:       list.New(),
		lruNodes:        make(map[string]*list.Element),
		registeredRoots: map[string]bool{root: true},
		folderTypes:     make(map[string]string),
		imageDB:         db,
		userDataDir:     t.TempDir(),
		dirMtimes:       map[string]int64{},
		mu:              sync.RWMutex{},
	}

	// 首次刷新：全量入库 2 张，同时记录叶子目录 mtime 缓存
	r1 := a.RefreshFolder(root)
	if !r1.Success || len(r1.Added) != 2 {
		t.Fatalf("第一次刷新应添加 2 张, success=%v added=%d", r1.Success, len(r1.Added))
	}

	// 模拟旧 bug 的"已删光"状态：内存索引清空 + SQL 记录删除（与真实 DB 现状一致）
	ids, err := db.LoadImageCacheIDsUnderPath(strings.ReplaceAll(root, "\\", "/"))
	if err != nil || len(ids) != 2 {
		t.Fatalf("读取待删 ID 失败: %v, n=%d", err, len(ids))
	}
	if err := db.DeleteImageCacheBatch(ids); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteImagesBatch(ids); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.folderIndex = make(map[string][]string)
	a.folderLoaded = make(map[string]bool)
	a.images = make(map[string]*ImageEntry)
	a.mu.Unlock()
	if n, _ := db.CountImageCache(); n != 0 {
		t.Fatalf("模拟误删后 image_cache 应为 0, got %d", n)
	}

	// 再次刷新：应重新索引 2 张（mtime 未变 + 无 folderIndex 键 → 回退完整处理）
	r2 := a.RefreshFolder(root)
	if len(r2.Removed) != 0 {
		t.Fatalf("恢复刷新不应删除记录, removed=%d", len(r2.Removed))
	}
	if len(r2.Added) != 2 {
		t.Fatalf("恢复刷新应重新索引 2 张, added=%d", len(r2.Added))
	}
	n, err := db.CountImageCache()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("image_cache 应恢复为 2 条记录, got %d", n)
	}
	rootNorm := strings.ReplaceAll(root, "\\", "/")
	a.mu.RLock()
	recovered := len(a.folderIndex[rootNorm+"/leaf"])
	a.mu.RUnlock()
	if recovered != 2 {
		t.Fatalf("folderIndex 应恢复 2 个 ID, got %d", recovered)
	}
}

// TestAutoHealMissingFolder 验证 GetImages=0 时的自愈：目录存在但记录被清空（标签点击
// 不出图的场景），autoHealMissingFolder 后台补扫后 image_cache 自动恢复。
func TestAutoHealMissingFolder(t *testing.T) {
	root := t.TempDir()
	leaf := filepath.Join(root, "leaf")
	if err := os.MkdirAll(leaf, 0755); err != nil {
		t.Fatal(err)
	}
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(leaf, name), []byte("fakeimage"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.jpg")
	write("b.jpg")

	db, err := database.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	a := &App{
		images:          make(map[string]*ImageEntry),
		folderIndex:     make(map[string][]string),
		folderCount:     make(map[string]int),
		folderLoaded:    make(map[string]bool),
		folderLRU:       list.New(),
		lruNodes:        make(map[string]*list.Element),
		registeredRoots: map[string]bool{root: true},
		folderTypes:     make(map[string]string),
		imageDB:         db,
		userDataDir:     t.TempDir(),
		dirMtimes:       map[string]int64{},
		scanningRoots:   make(map[string]bool),
		mu:              sync.RWMutex{},
		scanMu:          sync.Mutex{},
	}

	// 首次刷新：全量入库
	if r1 := a.RefreshFolder(root); !r1.Success || len(r1.Added) != 2 {
		t.Fatalf("第一次刷新应添加 2 张, success=%v", r1.Success)
	}

	// 模拟记录被清空（旧 bug 已删光 image_cache/images，标签点击 GetImages=0）
	ids, err := db.LoadImageCacheIDsUnderPath(strings.ReplaceAll(root, "\\", "/"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteImageCacheBatch(ids); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteImagesBatch(ids); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.folderIndex = make(map[string][]string)
	a.folderLoaded = make(map[string]bool)
	a.images = make(map[string]*ImageEntry)
	a.mu.Unlock()
	if n, _ := db.CountImageCache(); n != 0 {
		t.Fatalf("模拟清空后 image_cache 应为 0, got %d", n)
	}

	// 触发自愈（后台 goroutine），轮询等待 image_cache 恢复
	rootNorm := strings.ReplaceAll(root, "\\", "/")
	a.autoHealMissingFolder(rootNorm)
	deadline := time.Now().Add(3 * time.Second)
	for {
		n, _ := db.CountImageCache()
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("自愈超时：image_cache 未恢复, got %d", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// 扫描标记应已清除
	a.scanMu.Lock()
	busy := a.scanningRoots[rootNorm]
	a.scanMu.Unlock()
	if busy {
		t.Fatalf("自愈完成后 scanningRoots 标记应清除")
	}
}
