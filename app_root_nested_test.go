package main

import (
	"strings"
	"sync"
	"testing"
)

// TestExistingNested 验证嵌套根目录判断：父子路径视为嵌套，
// 同一路径（大小写/分隔符不同）不算嵌套，兄弟目录不算嵌套。
func TestExistingNested(t *testing.T) {
	existing := map[string]bool{
		`G:\AI\SuCai\elsa`:          true,
		`D:\Black Cat Scans`:        true,
		`H:\webui\swarm-Output`:     true,
		`L:\Local Gallery\测试图片`: true,
	}

	cases := []struct {
		path   string
		nested bool
	}{
		// 子目录 → 嵌套
		{`G:\AI\SuCai\elsa\20260815`, true},
		{`g:\ai\sucai\elsa\20260815`, true},
		// 父目录 → 嵌套
		{`G:\AI\SuCai`, true},
		// 相同路径（仅大小写/分隔符不同）→ 不算嵌套
		{`g:\ai\sucai\elsa`, false},
		{`G:/AI/SuCai/elsa`, false},
		// 兄弟目录、无关目录 → 不嵌套
		{`G:\AI\SuCai\elsa2`, false},
		{`G:\AI\SuCai\elsa_foo`, false},
		{`D:\Black Cat Scans 2`, false},
	}
	for _, c := range cases {
		key := strings.ToLower(strings.ReplaceAll(c.path, "\\", "/"))
		if _, got := existingNested(key, existing); got != c.nested {
			t.Errorf("existingNested(%q) 嵌套=%v, 期望 %v", c.path, got, c.nested)
		}
	}
}

// TestRemoveNestedRoots 验证乙方案：嵌套的根目录允许与父根共存，
// removeNestedRoots 不再删除嵌套根（父子嵌套已受支持，数据层按完整路径去重）。
func TestRemoveNestedRoots(t *testing.T) {
	a := &App{
		images:          make(map[string]*ImageEntry),
		folderIndex:     make(map[string][]string),
		folderCount:     make(map[string]int),
		folderLoaded:    make(map[string]bool),
		folderLRU:       nil,
		registeredRoots: make(map[string]bool),
		folderTypes:     make(map[string]string),
		userDataDir:     t.TempDir(),
	}
	a.registeredRoots[`G:\AI\SuCai\elsa`] = true
	a.registeredRoots[`G:\AI\SuCai\elsa\20260815`] = true // 嵌套内层根，乙方案应保留
	a.registeredRoots[`D:\Black Cat Scans`] = true        // 无关根，保留

	a.removeNestedRoots()

	// 乙方案：嵌套根不再被移除
	if !a.registeredRoots[`G:\AI\SuCai\elsa\20260815`] {
		t.Error("嵌套内层根目录不应被移除（乙方案允许共存）")
	}
	if !a.registeredRoots[`G:\AI\SuCai\elsa`] {
		t.Error("外层根目录被误删")
	}
	if !a.registeredRoots[`D:\Black Cat Scans`] {
		t.Error("无关根目录被误删")
	}
}

// TestBuildFolderTreeNested 验证乙方案侧栏树构建：
// 父根"照片"与嵌套根"照片/旅游"共存时——
//  1. 照片的树里"旅游"是折叠入口（无子节点），计数为其全部图片数；
//  2. 顶层"旅游"（buildFolderTreeFromIndex(旅游)）保留其完整子树。
func TestBuildFolderTreeNested(t *testing.T) {
	// 浅层根持有所有权：所有文件的 folderIndex/folderCount 键为真实目录路径，
	// 统一挂在父根"照片"下（RootPath=照片）。
	a := &App{
		images:          make(map[string]*ImageEntry),
		folderIndex:     make(map[string][]string),
		folderCount:     make(map[string]int),
		folderLoaded:    make(map[string]bool),
		registeredRoots: make(map[string]bool),
		folderTypes:     make(map[string]string),
		mu:              sync.RWMutex{},
	}
	a.registeredRoots[`G:\照片`] = true
	a.registeredRoots[`G:\照片\旅游`] = true

	a.folderIndex[`G:/照片`] = []string{"f1"}                 // 照片根下直接一张
	a.folderIndex[`G:/照片/旅游`] = []string{"t1"}            // 旅游根下直接一张
	a.folderIndex[`G:/照片/旅游/sub`] = []string{"t2"}        // 旅游子目录一张
	a.folderCount[`G:/照片`] = 3
	a.folderCount[`G:/照片/旅游`] = 2
	a.folderCount[`G:/照片/旅游/sub`] = 1

	thumbCounts := map[string]int{}
	previews := map[string][]FolderPreview{}

	// 父根"照片"：旅游应作为折叠叶子出现，不再展开其子目录
	roots := a.buildFolderTreeFromIndex(`G:\照片`, thumbCounts, previews)
	if len(roots) != 1 {
		t.Fatalf("照片子树应只有 1 个直接子节点(旅游), got %d", len(roots))
	}
	nested := roots[0]
	if nested.Name != "旅游" {
		t.Fatalf("子节点应为 旅游, got %q", nested.Name)
	}
	if nested.ImageCount != 2 {
		t.Errorf("折叠的旅游节点计数应为 2（全部图片）, got %d", nested.ImageCount)
	}
	if len(nested.Children) != 0 {
		t.Errorf("折叠的旅游节点不应展开子目录, got %d 个子节点", len(nested.Children))
	}

	// 顶层"旅游"：保留完整子树（sub 作为子节点出现）
	tourRoots := a.buildFolderTreeFromIndex(`G:\照片\旅游`, thumbCounts, previews)
	if len(tourRoots) != 1 {
		t.Fatalf("顶层旅游子树应有 1 个直接子节点(sub), got %d", len(tourRoots))
	}
	if tourRoots[0].Name != "sub" || tourRoots[0].ImageCount != 1 {
		t.Errorf("顶层旅游子节点应为 sub(计数1), got %q(%d)", tourRoots[0].Name, tourRoots[0].ImageCount)
	}
}

func TestIsRootNested(t *testing.T) {
	a := &App{registeredRoots: map[string]bool{
		`G:\AI\SuCai\elsa`: true,
		`D:\Black Cat Scans`: true,
	}}
	cases := []struct {
		path   string
		nested bool
	}{
		{`G:\AI\SuCai\elsa\20260815`, true}, // 已注册根的子目录 → 嵌套
		{`g:\ai\sucai\elsa\20260815`, true}, // 大小写不同仍嵌套
		{`G:\AI\SuCai\elsa`, false},         // 本身是已注册根 → 非嵌套
		{`G:\AI\SuCai\elsa2`, false},        // 兄弟目录 → 非嵌套
		{`D:\Black Cat Scans 2`, false},     // 无关目录 → 非嵌套
	}
	for _, c := range cases {
		if got := a.isRootNested(c.path); got != c.nested {
			t.Errorf("isRootNested(%q) = %v, 期望 %v", c.path, got, c.nested)
		}
	}
}
