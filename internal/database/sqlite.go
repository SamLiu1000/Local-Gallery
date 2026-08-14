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
	-- ★ 复合索引：启动光索引（GROUP BY root_path,folder）与按文件夹分页查询共用，
	--   在百万级行数下让 GROUP BY 走覆盖索引（实测 1.2s → 0.08s，约 15 倍）
	CREATE INDEX IF NOT EXISTS idx_image_cache_root_folder ON image_cache(root_path, folder);
	-- ★ 排序索引：按文件夹分页时 ORDER BY last_modified 不再全量排序。
	--   实测 85 万张的根目录 OFFSET 10 万时 2.3s → 0.1s
	CREATE INDEX IF NOT EXISTS idx_image_cache_root_modified ON image_cache(root_path, last_modified);
	-- ★ 路径索引：标签/收藏视图用 WHERE path IN (...) 精准查图。
	--   无索引时每个批次全表扫描 1.4M 行（实测 500 条路径 426ms），
	--   有索引后 1ms。一次性构建约 2-3s。
	CREATE INDEX IF NOT EXISTS idx_image_cache_path ON image_cache(path);

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
	// 迁移：添加 content_hash 列（用于扫描去重）
	if _, err := idb.db.Exec(`ALTER TABLE image_cache ADD COLUMN content_hash TEXT NOT NULL DEFAULT ''`); err != nil {
		fmt.Printf("[数据库迁移] content_hash 列添加失败（可能已存在）: %v\n", err)
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

// escapeLike 转义 LIKE 通配符（%、_）和转义符本身，防止用户输入被当作通配符
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// SearchImagesBySubstring 在 path、prompt、negative_prompt、params_json 中模糊搜索
// 返回匹配的图片记录列表，按相关度打分 DESC、last_modified DESC 排序
func (idb *ImageDB) SearchImagesBySubstring(query string, folder string, offset int, limit int) ([]SearchResult, int, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()

	// 转义通配符后再拼模糊模式
	searchPattern := "%" + escapeLike(query) + "%"

	// 相关度打分：命中字段越重要分越高
	scoreExpr := `(CASE WHEN name LIKE ? ESCAPE '\' THEN 5 ELSE 0 END
		+ CASE WHEN prompt LIKE ? ESCAPE '\' THEN 4 ELSE 0 END
		+ CASE WHEN negative_prompt LIKE ? ESCAPE '\' THEN 2 ELSE 0 END
		+ CASE WHEN path LIKE ? ESCAPE '\' THEN 2 ELSE 0 END
		+ CASE WHEN params_json LIKE ? ESCAPE '\' THEN 1 ELSE 0 END)`
	scoreArgs := []interface{}{searchPattern, searchPattern, searchPattern, searchPattern, searchPattern}

	// 构建 WHERE 条件（占位符顺序在 SELECT 之后）
	where := "WHERE (path LIKE ? ESCAPE '\\' OR prompt LIKE ? ESCAPE '\\' OR negative_prompt LIKE ? ESCAPE '\\' OR params_json LIKE ? ESCAPE '\\')"
	whereArgs := []interface{}{searchPattern, searchPattern, searchPattern, searchPattern}

	if folder != "" {
		normalizedFolder := strings.ReplaceAll(folder, "\\", "/")
		where += " AND (root_path = ? OR folder LIKE ? ESCAPE '\\')"
		whereArgs = append(whereArgs, normalizedFolder, normalizedFolder+"/%")
	}

	// 先查总数
	var total int
	countSQL := "SELECT COUNT(*) FROM images " + where
	if err := idb.db.QueryRow(countSQL, whereArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("搜索计数失败: %w", err)
	}

	// 查询结果：相关度优先，其次按修改时间
	querySQL := "SELECT id, path, name, size, last_modified, created_at, folder, root_path, prompt, negative_prompt, params_json, " + scoreExpr + " AS score FROM images " + where + " ORDER BY score DESC, last_modified DESC LIMIT ? OFFSET ?"
	queryArgs := append(scoreArgs, whereArgs...)
	queryArgs = append(queryArgs, limit, offset)
	rows, err := idb.db.Query(querySQL, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("搜索查询失败: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var score int // score 列仅用于排序，不输出
		if err := rows.Scan(&r.ID, &r.Path, &r.Name, &r.Size, &r.LastModified, &r.CreatedAt, &r.Folder, &r.RootPath, &r.Prompt, &r.NegativePrompt, &r.ParamsJSON, &score); err != nil {
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
// 结果按相关度打分 DESC、last_modified DESC 排序（命中字段/条件越多分越高）
func (idb *ImageDB) SearchImagesAdvanced(conditions []SearchCondition, folders []string, dateFrom, dateTo int64, matchMode string, offset, limit int) ([]SearchResult, int, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()

	var whereParts []string
	var whereArgs []interface{}
	// 相关度打分：非 exclude 条件按命中字段数累加（SELECT 里的占位符在 WHERE 之前）
	var scoreParts []string
	var scoreArgs []interface{}

	// 构建条件子句
	if len(conditions) > 0 {
		var condParts []string
		for _, c := range conditions {
			fields := getFields(c.Field)
			part, condArgs := buildConditionSQL(fields, c.Value, c.Mode)
			if part != "" {
				condParts = append(condParts, "("+part+")")
				whereArgs = append(whereArgs, condArgs...)

				// exclude 只做过滤，不参与打分
				if c.Mode != "exclude" {
					scorePart, scoreArg := buildConditionScore(fields, c.Value, c.Mode)
					scoreParts = append(scoreParts, "("+scorePart+")")
					scoreArgs = append(scoreArgs, scoreArg...)
				}
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
			whereArgs = append(whereArgs, normalized, backslash)
			folderParts = append(folderParts, "folder LIKE ? ESCAPE '\\'")
			whereArgs = append(whereArgs, normalized+"/%")
		}
		whereParts = append(whereParts, "("+strings.Join(folderParts, " OR ")+")")
	}

	// 日期范围
	if dateFrom > 0 {
		whereParts = append(whereParts, "last_modified >= ?")
		whereArgs = append(whereArgs, dateFrom)
	}
	if dateTo > 0 {
		whereParts = append(whereParts, "last_modified <= ?")
		whereArgs = append(whereArgs, dateTo)
	}

	where := ""
	if len(whereParts) > 0 {
		where = "WHERE " + strings.Join(whereParts, " AND ")
	}

	// 打分表达式：无条件时恒为 1，回退按修改时间排序
	scoreExpr := "1"
	if len(scoreParts) > 0 {
		scoreExpr = "(" + strings.Join(scoreParts, " + ") + ")"
	}

	// 先查总数
	var total int
	countSQL := "SELECT COUNT(*) FROM images " + where
	if err := idb.db.QueryRow(countSQL, whereArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("高级搜索计数失败: %w", err)
	}

	// 查询结果（占位符顺序：SELECT 打分 → WHERE → LIMIT/OFFSET）
	querySQL := "SELECT id, path, name, size, last_modified, created_at, folder, root_path, prompt, negative_prompt, params_json, " + scoreExpr + " AS score FROM images " + where + " ORDER BY score DESC, last_modified DESC LIMIT ? OFFSET ?"
	queryArgs := append(scoreArgs, whereArgs...)
	queryArgs = append(queryArgs, limit, offset)
	rows, err := idb.db.Query(querySQL, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("高级搜索查询失败: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var score int // score 列仅用于排序，不输出
		if err := rows.Scan(&r.ID, &r.Path, &r.Name, &r.Size, &r.LastModified, &r.CreatedAt, &r.Folder, &r.RootPath, &r.Prompt, &r.NegativePrompt, &r.ParamsJSON, &score); err != nil {
			return nil, 0, fmt.Errorf("扫描高级搜索结果失败: %w", err)
		}
		results = append(results, r)
	}

	return results, total, nil
}

// getFields 返回条件对应的数据库字段列表
// 普通字段直接返回列名；结构化参数以 "json:Key" 标记，由 fieldExpr 展开成 json_extract。
// ★ 支持 "json:<任意键>" 直传：生成参数标签面板点击标签时按 params_json 里的原始键搜索。
func getFields(field string) []string {
	if strings.HasPrefix(field, "json:") {
		return []string{field}
	}
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
	case "param_sampler":
		return []string{"json:Sampler"}
	case "param_model":
		return []string{"json:Model"}
	case "param_steps":
		return []string{"json:Steps"}
	case "param_seed":
		return []string{"json:Seed"}
	case "param_cfg":
		return []string{"json:CFG Scale"}
	case "param_size":
		return []string{"json:Size"}
	case "param_vae":
		return []string{"json:VAE"}
	case "param_lora":
		return []string{"json:LoRA"}
	default:
		return []string{"path", "prompt", "negative_prompt", "params_json"}
	}
}

// fieldExpr 返回字段对应的 SQL 表达式。
// json: 前缀的字段从 params_json 提取对应参数（键含空格时 JSON path 用双引号包裹），
// 参数缺失时 COALESCE 为空字符串：contains 不命中、exclude 放行。
// ★ 先 json_valid 防护：部分历史数据的 params_json 不是合法 JSON（空串/截断），
//   直接 json_extract 会抛 "malformed JSON" 导致整条查询失败。
func fieldExpr(f string) string {
	if strings.HasPrefix(f, "json:") {
		key := strings.TrimPrefix(f, "json:")
		return `COALESCE(CASE WHEN json_valid(params_json) THEN json_extract(params_json, '$."` + key + `"') ELSE NULL END, '')`
	}
	return f
}

// wordSeparators 全词匹配时视为词分隔符的字符（提示词里常用逗号/括号/标点分隔）
var wordSeparators = []string{",", "，", ".", "。", "、", ";", "；", ":", "：", "!", "！", "?", "？", "(", ")", "[", "]", "{", "}", "/", "\\", "|", "\n", "\r", "\t", "_", "-", "=", "+"}

// wordNormExpr 把文本字段表达式中常见的词分隔符归一化为空格（SQL 侧）
func wordNormExpr(expr string) string {
	norm := expr
	for _, sep := range wordSeparators {
		norm = "REPLACE(" + norm + ", '" + sep + "', ' ')"
	}
	return "TRIM(" + norm + ")"
}

// normalizeWordValue 把用户输入中的分隔符归一化为空格并压缩空白（Go 侧）
func normalizeWordValue(s string) string {
	for _, sep := range wordSeparators {
		s = strings.ReplaceAll(s, sep, " ")
	}
	return strings.Join(strings.Fields(s), " ")
}

// wordPattern 全词匹配模式：把值归一化后包上词边界空格
func wordPattern(value string) string {
	return "% " + escapeLike(normalizeWordValue(value)) + " %"
}

// buildConditionSQL 根据模式和字段构建 SQL 片段及参数
func buildConditionSQL(fields []string, value, mode string) (string, []interface{}) {
	switch mode {
	case "exact":
		var parts []string
		var args []interface{}
		for _, f := range fields {
			parts = append(parts, fieldExpr(f)+" = ?")
			args = append(args, value)
		}
		return strings.Join(parts, " OR "), args

	case "exclude":
		var parts []string
		var args []interface{}
		pattern := "%" + escapeLike(value) + "%"
		for _, f := range fields {
			parts = append(parts, fieldExpr(f)+" NOT LIKE ? ESCAPE '\\'")
			args = append(args, pattern)
		}
		return strings.Join(parts, " AND "), args

	case "word":
		// 全词匹配：把文本里的分隔符归一化为空格后按空格分隔的短语匹配
		var parts []string
		var args []interface{}
		pattern := wordPattern(value)
		for _, f := range fields {
			norm := wordNormExpr(fieldExpr(f))
			parts = append(parts, "((' ' || "+norm+" || ' ') LIKE ? ESCAPE '\\')")
			args = append(args, pattern)
		}
		return strings.Join(parts, " OR "), args

	default: // "contains"
		var parts []string
		var args []interface{}
		pattern := "%" + escapeLike(value) + "%"
		for _, f := range fields {
			parts = append(parts, fieldExpr(f)+" LIKE ? ESCAPE '\\'")
			args = append(args, pattern)
		}
		return strings.Join(parts, " OR "), args
	}
}

// scoreWeight 各匹配模式在相关度打分中的单字段权重
func scoreWeight(mode string) string {
	switch mode {
	case "exact":
		return "10"
	case "word":
		return "6"
	default: // contains
		return "3"
	}
}

// buildConditionScore 为单个条件生成相关度打分表达式：命中的字段越多分越高
func buildConditionScore(fields []string, value, mode string) (string, []interface{}) {
	var parts []string
	var args []interface{}
	w := scoreWeight(mode)
	for _, f := range fields {
		expr := fieldExpr(f)
		var cond string
		switch mode {
		case "exact":
			cond = expr + " = ?"
			args = append(args, value)
		case "word":
			norm := wordNormExpr(expr)
			cond = "((' ' || " + norm + " || ' ') LIKE ? ESCAPE '\\')"
			args = append(args, wordPattern(value))
		default: // contains
			cond = expr + " LIKE ? ESCAPE '\\'"
			args = append(args, "%"+escapeLike(value)+"%")
		}
		parts = append(parts, "(CASE WHEN "+cond+" THEN "+w+" ELSE 0 END)")
	}
	return strings.Join(parts, " + "), args
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
	ContentHash  string
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
		(id, path, name, size, last_modified, created_at, folder, root_path, width, height, is_video, content_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
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
			e.CreatedAt, e.Folder, e.RootPath, e.Width, e.Height, isVideo, e.ContentHash); err != nil {
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

// DeleteImageCacheBatch 批量从 image_cache 删除指定 ID 的记录
func (idb *ImageDB) DeleteImageCacheBatch(ids []string) error {
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
	stmt, err := tx.Prepare("DELETE FROM image_cache WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, id := range ids {
		if _, err := stmt.Exec(id); err != nil {
			fmt.Printf("[数据库] image_cache 删除失败 %s: %v\n", id, err)
		}
	}
	return tx.Commit()
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
	stmt, err := tx.Prepare("INSERT OR REPLACE INTO image_cache (id, path, name, size, last_modified, created_at, folder, root_path, width, height, is_video, content_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
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
			e.CreatedAt, e.Folder, e.RootPath, e.Width, e.Height, isVideo, e.ContentHash); err != nil {
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

// LoadFolderIndexLight 仅加载文件夹列表与计数（不读图片详情）。
// 返回的 entry 仅填充 RootPath/Folder；Size 字段复用为 COUNT(*)。
func (idb *ImageDB) LoadFolderIndexLight() ([]ImageCacheEntry, error) {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	rows, err := idb.db.Query(`SELECT root_path, folder, COUNT(*) FROM image_cache GROUP BY root_path, folder`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []ImageCacheEntry
	for rows.Next() {
		var e ImageCacheEntry
		if err := rows.Scan(&e.RootPath, &e.Folder, &e.Size); err != nil {
			return entries, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// LoadImageCacheByFolder 按根目录+相对文件夹精确加载。folder="" 表示根目录直接子文件。
func (idb *ImageDB) LoadImageCacheByFolder(rootPath, folder string) ([]ImageCacheEntry, error) {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	rows, err := idb.db.Query(`SELECT id, path, name, size, last_modified, created_at,
		folder, root_path, width, height, is_video FROM image_cache WHERE root_path=? AND folder=?`, rootPath, folder)
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

// CountImagesByFolder 统计某根目录+相对文件夹下的直接图片数（侧边栏预览确定性选取用）
func (idb *ImageDB) CountImagesByFolder(rootPath, folder string) (int, error) {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	var n int
	err := idb.db.QueryRow(`SELECT COUNT(*) FROM image_cache WHERE root_path=? AND folder=?`, rootPath, folder).Scan(&n)
	return n, err
}

// GetImageByFolderOffset 按 ORDER BY id 分页取某文件夹直接图片的 ID、修改时间与路径（侧边栏预览用，LIMIT 1 开销极低）
func (idb *ImageDB) GetImageByFolderOffset(rootPath, folder string, offset int) (string, int64, string, error) {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	var id string
	var lm int64
	var p string
	err := idb.db.QueryRow(`SELECT id, last_modified, path FROM image_cache WHERE root_path=? AND folder=? ORDER BY id LIMIT 1 OFFSET ?`,
		rootPath, folder, offset).Scan(&id, &lm, &p)
	return id, lm, p, err
}

// LoadImageCacheByRoot 加载整个根目录所有子文件夹的图片。
func (idb *ImageDB) LoadImageCacheByRoot(rootPath string) ([]ImageCacheEntry, error) {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	rows, err := idb.db.Query(`SELECT id, path, name, size, last_modified, created_at,
		folder, root_path, width, height, is_video FROM image_cache WHERE root_path=?`, rootPath)
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

// LoadImageCacheByPathPrefixPaged 按绝对路径前缀加载（★ 支持嵌套根与父根共存）。
// 同一批文件无论记录归属哪个根目录（嵌套根/父根），通过完整路径前缀都能查到，
// 避免"root_path + 相对 folder"匹配随机根目录导致的空结果。
// 用范围查询 (path >= ? AND path < ?) 命中 path 索引（LIKE 前缀无法可靠走索引）。
func (idb *ImageDB) LoadImageCacheByPathPrefixPaged(pathPrefix string, offset, limit int, sortOrder string) ([]ImageCacheEntry, int, error) {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	if limit <= 0 {
		limit = 500
	}
	// 上界：前缀 + U+FFFF，匹配所有以此前缀开头的路径
	upper := pathPrefix + "\uFFFF"
	where := "path >= ? AND path < ?"
	args := []interface{}{pathPrefix, upper}
	var total int
	if err := idb.db.QueryRow("SELECT COUNT(*) FROM image_cache WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	queryArgs := append([]interface{}{}, args...)
	queryArgs = append(queryArgs, limit, offset)
	query := `SELECT id, path, name, size, last_modified, created_at,
		folder, root_path, width, height, is_video FROM image_cache WHERE ` + where + ` ORDER BY ` + imageCacheOrderBy(sortOrder) + ` LIMIT ? OFFSET ?`
	rows, err := idb.db.Query(query, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var entries []ImageCacheEntry
	for rows.Next() {
		var e ImageCacheEntry
		var isVideo int
		if err := rows.Scan(&e.ID, &e.Path, &e.Name, &e.Size, &e.LastModified,
			&e.CreatedAt, &e.Folder, &e.RootPath, &e.Width, &e.Height, &isVideo); err != nil {
			return entries, total, err
		}
		e.IsVideo = isVideo != 0
		entries = append(entries, e)
	}
	return entries, total, rows.Err()
}

func imageCacheOrderBy(sortOrder string) string {
	switch sortOrder {
	case "name-asc", "name":
		return "name ASC, path ASC"
	case "name-desc":
		return "name DESC, path ASC"
	case "size-desc", "size":
		return "size DESC, path ASC"
	case "size-asc":
		return "size ASC, path ASC"
	case "date-asc":
		return "last_modified ASC, path ASC"
	case "created":
		return "created_at DESC, path ASC"
	case "modified", "date-desc", "":
		return "last_modified DESC, path ASC"
	default:
		return "last_modified DESC, path ASC"
	}
}

// LoadImageCacheByFolderTreePaged 按根目录+相对文件夹前缀分页加载文件夹树图片。
func (idb *ImageDB) LoadImageCacheByFolderTreePaged(rootPath, folder string, offset, limit int, sortOrder string) ([]ImageCacheEntry, int, error) {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	if limit <= 0 {
		limit = 500
	}
	folder = strings.ReplaceAll(strings.Trim(folder, "/"), "\\", "/")
	where := "root_path=?"
	args := []interface{}{rootPath}
	if folder != "" {
		where += " AND (folder=? OR folder LIKE ?)"
		args = append(args, folder, folder+"/%")
	}
	var total int
	if err := idb.db.QueryRow("SELECT COUNT(*) FROM image_cache WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	queryArgs := append([]interface{}{}, args...)
	queryArgs = append(queryArgs, limit, offset)
	query := `SELECT id, path, name, size, last_modified, created_at,
		folder, root_path, width, height, is_video FROM image_cache WHERE ` + where + ` ORDER BY ` + imageCacheOrderBy(sortOrder) + ` LIMIT ? OFFSET ?`
	rows, err := idb.db.Query(query, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var entries []ImageCacheEntry
	for rows.Next() {
		var e ImageCacheEntry
		var isVideo int
		if err := rows.Scan(&e.ID, &e.Path, &e.Name, &e.Size, &e.LastModified,
			&e.CreatedAt, &e.Folder, &e.RootPath, &e.Width, &e.Height, &isVideo); err != nil {
			return entries, total, err
		}
		e.IsVideo = isVideo != 0
		entries = append(entries, e)
	}
	return entries, total, rows.Err()
}

// LoadImageCachePaged 按 offset/limit 分页加载全部图片（folder="" 时用）。
func (idb *ImageDB) LoadImageCachePaged(offset, limit int, sortOrder string) ([]ImageCacheEntry, error) {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	query := `SELECT id, path, name, size, last_modified, created_at,
		folder, root_path, width, height, is_video FROM image_cache ORDER BY ` + imageCacheOrderBy(sortOrder) + ` LIMIT ? OFFSET ?`
	rows, err := idb.db.Query(query, limit, offset)
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

// GetImageEntry 单点查询某 imageID（推广 resolveImagePath 回退模式）。
func (idb *ImageDB) GetImageEntry(id string) (*ImageCacheEntry, error) {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	var e ImageCacheEntry
	var isVideo int
	err := idb.db.QueryRow(`SELECT id, path, name, size, last_modified, created_at,
		folder, root_path, width, height, is_video FROM image_cache WHERE id=? LIMIT 1`, id).
		Scan(&e.ID, &e.Path, &e.Name, &e.Size, &e.LastModified,
			&e.CreatedAt, &e.Folder, &e.RootPath, &e.Width, &e.Height, &isVideo)
	if err != nil {
		return nil, err
	}
	e.IsVideo = isVideo != 0
	return &e, nil
}

// GetImagesNeedingMetadataBackfill 返回需要回填元数据的图片（params_json 为空或非法 JSON）。
// 用于后台批量重新提取生成参数（Model/LoRA/CFG 等）写入搜索索引。
func (idb *ImageDB) GetImagesNeedingMetadataBackfill(limit int) ([]ImageCacheEntry, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	rows, err := idb.db.Query(`SELECT id, path, name, size, last_modified, created_at,
		folder, root_path FROM images WHERE params_json = '' OR json_valid(params_json) = 0 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []ImageCacheEntry
	for rows.Next() {
		var e ImageCacheEntry
		if err := rows.Scan(&e.ID, &e.Path, &e.Name, &e.Size, &e.LastModified,
			&e.CreatedAt, &e.Folder, &e.RootPath); err != nil {
			return entries, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// MarkMetadataEmpty 将无元数据的图片 params_json/raw_json 置为 {}，
// 使其成为合法 JSON（后续不再被回填查询命中，避免每次启动重复解析）。
func (idb *ImageDB) MarkMetadataEmpty(id string) error {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	_, err := idb.db.Exec(`UPDATE images SET params_json = '{}', raw_json = '{}' WHERE id = ?`, id)
	return err
}

// ParamTagValue 单个生成参数标签（值 + 出现次数）
type ParamTagValue struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// GetParamTagAggregation 聚合所有图片 params_json 中每个参数键的取值分布。
// 返回 map[参数键]map[参数值]出现次数，供"生成参数标签面板"按类别汇总标签。
// 使用 json_each 展开 JSON 对象：一个键一个值一组（值过滤空串与纯空白）。
func (idb *ImageDB) GetParamTagAggregation() (map[string]map[string]int, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	rows, err := idb.db.Query(`
		SELECT je.key, je.value, COUNT(*) c
		FROM images, json_each(images.params_json) je
		WHERE json_valid(images.params_json) AND json_type(images.params_json) = 'object'
		  AND je.key != '' AND je.value != '' AND TRIM(je.value) != ''
		GROUP BY je.key, je.value`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agg := make(map[string]map[string]int)
	for rows.Next() {
		var key, val string
		var c int
		if err := rows.Scan(&key, &val, &c); err != nil {
			return nil, err
		}
		if key == "" || val == "" {
			continue
		}
		m := agg[key]
		if m == nil {
			m = make(map[string]int)
			agg[key] = m
		}
		m[val] += c
	}
	return agg, rows.Err()
}

// LoadImageCacheByPaths 按路径集合批量查询。超过 500 个路径分批查询。
func (idb *ImageDB) LoadImageCacheByPaths(paths []string) ([]ImageCacheEntry, error) {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	var entries []ImageCacheEntry
	const batchSize = 500
	for i := 0; i < len(paths); i += batchSize {
		end := i + batchSize
		if end > len(paths) {
			end = len(paths)
		}
		batch := paths[i:end]
		placeholders := make([]string, len(batch))
		args := make([]interface{}, len(batch))
		for j, p := range batch {
			placeholders[j] = "?"
			args[j] = p
		}
		query := `SELECT id, path, name, size, last_modified, created_at,
			folder, root_path, width, height, is_video FROM image_cache WHERE path IN (` +
			strings.Join(placeholders, ",") + `)`
		rows, err := idb.db.Query(query, args...)
		if err != nil {
			return entries, err
		}
		for rows.Next() {
			var e ImageCacheEntry
			var isVideo int
			if err := rows.Scan(&e.ID, &e.Path, &e.Name, &e.Size, &e.LastModified,
				&e.CreatedAt, &e.Folder, &e.RootPath, &e.Width, &e.Height, &isVideo); err != nil {
				rows.Close()
				return entries, err
			}
			e.IsVideo = isVideo != 0
			entries = append(entries, e)
		}
		rows.Close()
	}
	return entries, nil
}

// LoadImageCacheIDs 返回所有 imageID（用于 CleanOrphanedThumbs 等场景）。
func (idb *ImageDB) LoadImageCacheIDs() ([]string, error) {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	rows, err := idb.db.Query(`SELECT id FROM image_cache`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// LoadImageCacheMetaForThumbCounts 加载缩略图计数所需的轻量元数据（id/root_path/folder/is_video）。
// 用于 computeThumbCounts 与 bbolt thumbSet 做集合运算。
type ImageMetaForThumb struct {
	ID       string
	RootPath string
	Folder   string
	IsVideo  bool
}

func (idb *ImageDB) LoadImageCacheMetaForThumbCounts() ([]ImageMetaForThumb, error) {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	rows, err := idb.db.Query(`SELECT id, root_path, folder, is_video FROM image_cache`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var metas []ImageMetaForThumb
	for rows.Next() {
		var m ImageMetaForThumb
		var isVideo int
		if err := rows.Scan(&m.ID, &m.RootPath, &m.Folder, &isVideo); err != nil {
			return metas, err
		}
		m.IsVideo = isVideo != 0
		metas = append(metas, m)
	}
	return metas, rows.Err()
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

// CountBySizeForDedup 返回 image_cache 中相同 size 的记录数（用于判断是否需要计算内容哈希）
func (idb *ImageDB) CountBySizeForDedup(size int64) (int, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	var count int
	err := idb.db.QueryRow(`SELECT COUNT(*) FROM image_cache WHERE size=? AND content_hash!=''`, size).Scan(&count)
	return count, err
}

// ExistsByContentHash 检查是否已存在相同 content_hash 和 size 的记录
func (idb *ImageDB) ExistsByContentHash(hash string, size int64) (bool, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	var count int
	err := idb.db.QueryRow(`SELECT COUNT(*) FROM image_cache WHERE content_hash=? AND size=?`, hash, size).Scan(&count)
	return count > 0, err
}

// UpdateContentHash 更新指定 ID 的 content_hash 值
func (idb *ImageDB) UpdateContentHash(id string, hash string) error {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	_, err := idb.db.Exec(`UPDATE image_cache SET content_hash=? WHERE id=?`, hash, id)
	return err
}

// GetNullContentHashBatch 分批查询 content_hash 为空的记录（用于后台回填）
func (idb *ImageDB) GetNullContentHashBatch(limit int) ([]ImageCacheEntry, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	rows, err := idb.db.Query(`SELECT id, path, name, size, last_modified, created_at,
		folder, root_path, width, height, is_video FROM image_cache WHERE content_hash='' LIMIT ?`, limit)
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

// LoadDuplicatesByContentHash 按 content_hash 分组找出重复图片（每组返回多余条目的 ID）
func (idb *ImageDB) LoadDuplicatesByContentHash() (map[string][]string, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	rows, err := idb.db.Query(`SELECT id, content_hash FROM image_cache WHERE content_hash!='' ORDER BY content_hash, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := make(map[string][]string)
	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			return nil, err
		}
		groups[hash] = append(groups[hash], id)
	}
	// 只保留每组中 count>1 的，每组第一条作为保留，其余为重复
	dups := make(map[string][]string)
	for hash, ids := range groups {
		if len(ids) > 1 {
			dups[hash] = ids[1:] // 第一条保留，其余返回
		}
	}
	return dups, nil
}
