package database

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ==================== folders 表：文件夹树与计数的唯一真相源 ====================
// 查询式改造的核心：GetFolders / GetFolderCount / 侧栏树全部改为对这张表的查询。
// 扫描写入图片时在同一事务内增量维护 direct_count / subtree_count；
// 启动或可疑状态时用 RecomputeFolderCountsForRoot 从 image_cache 全量校准。

// FolderRow folders 表一行
type FolderRow struct {
	RootPath     string `json:"rootPath"`
	Folder       string `json:"folder"` // 相对 root 的正斜杠路径；"" 表示根本身
	Parent       string `json:"parent"`
	Name         string `json:"name"`
	Depth        int    `json:"depth"`
	DirectCount  int    `json:"directCount"`
	SubtreeCount int    `json:"subtreeCount"`
	Mtime        int64  `json:"mtime"`
}

const foldersSchema = `
CREATE TABLE IF NOT EXISTS folders (
	root_path TEXT NOT NULL,
	folder TEXT NOT NULL,
	parent TEXT NOT NULL DEFAULT '',
	name TEXT NOT NULL DEFAULT '',
	depth INTEGER NOT NULL DEFAULT 0,
	direct_count INTEGER NOT NULL DEFAULT 0,
	subtree_count INTEGER NOT NULL DEFAULT 0,
	mtime INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY(root_path, folder)
);
CREATE INDEX IF NOT EXISTS idx_folders_parent ON folders(root_path, parent);
CREATE INDEX IF NOT EXISTS idx_folders_root ON folders(root_path);
`

// initFoldersSchema 建表（由 sqlite.go initSchema 调用）
func (idb *ImageDB) initFoldersSchema() error {
	_, err := idb.db.Exec(foldersSchema)
	return err
}

// folderAncestors 返回 folder 的全部祖先相对路径（不含自身、不含根 ""）。
// "a/b/c" → ["a", "a/b"]。folder 为 "" 时返回空。
func folderAncestors(folder string) []string {
	if folder == "" {
		return nil
	}
	parts := strings.Split(folder, "/")
	anc := make([]string, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		anc = append(anc, strings.Join(parts[:i], "/"))
	}
	return anc
}

// EnsureFolderRows 确保一组文件夹行及其祖先链存在（不动计数）。
func (idb *ImageDB) EnsureFolderRows(root string, folders []string) error {
	if len(folders) == 0 {
		return nil
	}
	idb.mu.Lock()
	defer idb.mu.Unlock()
	tx, err := idb.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := ensureFolderRowsTx(tx, root, folders); err != nil {
		return err
	}
	return tx.Commit()
}

