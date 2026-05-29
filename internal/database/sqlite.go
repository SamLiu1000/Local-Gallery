package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

import _ "modernc.org/sqlite"

// ImageRecord 数据库中的图片记录
type ImageRecord struct {
	ID             string `json:"id"`
	Path           string `json:"path"`
	Name           string `json:"name"`
	Size           int64  `json:"size"`
	LastModified   int64  `json:"lastModified"`
	CreatedAt      int64  `json:"createdAt"`
	Folder         string `json:"folder"`
	RootPath       string `json:"rootPath"`
	Prompt         string `json:"prompt"`
	NegativePrompt string `json:"negativePrompt"`
	ParamsJSON     string `json:"paramsJson"`
	RawJSON        string `json:"rawJson"`
}

// ImageDB SQLite 图片元数据数据库
type ImageDB struct {
	db     *sql.DB
	dbPath string
	mu     sync.RWMutex
}

// New 打开或创建数据库
func New(dbPath string) (*ImageDB, error) {
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

	idb := &ImageDB{db: db, dbPath: dbPath}
	if err := idb.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化数据库 schema 失败: %w", err)
	}

	return idb, nil
}

// GetDB 暴露底层 *sql.DB 给高性能批量写入使用
func (idb *ImageDB) GetDB() *sql.DB {
	return idb.db
}

func (idb *ImageDB) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS images (
		id TEXT PRIMARY KEY,
		path TEXT NOT NULL,
		name TEXT NOT NULL,
		size INTEGER NOT NULL DEFAULT 0,
		last_modified INTEGER NOT NULL DEFAULT 0,
		folder TEXT NOT NULL DEFAULT '',
		root_path TEXT NOT NULL DEFAULT '',
		prompt TEXT NOT NULL DEFAULT '',
		negative_prompt TEXT NOT NULL DEFAULT '',
		params_json TEXT NOT NULL DEFAULT '{}',
		raw_json TEXT NOT NULL DEFAULT '{}',
		indexed_at INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL DEFAULT 0
	);

	CREATE INDEX IF NOT EXISTS idx_images_root_path ON images(root_path);
	CREATE INDEX IF NOT EXISTS idx_images_folder ON images(folder);

	CREATE TABLE IF NOT EXISTS image_cache (
		id TEXT PRIMARY KEY,
		path TEXT NOT NULL,
		name TEXT NOT NULL,
		size INTEGER NOT NULL DEFAULT 0,
		last_modified INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL DEFAULT 0,
		folder TEXT NOT NULL DEFAULT '',
		root_path TEXT NOT NULL DEFAULT '',
		width INTEGER NOT NULL DEFAULT 0,
		height INTEGER NOT NULL DEFAULT 0,
		is_video INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_image_cache_root_path ON image_cache(root_path);
	CREATE INDEX IF NOT EXISTS idx_image_cache_folder ON image_cache(folder);

	CREATE TABLE IF NOT EXISTS prompt_versions (
		id TEXT PRIMARY KEY,
		image_path TEXT NOT NULL,
		positive_prompt TEXT NOT NULL DEFAULT '',
		negative_prompt TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL DEFAULT 'custom',
		created_at TEXT NOT NULL DEFAULT ''
	);

	CREATE INDEX IF NOT EXISTS idx_prompt_versions_image_path ON prompt_versions(image_path);
	`
	_, err := idb.db.Exec(schema)
	if err != nil {
		return err
	}
	// 迁移：添加 created_at 列（列已存在时忽略错误）
	if _, err := idb.db.Exec(`ALTER TABLE images ADD COLUMN created_at INTEGER NOT NULL DEFAULT 0`); err != nil {
		fmt.Printf("[数据库迁移] created_at 列添加失败（可能已存在）: %v\n", err)
	}
	return nil
}

func (idb *ImageDB) IndexImage(record *ImageRecord) error {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	now := time.Now().UnixMilli()
	tx, err := idb.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
		INSERT INTO images(id, path, name, size, last_modified, created_at, folder, root_path,
		                  prompt, negative_prompt, params_json, raw_json, indexed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			path=excluded.path, name=excluded.name, size=excluded.size,
			last_modified=excluded.last_modified, created_at=excluded.created_at, folder=excluded.folder,
			root_path=excluded.root_path, prompt=excluded.prompt,
			negative_prompt=excluded.negative_prompt, params_json=excluded.params_json,
			raw_json=excluded.raw_json, indexed_at=excluded.indexed_at
	`, record.ID, record.Path, record.Name, record.Size, record.LastModified, record.CreatedAt,
		record.Folder, record.RootPath, record.Prompt, record.NegativePrompt,
		record.ParamsJSON, record.RawJSON, now)
	if err != nil {
		return fmt.Errorf("插入图片记录失败 [%s]: %w", record.ID, err)
	}
	return tx.Commit()
}

