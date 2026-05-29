package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ImportedRoot 导入的根目录记录
type ImportedRoot struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	FolderType  string `json:"folderType"`
	HandleName  string `json:"handleName"`
	AddedAt     string `json:"addedAt"`
}

// ImageTag 图片-标签关联
type ImageTag struct {
	ImagePath string `json:"imagePath"`
	TagID     string `json:"tagId"`
	AddedAt   string `json:"addedAt"`
}

// UserDataDB 用户数据 SQLite 数据库
type UserDataDB struct {
	db     *sql.DB
	dbPath string
	mu     sync.RWMutex
}

// NewUserDataDB 打开或创建用户数据库
func NewUserDataDB(dbPath string) (*UserDataDB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("创建数据库目录失败: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath+"?cache=shared&_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA synchronous=NORMAL")
	db.Exec("PRAGMA cache_size=-10000")
	db.Exec("PRAGMA busy_timeout=5000")
	db.Exec("PRAGMA foreign_keys=ON")
	db.Exec("PRAGMA temp_store=MEMORY")

	udb := &UserDataDB{db: db, dbPath: dbPath}
	if err := udb.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化用户数据库 schema 失败: %w", err)
	}

	return udb, nil
}

func (udb *UserDataDB) initSchema() error {
	_, err := udb.db.Exec(`
		CREATE TABLE IF NOT EXISTS imported_roots (
			path         TEXT PRIMARY KEY,
			name         TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '',
			folder_type  TEXT NOT NULL DEFAULT '',
			handle_name  TEXT NOT NULL DEFAULT '',
			added_at     TEXT NOT NULL DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS image_tags (
			image_path TEXT NOT NULL,
			tag_id     TEXT NOT NULL,
			added_at   TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (image_path, tag_id)
		);
		CREATE INDEX IF NOT EXISTS idx_image_tags_tag_id ON image_tags(tag_id);

		CREATE TABLE IF NOT EXISTS favorites (
			image_path TEXT PRIMARY KEY,
			added_at   TEXT NOT NULL DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS sidebar_settings (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT ''
		);
	`)
	return err
}

// Close 关闭数据库
func (udb *UserDataDB) Close() error {
	return udb.db.Close()
}

// ==================== Imported Roots ====================