// ensureFolderRowsTx 在事务内插入文件夹行（含祖先链），已存在则跳过。
func ensureFolderRowsTx(tx *sql.Tx, root string, folders []string) error {
	stmt, err := tx.Prepare(`INSERT INTO folders(root_path, folder, parent, name, depth)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(root_path, folder) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	seen := map[string]bool{}
	for _, f := range folders {
		chain := append([]string{""}, folderAncestors(f)...)
		chain = append(chain, f)
		for _, key := range chain {
			if seen[key] {
				continue
			}
			seen[key] = true
			parent := ""
			name := root
			depth := 0
			if key != "" {
				idx := strings.LastIndex(key, "/")
				if idx >= 0 {
					parent = key[:idx]
				}
				name = key[idx+1:]
				depth = strings.Count(key, "/") + 1
			}
			if _, err := stmt.Exec(root, key, parent, name, depth); err != nil {
				return fmt.Errorf("写入文件夹行失败 [%s]: %w", key, err)
			}
		}
	}
	return nil
}

// applyImageDeltaTx 在给定事务内调整 direct_count 与全部祖先的 subtree_count。
// 调用方必须保证行已存在（先 ensureFolderRowsTx）。
func applyImageDeltaTx(tx *sql.Tx, root, folder string, delta int) error {
	stmt, err := tx.Prepare(`UPDATE folders SET subtree_count = subtree_count + ? WHERE root_path=? AND folder=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	// 根行（folder=''）的 subtree_count 恒等于整根图片总数，祖先链含根
	chain := append(folderAncestors(folder), "")
	if folder != "" {
		if _, err := tx.Exec(`UPDATE folders SET direct_count = direct_count + ? WHERE root_path=? AND folder=?`,
			delta, root, folder); err != nil {
			return err
		}
	}
	for _, anc := range chain {
		if _, err := stmt.Exec(delta, root, anc); err != nil {
			return err
		}
	}
	return nil
}

// ApplyImageDelta 图片数量变化时增量维护计数（独立事务；扫描热路径请在自己的
// 事务内调用 ensureFolderRowsTx + applyImageDeltaTx）。
func (idb *ImageDB) ApplyImageDelta(root, folder string, delta int) error {
	if delta == 0 {
		return nil
	}
	idb.mu.Lock()
	defer idb.mu.Unlock()
	tx, err := idb.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := ensureFolderRowsTx(tx, root, []string{folder}); err != nil {
		return err
	}
	if err := applyImageDeltaTx(tx, root, folder, delta); err != nil {
		return err
	}
	return tx.Commit()
}

// RecomputeFolderCountsForRoot 从 image_cache 全量校准某根的 folders 计数。
// diskFolders 非 nil 时同时清掉不在磁盘目录集合内的行。
// 一次 GROUP BY + Go 侧自底向上累加，百万行秒级，作为增量维护的兜底。
func (idb *ImageDB) RecomputeFolderCountsForRoot(root string, diskFolders map[string]bool) error {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	start := time.Now()

	// 1. direct_count 直接从 image_cache 校准
	if _, err := idb.db.Exec(`UPDATE folders SET direct_count =
		COALESCE((SELECT COUNT(*) FROM image_cache ic WHERE ic.root_path = folders.root_path AND ic.folder = folders.folder), 0)
		WHERE root_path = ?`, root); err != nil {
		return err
	}

	// 2. 读全树，Go 侧自底向上算 subtree_count = direct + 子级 subtree 之和
	rows, err := idb.db.Query(`SELECT folder, direct_count FROM folders WHERE root_path=?`, root)
	if err != nil {
		return err
	}
	type node struct{ direct int }
	tree := make(map[string]node)
	var order []string // 任意序；累加自底向上不依赖顺序
	for rows.Next() {
		var f string
		var d int
		if err := rows.Scan(&f, &d); err != nil {
			rows.Close()
			return err
		}
		tree[f] = node{direct: d}
		order = append(order, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// subtree(f) = direct(f) + Σ subtree(child)；按深度降序遍历保证子级先算完
	depthOf := func(f string) int {
		if f == "" {
			return 0
		}
		return strings.Count(f, "/") + 1
	}
	maxDepth := 0
	for f := range tree {
		if d := depthOf(f); d > maxDepth {
			maxDepth = d
		}
	}
	subtree := make(map[string]int, len(tree))
	for f, n := range tree {
		subtree[f] = n.direct
	}
	for d := maxDepth; d >= 1; d-- {
		for _, f := range order {
			if depthOf(f) != d {
				continue
			}
			idx := strings.LastIndex(f, "/")
			parent := ""
			if idx >= 0 {
				parent = f[:idx]
			}
			if _, ok := tree[parent]; ok {
				subtree[parent] += subtree[f]
			}
		}
	}

	// 3. 写回（subtree 直接覆盖而非累加，保证幂等）
	tx, err := idb.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	upd, err := tx.Prepare(`UPDATE folders SET subtree_count=? WHERE root_path=? AND folder=?`)
	if err != nil {
		return err
	}
	defer upd.Close()
	for f, st := range subtree {
		if _, err := upd.Exec(st, root, f); err != nil {
			return err
		}
	}

	// 4. 清理磁盘上不存在的行（可选）
	if diskFolders != nil {
		del, err := tx.Prepare(`DELETE FROM folders WHERE root_path=? AND folder=?`)
		if err != nil {
			return err
		}
		defer del.Close()
		for _, f := range order {
			if f != "" && !diskFolders[f] {
				if _, err := del.Exec(root, f); err != nil {
					return err
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Printf("[文件夹索引] %s 计数校准完成（%d 个文件夹），耗时 %s\n",
		root, len(tree), time.Since(start).Round(time.Millisecond))
	return nil
}

// LoadFolderTree 加载某根的全部文件夹行（含根行）。
func (idb *ImageDB) LoadFolderTree(root string) ([]FolderRow, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	rows, err := idb.db.Query(`SELECT root_path, folder, parent, name, depth, direct_count, subtree_count, mtime
		FROM folders WHERE root_path=? ORDER BY folder`, root)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FolderRow
	for rows.Next() {
		var r FolderRow
		if err := rows.Scan(&r.RootPath, &r.Folder, &r.Parent, &r.Name, &r.Depth, &r.DirectCount, &r.SubtreeCount, &r.Mtime); err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LoadAllFolderTrees 加载所有根的文件夹行（启动构建侧栏树用）。
func (idb *ImageDB) LoadAllFolderTrees() ([]FolderRow, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	rows, err := idb.db.Query(`SELECT root_path, folder, parent, name, depth, direct_count, subtree_count, mtime
		FROM folders ORDER BY root_path, folder`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FolderRow
	for rows.Next() {
		var r FolderRow
		if err := rows.Scan(&r.RootPath, &r.Folder, &r.Parent, &r.Name, &r.Depth, &r.DirectCount, &r.SubtreeCount, &r.Mtime); err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetFolderSubtreeCount 查询某文件夹（含子树）的图片数；行不存在返回 -1。
func (idb *ImageDB) GetFolderSubtreeCount(rootPath, folder string) int {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	var n int
	err := idb.db.QueryRow(`SELECT subtree_count FROM folders WHERE root_path=? AND folder=?`,
		rootPath, folder).Scan(&n)
	if err != nil {
		return -1
	}
	return n
}

// GetFolderDirectCount 查询某文件夹直接图片数；行不存在返回 -1。
func (idb *ImageDB) GetFolderDirectCount(rootPath, folder string) int {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	var n int
	err := idb.db.QueryRow(`SELECT direct_count FROM folders WHERE root_path=? AND folder=?`,
		rootPath, folder).Scan(&n)
	if err != nil {
		return -1
	}
	return n
}

// DeleteFoldersByRoot 删除某根的全部文件夹行（移除根目录时用）。
func (idb *ImageDB) DeleteFoldersByRoot(root string) error {
	_, err := idb.execLocked(`DELETE FROM folders WHERE root_path=?`, root)
	return err
}

// CountFoldersByRoot 返回某根的文件夹行数（诊断用）。
func (idb *ImageDB) CountFoldersByRoot(root string) int {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	var n int
	idb.db.QueryRow(`SELECT COUNT(*) FROM folders WHERE root_path=?`, root).Scan(&n)
	return n
}

// ==================== 缩略图状态（has_thumb 列） ====================

// SetHasThumb 更新单张图片的缩略图存在标记
func (idb *ImageDB) SetHasThumb(id string, has bool) error {
	v := 0
	if has {
		v = 1
	}
	idb.mu.Lock()
	defer idb.mu.Unlock()
	_, err := idb.db.Exec(`UPDATE image_cache SET has_thumb=? WHERE id=?`, v, id)
	return err
}

// SetHasThumbBatch 批量更新缩略图存在标记
func (idb *ImageDB) SetHasThumbBatch(ids []string, has bool) error {
	if len(ids) == 0 {
		return nil
	}
	v := 0
	if has {
		v = 1
	}
	idb.mu.Lock()
	defer idb.mu.Unlock()
	tx, err := idb.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`UPDATE image_cache SET has_thumb=? WHERE id=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, id := range ids {
		if _, err := stmt.Exec(v, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ThumbCountByFolder 返回 root 下各相对文件夹的 has_thumb 数量。
func (idb *ImageDB) ThumbCountByFolder(root string) (map[string]int, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	rows, err := idb.db.Query(`SELECT folder, COUNT(*) FROM image_cache WHERE root_path=? AND has_thumb=1 GROUP BY folder`, root)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var f string
		var c int
		if err := rows.Scan(&f, &c); err != nil {
			return out, err
		}
		out[f] = c
	}
	return out, rows.Err()
}