func (idb *ImageDB) IndexBatch(records []*ImageRecord) (int, error) {
	if len(records) == 0 {
		return 0, nil
	}
	idb.mu.Lock()
	defer idb.mu.Unlock()
	now := time.Now().UnixMilli()
	tx, err := idb.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`
		INSERT INTO images(id, path, name, size, last_modified, created_at, folder, root_path,
		                  prompt, negative_prompt, params_json, raw_json, indexed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			path=excluded.path, name=excluded.name, size=excluded.size,
			last_modified=excluded.last_modified, created_at=excluded.created_at, folder=excluded.folder,
			root_path=excluded.root_path, prompt=excluded.prompt,
			negative_prompt=excluded.negative_prompt, params_json=excluded.params_json,
			raw_json=excluded.raw_json, indexed_at=excluded.indexed_at
	`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	count := 0
	for _, rec := range records {
		if rec == nil {
			continue
		}
		_, err = stmt.Exec(rec.ID, rec.Path, rec.Name, rec.Size, rec.LastModified,
			rec.CreatedAt, rec.Folder, rec.RootPath, rec.Prompt, rec.NegativePrompt,
			rec.ParamsJSON, rec.RawJSON, now)
		if err != nil {
			return count, fmt.Errorf("批量插入失败 [%s]: %w", rec.ID, err)
		}
		count++
	}
	if err := tx.Commit(); err != nil {
		return count, fmt.Errorf("提交事务失败: %w", err)
	}
	return count, nil
}

// ==================== 单条查询 ====================

func (idb *ImageDB) GetImagePath(id string) string {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	var path string
	idb.db.QueryRow("SELECT path FROM images WHERE id = ?", id).Scan(&path)
	return path
}

func (idb *ImageDB) GetImageRecord(id string) *ImageRecord {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	var rec ImageRecord
	err := idb.db.QueryRow(`
		SELECT id, path, name, size, last_modified, created_at, folder, root_path,
		       prompt, negative_prompt, params_json, raw_json
		FROM images WHERE id = ?`, id).Scan(
		&rec.ID, &rec.Path, &rec.Name, &rec.Size, &rec.LastModified, &rec.CreatedAt,
		&rec.Folder, &rec.RootPath, &rec.Prompt, &rec.NegativePrompt,
		&rec.ParamsJSON, &rec.RawJSON,
	)
	if err != nil {
		return nil
	}
	return &rec
}

// ==================== 删除操作 ====================

func (idb *ImageDB) DeleteByRoot(rootPath string) (int, error) {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	tx, err := idb.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.Exec("DELETE FROM images WHERE root_path = ?", rootPath)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	affected, _ := result.RowsAffected()
	return int(affected), nil
}

func (idb *ImageDB) DeleteImage(id string) error {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	tx, err := idb.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	tx.Exec("DELETE FROM images WHERE id = ?", id)
	return tx.Commit()
}

