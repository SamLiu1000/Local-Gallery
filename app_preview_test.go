package main

import (
	"bytes"
	"container/list"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"local-gallery/internal/database"
)

func TestBuildFolderTreeFromIndexPreviews(t *testing.T) {
	a := &App{}
	a.folderIndex = map[string][]string{
		"K:/photos":     {"root1", "root2"},
		"K:/photos/sub": {"s1", "s2", "s3", "s4", "s5", "s6"},
	}
	a.folderCount = map[string]int{
		"K:/photos":     2,
		"K:/photos/sub": 6,
	}
	thumbCounts := map[string]int{"K:/photos/sub": 6}
	previews := map[string][]FolderPreview{
		"K:/photos/sub": {
			{ID: "s1", LastModified: 1001},
			{ID: "s3", LastModified: 1003},
			{ID: "s5", LastModified: 1005},
			{ID: "s2", LastModified: 1002},
		},
	}

	roots := a.buildFolderTreeFromIndex(`K:\photos`, thumbCounts, previews)
	if len(roots) != 1 {
		t.Fatalf("expected 1 root child, got %d", len(roots))
	}
	sub := roots[0]
	if sub.Name != "sub" {
		t.Fatalf("expected name 'sub', got %q", sub.Name)
	}
	if len(sub.Previews) != 4 {
		t.Fatalf("expected 4 previews, got %d: %+v", len(sub.Previews), sub.Previews)
	}

	// 未开启预览的文件夹不应有 Previews
	roots2 := a.buildFolderTreeFromIndex(`K:\photos`, thumbCounts, map[string][]FolderPreview{})
	if len(roots2[0].Previews) != 0 {
		t.Fatalf("expected no previews when not enabled, got %+v", roots2[0].Previews)
	}
}

func TestCollectPreviewDataMemoryPath(t *testing.T) {
	a := &App{}
	a.registeredRoots = map[string]bool{`K:\photos`: true}
	a.folderIndex = map[string][]string{
		"K:/photos":     {"root1", "root2"},
		"K:/photos/sub": {"s1", "s2", "s3", "s4", "s5", "s6"},
	}
	a.images = map[string]*ImageEntry{
		"root1": {ID: "root1", LastModified: 1},
		"root2": {ID: "root2", LastModified: 2},
		"s1":    {ID: "s1", LastModified: 1001},
		"s2":    {ID: "s2", LastModified: 1002},
		"s3":    {ID: "s3", LastModified: 1003},
		"s4":    {ID: "s4", LastModified: 1004},
		"s5":    {ID: "s5", LastModified: 1005},
		"s6":    {ID: "s6", LastModified: 1006},
	}

	// 开启 K:/photos/sub 及其子树
	previews := a.collectPreviewData(map[string]bool{"K:/photos/sub": true})
	subPs := previews["K:/photos/sub"]
	if len(subPs) != 4 {
		t.Fatalf("expected 4 previews for sub, got %d: %+v", len(subPs), subPs)
	}
	for _, p := range subPs {
		if p.LastModified == 0 {
			t.Fatalf("expected non-zero lastModified for %s", p.ID)
		}
	}
	// 父文件夹 K:/photos 不应命中（不是开启子树内）
	if _, ok := previews["K:/photos"]; ok {
		t.Fatalf("parent K:/photos should NOT have previews: %+v", previews["K:/photos"])
	}
	// 确定性：两次调用结果一致
	previews2 := a.collectPreviewData(map[string]bool{"K:/photos/sub": true})
	if len(previews2["K:/photos/sub"]) != 4 ||
		previews2["K:/photos/sub"][0].ID != subPs[0].ID {
		t.Fatalf("expected deterministic previews, got %+v vs %+v", subPs, previews2["K:/photos/sub"])
	}

	// 开启根目录 K:/photos：根节点自身也应命中
	previews3 := a.collectPreviewData(map[string]bool{"K:/photos": true})
	if len(previews3["K:/photos"]) != 2 {
		t.Fatalf("expected 2 previews for root when enabled, got %+v", previews3["K:/photos"])
	}
	if len(previews3["K:/photos/sub"]) != 4 {
		t.Fatalf("expected 4 previews for sub under enabled root, got %+v", previews3["K:/photos/sub"])
	}
}

func TestInPreviewSubtree(t *testing.T) {
	roots := map[string]bool{"K:/photos/sub": true}
	if !inPreviewSubtree("K:/photos/sub", roots) {
		t.Error("self should match")
	}
	if !inPreviewSubtree("K:/photos/sub/deep", roots) {
		t.Error("descendant should match")
	}
	if inPreviewSubtree("K:/photos", roots) {
		t.Error("parent should NOT match")
	}
	if inPreviewSubtree("K:/photos/sub2", roots) {
		t.Error("sibling should NOT match")
	}
	if inPreviewSubtree("K:/photos/subfolder", roots) {
		t.Error("prefix-collision sibling should NOT match")
	}
}

