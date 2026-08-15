package main

import (
	"container/list"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"local-gallery/internal/database"
)

// TestPruneDeadFolders 验证幽灵文件夹清理：
// 磁盘上已删除的文件夹（folder key 存在但目录不存在）连同其图片被移除，
// 磁盘上仍然存在的文件夹不受影响。
func TestPruneDeadFolders(t *testing.T) {
	// 构造最小 App（仅含 pruneDeadFolders 需要的字段，DB 置 nil 走纯内存路径）
	a := &App{
		images:          make(map[string]*ImageEntry),
		folderIndex:     make(map[string][]string),
		folderCount:     make(map[string]int),
		folderLoaded:    make(map[string]bool),
		folderLRU:       list.New(),
		lruNodes:        make(map[string]*list.Element),
		registeredRoots: make(map[string]bool),
		userDataDir:     t.TempDir(),
	}

	root := filepath.Join(t.TempDir(), "bid")
	liveDir := filepath.Join(root, "Live Folder")
	if err := os.MkdirAll(liveDir, 0755); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(liveDir, "a.jpg")
	if err := os.WriteFile(img, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	rootNorm := strings.ReplaceAll(root, "\\", "/")
	a.registeredRoots[root] = true

	liveKey := rootNorm + "/Live Folder"       // 目录存在
	ghostKey := rootNorm + "/Ghost Folder (1)" // 目录不存在

	a.images["live-id"] = &ImageEntry{ID: "live-id", Path: img, Folder: "Live Folder", RootPath: root}
	a.images["ghost-id-1"] = &ImageEntry{ID: "ghost-id-1", Path: filepath.Join(root, "Ghost Folder (1)", "b.jpg"), Folder: "Ghost Folder (1)", RootPath: root}
	a.images["ghost-id-2"] = &ImageEntry{ID: "ghost-id-2", Path: filepath.Join(root, "Ghost Folder (1)", "c.jpg"), Folder: "Ghost Folder (1)", RootPath: root}
	a.folderIndex[liveKey] = []string{"live-id"}
	a.folderIndex[ghostKey] = []string{"ghost-id-1", "ghost-id-2"}
	a.folderCount[rootNorm] = 3
	a.folderCount[liveKey] = 1
	a.folderCount[ghostKey] = 2

	folders, images := a.pruneDeadFolders()
	if folders != 1 {
		t.Fatalf("期望清理 1 个幽灵文件夹, got %d", folders)
	}
	if images != 2 {
		t.Fatalf("期望清理 2 张失效图片, got %d", images)
	}

	if _, ok := a.folderIndex[ghostKey]; ok {
		t.Error("幽灵文件夹 key 应被从 folderIndex 移除")
	}
	if _, ok := a.folderIndex[liveKey]; !ok {
		t.Error("存在的文件夹 key 不应被移除")
	}
	if _, ok := a.images["ghost-id-1"]; ok {
		t.Error("幽灵图片应被从内存移除")
	}
	if _, ok := a.images["ghost-id-2"]; ok {
		t.Error("幽灵图片应被从内存移除")
	}
	if _, ok := a.images["live-id"]; !ok {
		t.Error("正常图片不应被移除")
	}
}

// TestPruneRealDB 直接对真实用户数据目录运行 pruneDeadFolders 做端到端验证。
// 仅当环境变量 GALLERY_PRUNE_REAL=1 时执行（避免误伤真实数据）。
func TestPruneRealDB(t *testing.T) {
	if os.Getenv("GALLERY_PRUNE_REAL") != "1" {
		t.Skip("set GALLERY_PRUNE_REAL=1 to run against real DB")
	}
	userDir := "L:/111-Local Gallery/user"
	imageDB, err := database.New(filepath.Join(userDir, "images.db"))
	if err != nil {
		t.Fatalf("open real images.db: %v", err)
	}
	defer imageDB.Close()

	a := &App{
		images:          make(map[string]*ImageEntry),
		folderIndex:     make(map[string][]string),
		folderCount:     make(map[string]int),
		folderLoaded:    make(map[string]bool),
		folderLRU:       list.New(),
		lruNodes:        make(map[string]*list.Element),
		registeredRoots: make(map[string]bool),
		folderTypes:     make(map[string]string),
		userDataDir:     userDir,
		imageDB:         imageDB,
	}

	// 从 user-data.db 恢复已注册根目录（与真实启动路径一致）
	if udb, err := database.NewUserDataDB(filepath.Join(userDir, "user-data.db")); err == nil {
		if roots, err := udb.GetAllRoots(); err == nil {
			for _, r := range roots {
				a.registeredRoots[r.Path] = true
			}
		}
		udb.Close()
	}
	a.loadFolderIndexLight()

	before := countImageCache(t, imageDB)
	folders, images := a.pruneDeadFolders()
	after := countImageCache(t, imageDB)
	t.Logf("prune 结果: 清理文件夹=%d 清理图片=%d | image_cache 总量 %d -> %d", folders, images, before, after)

	// 校验两个幽灵文件夹的记录已被删除
	ghostFolders := []string{
		"DevilsFilm Jessie Saint & Lulu Chu - Seducing My Straight White Best Friend (Mar 1, 2021) x168(1)",
		"DevilsFilm Jessie Saint & Lulu Chu - Seducing My Straight White Best Friend (Mar 1, 2021) x168(2)",
	}
	for _, gf := range ghostFolders {
		var n int
		if err := imageDB.GetDB().QueryRow(`SELECT COUNT(*) FROM image_cache WHERE folder=?`, gf).Scan(&n); err != nil && err != sql.ErrNoRows {
			t.Errorf("查询 %q 失败: %v", gf, err)
		}
		if n != 0 {
			t.Errorf("幽灵文件夹 %q 仍残留 %d 条记录", gf, n)
		}
	}

	// 真实文件夹应保留
	var live int
	liveFolder := "DevilsFilm Jessie Saint & Lulu Chu - Seducing My Straight White Best Friend (Mar 1, 2021) x168"
	imageDB.GetDB().QueryRow(`SELECT COUNT(*) FROM image_cache WHERE folder=?`, liveFolder).Scan(&live)
	t.Logf("真实文件夹 %q 保留记录: %d", liveFolder, live)
	if live == 0 {
		t.Errorf("真实文件夹的记录不应被误删")
	}
}

func countImageCache(t *testing.T, db *database.ImageDB) int {
	t.Helper()
	n, err := db.CountImageCache()
	if err != nil {
		t.Fatalf("CountImageCache: %v", err)
	}
	return n
}

// TestPurgeMissingImage 验证单张图片自愈：源文件被删后，从内存 a.images / folderIndex /
// folderCount 移除，且同一文件夹下其它图片不受影响。
func TestPurgeMissingImage(t *testing.T) {
	a := &App{
		images:          make(map[string]*ImageEntry),
		folderIndex:     make(map[string][]string),
		folderCount:     make(map[string]int),
		folderLoaded:    make(map[string]bool),
		folderLRU:       list.New(),
		lruNodes:        make(map[string]*list.Element),
		registeredRoots: make(map[string]bool),
		userDataDir:     t.TempDir(),
	}
	root := "K:/bid"
	key := root + "/Some Folder"
	a.registeredRoots["K:\\bid"] = true

	a.images["dead-id"] = &ImageEntry{ID: "dead-id", Path: "K:\\bid\\Some Folder\\a.jpg", Folder: "Some Folder", RootPath: "K:\\bid"}
	a.images["alive-id"] = &ImageEntry{ID: "alive-id", Path: "K:\\bid\\Some Folder\\b.jpg", Folder: "Some Folder", RootPath: "K:\\bid"}
	a.folderIndex[key] = []string{"dead-id", "alive-id"}
	a.folderCount[root] = 2
	a.folderCount[key] = 2

	a.purgeMissingImage("dead-id")

	if _, ok := a.images["dead-id"]; ok {
		t.Error("失效图片应被移除")
	}
	if _, ok := a.images["alive-id"]; !ok {
		t.Error("正常图片不应被移除")
	}
	if ids := a.folderIndex[key]; len(ids) != 1 || ids[0] != "alive-id" {
		t.Errorf("folderIndex[%q] 应为 [alive-id], got %v", key, ids)
	}
	if a.folderCount[key] != 1 || a.folderCount[root] != 1 {
		t.Errorf("folderCount 应递减为 1, got key=%d root=%d", a.folderCount[key], a.folderCount[root])
	}

	// 文件夹中最后一张图被移除 → key 应被整体删除
	a.purgeMissingImage("alive-id")
	if _, ok := a.folderIndex[key]; ok {
		t.Error("清空后的文件夹 key 应被移除")
	}
	if a.folderCount[key] != 0 || a.folderCount[root] != 0 {
		t.Errorf("folderCount 应归零, got key=%d root=%d", a.folderCount[key], a.folderCount[root])
	}
}

// TestFolderAbandon 验证文件夹切换取消机制：遗弃标记 / 聚焦恢复 / 过期 / 路径推导。
func TestFolderAbandon(t *testing.T) {
	abandonedFoldersMu.Lock()
	abandonedFolders = map[string]int64{}
	abandonedFoldersMu.Unlock()

	a := &App{registeredRoots: map[string]bool{}}
	a.registeredRoots[`K:\bid`] = true

	// imageFolderKey 推导
	if k := a.imageFolderKey(`K:\bid\DevilsFilm x168\2gp8wz.jpg`); k != "K:/bid/DevilsFilm x168" {
		t.Errorf("子文件夹推导错误: %q", k)
	}
	// ★ 多层子目录：filepath.Dir 在 Windows 返回反斜杠，若不转回正斜杠会产出
	//   混合分隔符路径（"K:/bid/A\B"），导致前缀匹配失败、遗弃检查拦不住子文件夹。
	if k := a.imageFolderKey(`K:\bid\A\B\c.jpg`); k != "K:/bid/A/B" {
		t.Errorf("多层子目录推导错误（可能混入反斜杠）: %q", k)
	}
	if k := a.imageFolderKey(`K:\bid\a.jpg`); k != "K:/bid" {
		t.Errorf("根目录图片推导错误: %q", k)
	}
	if k := a.imageFolderKey(`Z:\other\x.jpg`); k != "" {
		t.Errorf("未注册路径应为空: %q", k)
	}

	// 遗弃 → 聚焦恢复
	a.AbandonFolder(`K:\bid\DevilsFilm x168`)
	if !isFolderAbandoned("K:/bid/DevilsFilm x168") {
		t.Error("遗弃后应被标记")
	}
	// ★ 前缀匹配：遗弃父文件夹，其下子文件夹也应被拦截（关键：切走父目录后子目录图片不再做缩略图）
	if !isFolderAbandoned("K:/bid/DevilsFilm x168/sub") {
		t.Error("子文件夹应随父文件夹一并被视为遗弃")
	}
	// 无关文件夹不应被拦截
	if isFolderAbandoned("K:/bid/Other Folder") {
		t.Error("无关文件夹不应被误判为遗弃")
	}
	a.FocusFolder(`K:\bid\DevilsFilm x168`)
	if isFolderAbandoned("K:/bid/DevilsFilm x168") {
		t.Error("聚焦后应移除遗弃标记")
	}
	if isFolderAbandoned("K:/bid/DevilsFilm x168/sub") {
		t.Error("聚焦后子文件夹也应解除遗弃")
	}

	// ★ 遗弃永久有效（直到聚焦），不设过期——切换后旧文件夹生成彻底停止
	a.AbandonFolder(`K:\bid\DevilsFilm x168`)
	abandonedFoldersMu.Lock()
	abandonedFolders["K:/bid/DevilsFilm x168"] = time.Now().Add(-10 * time.Minute).UnixMilli()
	abandonedFoldersMu.Unlock()
	if !isFolderAbandoned("K:/bid/DevilsFilm x168") {
		t.Error("遗弃应是永久的，即使 10 分钟前设置也应保持")
	}

	// ★ 祖先清除：浏览根目录后切走 → 根被遗弃 → 前缀匹配拦截整棵树。
	//   再聚焦任意子文件夹时，必须连根/祖先的遗弃标记一起清除，
	//   否则整棵树缩略图全部 "Load failed"（本次 bug 的根因）。
	a.AbandonFolder(`K:\bid`) // 从根目录切走
	if !isFolderAbandoned("K:/bid/DevilsFilm x168") {
		t.Error("根目录遗弃后，其下子文件夹应被前缀匹配拦截")
	}
	a.FocusFolder(`K:\bid\DevilsFilm x168`) // 重新进入子文件夹
	if isFolderAbandoned("K:/bid/DevilsFilm x168") {
		t.Error("聚焦子文件夹后应清除祖先（根）的遗弃标记，生成恢复")
	}
	if isFolderAbandoned("K:/bid/Another Sub") {
		t.Error("根遗弃被清除后，其它子文件夹也不应再被拦截")
	}
}