func (idb *ImageDB) DeleteImagesBatch(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	idb.mu.Lock()
	defer idb.mu.Unlock()
	tx, err := idb.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare("DELETE FROM images WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, id := range ids {
		stmt.Exec(id)
	}
	return tx.Commit()
}

// SearchResult 搜索结果
type SearchResult struct {
	ID             string `json:"id"`
	Path           string `json:"path"`
	Name           string `json:"name"`
	Size           int64  `json:"size"`
	LastModified   int64  `json:"lastModified"`
	CreatedAt      int64  `json:"createdAt"`
	Folder         string `json:"folder"`
	RootPath       string `json:"rootPath"`
	Prompt         string `json:"prompt"`
	NegativePrompt string `json:"negativePrompt"`
	ParamsJSON     string `json:"paramsJson"`
}

// SearchImagesBySubstring 在 path、prompt、negative_prompt、params_json 中模糊搜索
// 返回匹配的图片记录列表，按 last_modified DESC 排序
func (idb *ImageDB) SearchImagesBySubstring(query string, folder string, offset int, limit int) ([]SearchResult, int, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()

	// 构建 WHERE 条件
	searchPattern := "%" + query + "%"
	where := "WHERE (path LIKE ? OR prompt LIKE ? OR negative_prompt LIKE ? OR params_json LIKE ?)"
	args := []interface{}{searchPattern, searchPattern, searchPattern, searchPattern}

	if folder != "" {
		normalizedFolder := strings.ReplaceAll(folder, "\\", "/")
		where += " AND (root_path = ? OR folder LIKE ?)"
		args = append(args, normalizedFolder, normalizedFolder+"/%")
	}

	// 先查总数
	var total int
	countSQL := "SELECT COUNT(*) FROM images " + where
	if err := idb.db.QueryRow(countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("搜索计数失败: %w", err)
	}

	// 查询结果
	querySQL := "SELECT id, path, name, size, last_modified, created_at, folder, root_path, prompt, negative_prompt, params_json FROM images " + where + " ORDER BY last_modified DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	rows, err := idb.db.Query(querySQL, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("搜索查询失败: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.ID, &r.Path, &r.Name, &r.Size, &r.LastModified, &r.CreatedAt, &r.Folder, &r.RootPath, &r.Prompt, &r.NegativePrompt, &r.ParamsJSON); err != nil {
			return nil, 0, fmt.Errorf("扫描搜索结果失败: %w", err)
		}
		results = append(results, r)
	}

	return results, total, nil
}

// SearchCondition 高级搜索条件
type SearchCondition struct {
	Field string // "all", "path", "prompt", "negative_prompt", "params_json"
	Value string
	Mode  string // "contains", "exact", "exclude", "word"
}