// GetAllRoots 获取所有导入根目录
func (udb *UserDataDB) GetAllRoots() ([]ImportedRoot, error) {
	udb.mu.RLock()
	defer udb.mu.RUnlock()

	rows, err := udb.db.Query("SELECT path, name, display_name, folder_type, handle_name, added_at FROM imported_roots ORDER BY added_at ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roots []ImportedRoot
	for rows.Next() {
		var r ImportedRoot
		if err := rows.Scan(&r.Path, &r.Name, &r.DisplayName, &r.FolderType, &r.HandleName, &r.AddedAt); err != nil {
			return nil, err
		}
		roots = append(roots, r)
	}
	return roots, rows.Err()
}

// SaveRoots 全量保存导入根目录（替换所有记录）
func (udb *UserDataDB) SaveRoots(roots []ImportedRoot) error {
	udb.mu.Lock()
	defer udb.mu.Unlock()

	tx, err := udb.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM imported_roots"); err != nil {
		return err
	}

	stmt, err := tx.Prepare("INSERT INTO imported_roots (path, name, display_name, folder_type, handle_name, added_at) VALUES (?, ?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range roots {
		if _, err := stmt.Exec(r.Path, r.Name, r.DisplayName, r.FolderType, r.HandleName, r.AddedAt); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// MergeRoots 合并保存（保留已有记录的 display_name 和 handle_name）
func (udb *UserDataDB) MergeRoots(roots []ImportedRoot) error {
	udb.mu.Lock()
	defer udb.mu.Unlock()

	tx, err := udb.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 先查询已有的元数据
	existing, _ := tx.Query("SELECT path, display_name, handle_name, added_at FROM imported_roots")
	type exMeta struct{ displayName, handleName, addedAt string }
	meta := make(map[string]exMeta)
	if existing != nil {
		for existing.Next() {
			var p, dn, hn, aa string
			if err := existing.Scan(&p, &dn, &hn, &aa); err == nil {
				meta[p] = exMeta{dn, hn, aa}
			}
		}
		existing.Close()
	}

	if _, err := tx.Exec("DELETE FROM imported_roots"); err != nil {
		return err
	}

	stmt, err := tx.Prepare("INSERT INTO imported_roots (path, name, display_name, folder_type, handle_name, added_at) VALUES (?, ?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range roots {
		dn := r.DisplayName
		hn := r.HandleName
		aa := r.AddedAt
		if m, ok := meta[r.Path]; ok {
			if dn == "" {
				dn = m.displayName
			}
			if hn == "" {
				hn = m.handleName
			}
			if aa == "" {
				aa = m.addedAt
			}
		}
		if _, err := stmt.Exec(r.Path, r.Name, dn, r.FolderType, hn, aa); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// RemoveRoot 删除导入根目录
func (udb *UserDataDB) RemoveRoot(path string) error {
	udb.mu.Lock()
	defer udb.mu.Unlock()

	_, err := udb.db.Exec("DELETE FROM imported_roots WHERE path = ?", path)
	if err != nil {
		return err
	}

	// 同时清理该路径下的 image_tags 和 favorites
	_, _ = udb.db.Exec("DELETE FROM image_tags WHERE image_path LIKE ?", path+"%")
	_, _ = udb.db.Exec("DELETE FROM favorites WHERE image_path LIKE ?", path+"%")

	return nil
}

// UpdateRootMeta 更新导入根目录的显示名和句柄名
func (udb *UserDataDB) UpdateRootMeta(path, displayName, handleName string) error {
	udb.mu.Lock()
	defer udb.mu.Unlock()

	_, err := udb.db.Exec("UPDATE imported_roots SET display_name = ?, handle_name = ? WHERE path = ?",
		displayName, handleName, path)
	return err
}

// GetRootCount 获取导入根目录数量
func (udb *UserDataDB) GetRootCount() (int, error) {
	udb.mu.RLock()
	defer udb.mu.RUnlock()

	var count int
	err := udb.db.QueryRow("SELECT COUNT(*) FROM imported_roots").Scan(&count)
	return count, err
}

// GetFolderType 获取指定路径的文件夹类型
func (udb *UserDataDB) GetFolderType(path string) (string, error) {
	udb.mu.RLock()
	defer udb.mu.RUnlock()

	var ft string
	err := udb.db.QueryRow("SELECT folder_type FROM imported_roots WHERE path = ?", path).Scan(&ft)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return ft, err
}

// GetFolderTypes 获取所有文件夹类型 (path -> type)
func (udb *UserDataDB) GetFolderTypes() (map[string]string, error) {
	udb.mu.RLock()
	defer udb.mu.RUnlock()

	rows, err := udb.db.Query("SELECT path, folder_type FROM imported_roots WHERE folder_type != ''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var p, ft string
		if err := rows.Scan(&p, &ft); err != nil {
			return nil, err
		}
		result[p] = ft
	}
	return result, rows.Err()
}

// ==================== Image Tags ====================

// AddImageTag 给图片添加标签
func (udb *UserDataDB) AddImageTag(imagePath, tagID, addedAt string) error {
	udb.mu.Lock()
	defer udb.mu.Unlock()

	_, err := udb.db.Exec("INSERT OR IGNORE INTO image_tags (image_path, tag_id, added_at) VALUES (?, ?, ?)", imagePath, tagID, addedAt)
	return err
}

// RemoveImageTagsByTagID 删除指定标签的所有关联
func (udb *UserDataDB) RemoveImageTagsByTagID(tagID string) error {
	udb.mu.Lock()
	defer udb.mu.Unlock()

	_, err := udb.db.Exec("DELETE FROM image_tags WHERE tag_id = ?", tagID)
	return err
}

// RemoveImageTag 移除图片标签
func (udb *UserDataDB) RemoveImageTag(imagePath, tagID string) error {
	udb.mu.Lock()
	defer udb.mu.Unlock()

	_, err := udb.db.Exec("DELETE FROM image_tags WHERE image_path = ? AND tag_id = ?", imagePath, tagID)
	return err
}

// GetTagsForImage 获取图片的所有标签 ID
func (udb *UserDataDB) GetTagsForImage(imagePath string) ([]ImageTag, error) {
	udb.mu.RLock()
	defer udb.mu.RUnlock()

	rows, err := udb.db.Query("SELECT image_path, tag_id, added_at FROM image_tags WHERE image_path = ?", imagePath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tags []ImageTag
	for rows.Next() {
		var t ImageTag
		if err := rows.Scan(&t.ImagePath, &t.TagID, &t.AddedAt); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

// GetImagePathsForTags 获取打了指定标签的图片路径
func (udb *UserDataDB) GetImagePathsForTags(tagIDs []string) ([]string, error) {
	udb.mu.RLock()
	defer udb.mu.RUnlock()

	if len(tagIDs) == 0 {
		return nil, nil
	}

	query := "SELECT DISTINCT image_path FROM image_tags WHERE tag_id IN ("
	args := make([]interface{}, len(tagIDs))
	for i, id := range tagIDs {
		if i > 0 {
			query += ", "
		}
		query += "?"
		args[i] = id
	}
	query += ")"

	rows, err := udb.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// GetAllImageTags 获取所有标签关联
func (udb *UserDataDB) GetAllImageTags() ([]ImageTag, error) {
	udb.mu.RLock()
	defer udb.mu.RUnlock()

	rows, err := udb.db.Query("SELECT image_path, tag_id, added_at FROM image_tags")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tags []ImageTag
	for rows.Next() {
		var t ImageTag
		if err := rows.Scan(&t.ImagePath, &t.TagID, &t.AddedAt); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

// ImportImageTags 批量导入标签关联
func (udb *UserDataDB) ImportImageTags(tags []ImageTag) error {
	udb.mu.Lock()
	defer udb.mu.Unlock()

	tx, err := udb.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("INSERT OR IGNORE INTO image_tags (image_path, tag_id, added_at) VALUES (?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, t := range tags {
		if _, err := stmt.Exec(t.ImagePath, t.TagID, t.AddedAt); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// ==================== Favorites ====================

// AddFavorite 添加收藏
func (udb *UserDataDB) AddFavorite(imagePath, addedAt string) error {
	udb.mu.Lock()
	defer udb.mu.Unlock()

	_, err := udb.db.Exec("INSERT OR IGNORE INTO favorites (image_path, added_at) VALUES (?, ?)", imagePath, addedAt)
	return err
}

// RemoveFavorite 取消收藏
func (udb *UserDataDB) RemoveFavorite(imagePath string) error {
	udb.mu.Lock()
	defer udb.mu.Unlock()

	_, err := udb.db.Exec("DELETE FROM favorites WHERE image_path = ?", imagePath)
	return err
}

// GetAllFavorites 获取所有收藏图片路径
func (udb *UserDataDB) GetAllFavorites() ([]string, error) {
	udb.mu.RLock()
	defer udb.mu.RUnlock()

	rows, err := udb.db.Query("SELECT image_path FROM favorites ORDER BY added_at ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// IsFavorite 检查图片是否收藏
func (udb *UserDataDB) IsFavorite(imagePath string) (bool, error) {
	udb.mu.RLock()
	defer udb.mu.RUnlock()

	var count int
	err := udb.db.QueryRow("SELECT COUNT(*) FROM favorites WHERE image_path = ?", imagePath).Scan(&count)
	return count > 0, err
}

// ImportFavorites 批量导入收藏
func (udb *UserDataDB) ImportFavorites(imagePaths []string, addedAt string) error {
	udb.mu.Lock()
	defer udb.mu.Unlock()

	tx, err := udb.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("INSERT OR IGNORE INTO favorites (image_path, added_at) VALUES (?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, p := range imagePaths {
		if _, err := stmt.Exec(p, addedAt); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// ==================== Sidebar Settings (expanded/folder_order) ====================

// GetSidebarSetting 读取侧边栏设置
func (udb *UserDataDB) GetSidebarSetting(key string) (string, error) {
	udb.mu.RLock()
	defer udb.mu.RUnlock()

	var value string
	err := udb.db.QueryRow("SELECT value FROM sidebar_settings WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// SetSidebarSetting 写入侧边栏设置
func (udb *UserDataDB) SetSidebarSetting(key, value string) error {
	udb.mu.Lock()
	defer udb.mu.Unlock()

	_, err := udb.db.Exec("INSERT OR REPLACE INTO sidebar_settings (key, value) VALUES (?, ?)", key, value)
	return err
}

// GetAllSidebarSettings 读取所有侧边栏设置
func (udb *UserDataDB) GetAllSidebarSettings() (map[string]string, error) {
	udb.mu.RLock()
	defer udb.mu.RUnlock()

	rows, err := udb.db.Query("SELECT key, value FROM sidebar_settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		result[k] = v
	}
	return result, rows.Err()
}