func TestFolderPreviewsFromDBNestedRoot(t *testing.T) {
	// 场景：G:\A\B 既作为 G:\A 的子目录，又被单独注册为根。
	// 图片实际存在 root_path=G:\A、folder=B/sub 下（而非 root_path=G:\A\B）。
	// DB 兜底必须回退到能查到数据的根，而不是只匹配"最长前缀"根。
	idb, err := database.New(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	defer idb.Close()

	sqlDB := idb.GetDB()
	for i := 0; i < 6; i++ {
		id := "img" + string(rune('a'+i))
		if _, err := sqlDB.Exec(`INSERT INTO image_cache (id, path, name, size, last_modified, created_at, folder, root_path, width, height, is_video)
			VALUES (?, ?, ?, 0, ?, 0, ?, ?, 0, 0, 0)`,
			id, `G:\A\B\sub\`+id+`.png`, id+".png", int64(1000+i), "B/sub", `G:\A`); err != nil {
			t.Fatalf("插入失败: %v", err)
		}
	}

	a := &App{
		imageDB:         idb,
		registeredRoots: map[string]bool{`G:\A`: true, `G:\A\B`: true},
		folderIndex: map[string][]string{
			"G:/A/B/sub": nil, // 未加载 → 走 SQLite 兜底
		},
		images: make(map[string]*ImageEntry),
	}

	previews := a.collectPreviewData(map[string]bool{"G:/A/B": true})
	sub := previews["G:/A/B/sub"]
	if len(sub) != 4 {
		t.Fatalf("expected 4 previews for nested-root folder, got %d: %+v", len(sub), sub)
	}
	for _, p := range sub {
		if p.ID == "" || p.LastModified == 0 {
			t.Fatalf("expected valid preview, got %+v", p)
		}
	}
}

func TestRefreshFolderInternalSubfolder(t *testing.T) {
	// 场景：右键刷新子文件夹，应只刷新该子文件夹子树，且落库的 folder 路径正确（相对根目录）
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	// 写一张合法 PNG
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "a.png"), buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	idb, err := database.New(filepath.Join(tmp, "img.db"))
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	defer idb.Close()

	a := &App{
		userDataDir:     tmp,
		imageDB:         idb,
		registeredRoots: map[string]bool{root: true},
		images:          make(map[string]*ImageEntry),
		folderIndex:     make(map[string][]string),
		folderCount:     make(map[string]int),
		folderLoaded:    make(map[string]bool),
		folderLRU:       list.New(),
		lruNodes:        make(map[string]*list.Element),
	}

	res := a.refreshFolderInternal(sub)
	if res == nil || !res.Success {
		t.Fatalf("刷新失败: %+v", res)
	}
	if len(res.Added) != 1 {
		t.Fatalf("期望新增 1 张，实际 %d", len(res.Added))
	}
	added := res.Added[0]
	if added.Folder != "sub" {
		t.Fatalf("期望 folder=sub，实际 %q（relBase 计算错误）", added.Folder)
	}
	if !strings.EqualFold(strings.ReplaceAll(added.RootPath, "\\", "/"), strings.ReplaceAll(root, "\\", "/")) {
		t.Fatalf("期望 RootPath=%q，实际 %q", root, added.RootPath)
	}
	// 验证落库的 image_cache：folder=sub 且属于根目录
	rows, err := idb.LoadImageCacheByFolder(root, "sub")
	if err != nil || len(rows) != 1 {
		t.Fatalf("期望 image_cache 1 条 (root,sub)，实际 %d err=%v", len(rows), err)
	}
}

func TestPickFolderPreviewIDs(t *testing.T) {
	// 不足 maxN 返回全部
	ids := []string{"a", "b", "c"}
	if got := pickFolderPreviewIDs(ids, "K:/photos/x", 4); len(got) != 3 {
		t.Errorf("fewer than max should return all, got %v", got)
	}
	// 超过 maxN 确定性取 maxN 个
	big := []string{"i0", "i1", "i2", "i3", "i4", "i5", "i6", "i7", "i8", "i9"}
	a := pickFolderPreviewIDs(big, "K:/photos/y", 4)
	b := pickFolderPreviewIDs(big, "K:/photos/y", 4)
	if len(a) != 4 {
		t.Fatalf("expected 4, got %v", a)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("deterministic expected, got %v vs %v", a, b)
		}
	}
	// 不同路径应得到不同结果（概率性，这里只保证不 panic 且去重逻辑正常）
	c := pickFolderPreviewIDs(big, "K:/photos/z", 4)
	if len(c) != 4 {
		t.Fatalf("expected 4 for other path, got %v", c)
	}
	_ = strings.Join(a, ",")
}