// SearchImagesAdvanced 高级搜索：支持多条件、多文件夹、日期范围、多种匹配模式
func (idb *ImageDB) SearchImagesAdvanced(conditions []SearchCondition, folders []string, dateFrom, dateTo int64, matchMode string, offset, limit int) ([]SearchResult, int, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()

	var whereParts []string
	var args []interface{}

	// 构建条件子句
	if len(conditions) > 0 {
		var condParts []string
		for _, c := range conditions {
			fields := getFields(c.Field)
			part, condArgs := buildConditionSQL(fields, c.Value, c.Mode)
			if part != "" {
				condParts = append(condParts, "("+part+")")
				args = append(args, condArgs...)
			}
		}
		if len(condParts) > 0 {
			joiner := " OR "
			if matchMode == "and" {
				joiner = " AND "
			}
			whereParts = append(whereParts, "("+strings.Join(condParts, joiner)+")")
		}
	}

	// 文件夹过滤
	if len(folders) > 0 {
		var folderParts []string
		for _, f := range folders {
			// root_path 存储系统原生路径（Windows 上是反斜杠），folder 已规范化为正斜杠
			normalized := strings.ReplaceAll(f, "\\", "/")
			backslash := strings.ReplaceAll(f, "/", "\\")
			folderParts = append(folderParts, "(root_path = ? OR root_path = ?)")
			args = append(args, normalized, backslash)
			folderParts = append(folderParts, "folder LIKE ?")
			args = append(args, normalized+"/%")
		}
		whereParts = append(whereParts, "("+strings.Join(folderParts, " OR ")+")")
	}

	// 日期范围
	if dateFrom > 0 {
		whereParts = append(whereParts, "last_modified >= ?")
		args = append(args, dateFrom)
	}
	if dateTo > 0 {
		whereParts = append(whereParts, "last_modified <= ?")
		args = append(args, dateTo)
	}

	where := ""
	if len(whereParts) > 0 {
		where = "WHERE " + strings.Join(whereParts, " AND ")
	}
	fmt.Printf("[DB 高级搜索] where=%s, args=%v\n", where, args)

	// 先查总数
	var total int
	countSQL := "SELECT COUNT(*) FROM images " + where
	if err := idb.db.QueryRow(countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("高级搜索计数失败: %w", err)
	}

	// 查询结果
	querySQL := "SELECT id, path, name, size, last_modified, created_at, folder, root_path, prompt, negative_prompt, params_json FROM images " + where + " ORDER BY last_modified DESC LIMIT ? OFFSET ?"
	queryArgs := append(args, limit, offset)
	rows, err := idb.db.Query(querySQL, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("高级搜索查询失败: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.ID, &r.Path, &r.Name, &r.Size, &r.LastModified, &r.CreatedAt, &r.Folder, &r.RootPath, &r.Prompt, &r.NegativePrompt, &r.ParamsJSON); err != nil {
			return nil, 0, fmt.Errorf("扫描高级搜索结果失败: %w", err)
		}
		results = append(results, r)
	}

	return results, total, nil
}

// getFields 返回条件对应的数据库字段列表
func getFields(field string) []string {
	switch field {
	case "path":
		return []string{"path"}
	case "prompt":
		return []string{"prompt"}
	case "negative_prompt":
		return []string{"negative_prompt"}
	case "params_json":
		return []string{"params_json"}
	case "folder_name":
		return []string{"folder", "root_path"}
	default:
		return []string{"path", "prompt", "negative_prompt", "params_json"}
	}
}

// buildConditionSQL 根据模式和字段构建 SQL 片段及参数
func buildConditionSQL(fields []string, value, mode string) (string, []interface{}) {
	switch mode {
	case "exact":
		var parts []string
		var args []interface{}
		for _, f := range fields {
			parts = append(parts, f+" = ?")
			args = append(args, value)
		}
		return strings.Join(parts, " OR "), args

	case "exclude":
		var parts []string
		var args []interface{}
		pattern := "%" + value + "%"
		for _, f := range fields {
			parts = append(parts, f+" NOT LIKE ?")
			args = append(args, pattern)
		}
		return strings.Join(parts, " AND "), args

	case "word":
		var parts []string
		var args []interface{}
		for _, f := range fields {
			parts = append(parts, "("+f+" LIKE ? OR "+f+" LIKE ? OR "+f+" LIKE ? OR "+f+" = ?)")
			args = append(args, value+" %", "% "+value+" %", "% "+value, value)
		}
		return strings.Join(parts, " OR "), args

	default: // "contains"
		var parts []string
		var args []interface{}
		pattern := "%" + value + "%"
		for _, f := range fields {
			parts = append(parts, f+" LIKE ?")
			args = append(args, pattern)
		}
		return strings.Join(parts, " OR "), args
	}
}

// ==================== 统计与维护 ====================

// GetExistingIDs 返回数据库中已有的所有图片 ID（用于增量索引修复）
func (idb *ImageDB) GetExistingIDs() (map[string]bool, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	rows, err := idb.db.Query("SELECT id FROM images")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, nil
}

func (idb *ImageDB) GetStats() map[string]interface{} {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	var totalImages int
	idb.db.QueryRow("SELECT COUNT(*) FROM images").Scan(&totalImages)
	var dbSize int64
	if info, err := os.Stat(idb.dbPath); err == nil {
		dbSize = info.Size()
	}
	rows, err := idb.db.Query("SELECT root_path, COUNT(*) FROM images GROUP BY root_path")
	rootCounts := make(map[string]int)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var path string
			var count int
			rows.Scan(&path, &count)
			rootCounts[path] = count
		}
	}
	return map[string]interface{}{
		"totalImages": totalImages,
		"dbSize":      dbSize,
		"dbSizeStr":   formatFileSize(dbSize),
		"roots":       rootCounts,
	}
}

// CountByRoot 返回指定根目录在搜索索引中的图片数
func (idb *ImageDB) CountByRoot(rootPath string) int {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	var count int
	idb.db.QueryRow("SELECT COUNT(*) FROM images WHERE root_path=?", rootPath).Scan(&count)
	return count
}

// ==================== Prompt 版本管理 ====================

// PromptVersion 提示词版本
type PromptVersion struct {
	ID             string `json:"id"`
	ImagePath      string `json:"imagePath"`
	PositivePrompt string `json:"positivePrompt"`
	NegativePrompt string `json:"negativePrompt"`
	Source         string `json:"source"`
	CreatedAt      string `json:"createdAt"`
}

// AddPromptVersion 添加提示词版本
func (idb *ImageDB) AddPromptVersion(v *PromptVersion) error {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	_, err := idb.db.Exec(`
		INSERT INTO prompt_versions(id, image_path, positive_prompt, negative_prompt, source, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, v.ID, v.ImagePath, v.PositivePrompt, v.NegativePrompt, v.Source, v.CreatedAt)
	return err
}

// GetPromptVersions 获取某张图片的所有提示词版本（按创建时间排序）
func (idb *ImageDB) GetPromptVersions(imagePath string) ([]PromptVersion, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	rows, err := idb.db.Query(`
		SELECT id, image_path, positive_prompt, negative_prompt, source, created_at
		FROM prompt_versions WHERE image_path = ? ORDER BY created_at ASC
	`, imagePath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PromptVersion
	for rows.Next() {
		var v PromptVersion
		if err := rows.Scan(&v.ID, &v.ImagePath, &v.PositivePrompt, &v.NegativePrompt, &v.Source, &v.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, nil
}

// DeletePromptVersion 删除提示词版本
func (idb *ImageDB) DeletePromptVersion(id string) error {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	_, err := idb.db.Exec("DELETE FROM prompt_versions WHERE id = ?", id)
	return err
}

// GetAllPromptVersions 获取所有提示词版本（导出用）
func (idb *ImageDB) GetAllPromptVersions() ([]PromptVersion, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	rows, err := idb.db.Query("SELECT id, image_path, positive_prompt, negative_prompt, source, created_at FROM prompt_versions ORDER BY created_at ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PromptVersion
	for rows.Next() {
		var v PromptVersion
		if err := rows.Scan(&v.ID, &v.ImagePath, &v.PositivePrompt, &v.NegativePrompt, &v.Source, &v.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, nil
}

// GetAllPromptVersionCounts 返回 imagePath → count 映射
func (idb *ImageDB) GetAllPromptVersionCounts() (map[string]int, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	rows, err := idb.db.Query("SELECT image_path, COUNT(*) FROM prompt_versions GROUP BY image_path")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var path string
		var count int
		if err := rows.Scan(&path, &count); err != nil {
			return nil, err
		}
		counts[path] = count
	}
	return counts, nil
}

// BatchAddPromptVersions 批量导入提示词版本
func (idb *ImageDB) BatchAddPromptVersions(versions []PromptVersion) error {
	if len(versions) == 0 {
		return nil
	}
	idb.mu.Lock()
	defer idb.mu.Unlock()
	tx, err := idb.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO prompt_versions(id, image_path, positive_prompt, negative_prompt, source, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, v := range versions {
		_, err := stmt.Exec(v.ID, v.ImagePath, v.PositivePrompt, v.NegativePrompt, v.Source, v.CreatedAt)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (idb *ImageDB) Close() error {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	return idb.db.Close()
}

func formatFileSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
}

// ImageCacheEntry 图片缓存记录（对应 ImageEntry）
type ImageCacheEntry struct {
	ID           string
	Path         string
	Name         string
	Size         int64
	LastModified int64
	CreatedAt    int64
	Folder       string
	RootPath     string
	Width        int
	Height       int
	IsVideo      bool
}

// SaveImageCacheBatch 批量写入图片缓存（INSERT OR REPLACE）
func (idb *ImageDB) SaveImageCacheBatch(entries []ImageCacheEntry) error {
	if len(entries) == 0 {
		return nil
	}
	idb.mu.Lock()
	defer idb.mu.Unlock()
	tx, err := idb.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO image_cache
		(id, path, name, size, last_modified, created_at, folder, root_path, width, height, is_video)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range entries {
		isVideo := 0
		if e.IsVideo {
			isVideo = 1
		}
		if _, err := stmt.Exec(e.ID, e.Path, e.Name, e.Size, e.LastModified,
			e.CreatedAt, e.Folder, e.RootPath, e.Width, e.Height, isVideo); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteImageCacheByRoot 删除指定根目录下的所有 image_cache 记录
func (idb *ImageDB) DeleteImageCacheByRoot(rootPath string) (int, error) {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	result, err := idb.db.Exec("DELETE FROM image_cache WHERE root_path = ?", rootPath)
	if err != nil {
		return 0, err
	}
	affected, _ := result.RowsAffected()
	return int(affected), nil
}

// SaveImageCacheByRoot
func (idb *ImageDB) SaveImageCacheByRoot(rootPath string, entries []ImageCacheEntry) error {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	tx, err := idb.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM image_cache WHERE root_path = ?", rootPath); err != nil {
		return err
	}
	if len(entries) == 0 {
		return tx.Commit()
	}
	stmt, err := tx.Prepare("INSERT OR REPLACE INTO image_cache (id, path, name, size, last_modified, created_at, folder, root_path, width, height, is_video) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range entries {
		isVideo := 0
		if e.IsVideo {
			isVideo = 1
		}
		if _, err := stmt.Exec(e.ID, e.Path, e.Name, e.Size, e.LastModified,
			e.CreatedAt, e.Folder, e.RootPath, e.Width, e.Height, isVideo); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LoadAllImageCache 加载全部图片缓存
func (idb *ImageDB) LoadAllImageCache() ([]ImageCacheEntry, error) {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	rows, err := idb.db.Query(`SELECT id, path, name, size, last_modified, created_at,
		folder, root_path, width, height, is_video FROM image_cache`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []ImageCacheEntry
	for rows.Next() {
		var e ImageCacheEntry
		var isVideo int
		if err := rows.Scan(&e.ID, &e.Path, &e.Name, &e.Size, &e.LastModified,
			&e.CreatedAt, &e.Folder, &e.RootPath, &e.Width, &e.Height, &isVideo); err != nil {
			return entries, err
		}
		e.IsVideo = isVideo != 0
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// UpdateImageDimensions 更新单张图片的宽高（补全旧数据时使用）
func (idb *ImageDB) UpdateImageDimensions(id string, width, height int) error {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	_, err := idb.db.Exec(`UPDATE image_cache SET width=?, height=? WHERE id=?`, width, height, id)
	return err
}

// CountImageCache 返回缓存中的图片数量
func (idb *ImageDB) CountImageCache() (int, error) {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	var count int
	err := idb.db.QueryRow(`SELECT COUNT(*) FROM image_cache`).Scan(&count)
	return count, err
}
