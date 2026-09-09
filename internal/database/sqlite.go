package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
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

	// ★ 后台重建 FTS 搜索索引：仅当 images 有数据但 images_fts 为空时执行
	//   （旧库首次升级到 FTS5 时填充一次；之后由触发器增量维护）。
	go idb.rebuildSearchFTSIfNeeded()

	return idb, nil
}

// rebuildSearchFTSIfNeeded 若 FTS 索引尚未全量重建（search_meta 无 fts_rebuilt 标记），
// 则从 images 重建。之后由触发器增量维护。
// ★ 注意：external-content 的 images_fts COUNT(*) 返回的是内容表行数而非索引行数，
//   因此用标记表判断，绝不能拿 images_fts 行数与 images 比较。
// ★ 用独立连接执行重建：主池 MaxOpenConns=1，若在主连接上跑几十秒的重建
//   会把整个应用的 DB 操作全部冻结；独立连接下 WAL 并发读不受影响。
func (idb *ImageDB) rebuildSearchFTSIfNeeded() {
	var marker string
	if err := idb.db.QueryRow(`SELECT value FROM search_meta WHERE key='fts_rebuilt'`).Scan(&marker); err == nil && marker == "1" {
		return
	}
	var imgCount int
	if err := idb.db.QueryRow(`SELECT COUNT(*) FROM images`).Scan(&imgCount); err != nil || imgCount == 0 {
		return
	}

	rebuildDB, err := sql.Open("sqlite", idb.dbPath+"?cache=shared&_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=60000")
	if err != nil {
		fmt.Printf("[搜索索引] FTS 重建连接失败: %v\n", err)
		return
	}
	defer rebuildDB.Close()
	start := time.Now()
	if _, err := rebuildDB.Exec(`INSERT INTO images_fts(images_fts) VALUES('rebuild')`); err != nil {
		fmt.Printf("[搜索索引] FTS 重建失败: %v\n", err)
		return
	}
	if _, err := rebuildDB.Exec(`INSERT INTO search_meta(key, value) VALUES('fts_rebuilt','1')
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
		fmt.Printf("[搜索索引] FTS 重建标记写入失败: %v\n", err)
	}
	fmt.Printf("[搜索索引] FTS 重建完成: %d 行，耗时 %s\n", imgCount, time.Since(start).Round(time.Millisecond))
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

	-- ★ FTS5 全文搜索索引（trigram 分词器：支持子串匹配，语义接近 LIKE %kw%）。
	--   external content：内容存 images 表，由触发器自动同步，Go 代码无需手动维护。
	--   trigram 要求查询词 >=3 字符，短词查询回退 LIKE（见 SearchImagesBySubstring）。
	CREATE VIRTUAL TABLE IF NOT EXISTS images_fts USING fts5(
		name, prompt, negative_prompt, path, params_json,
		content='images', content_rowid='rowid',
		tokenize='trigram'
	);
	CREATE TRIGGER IF NOT EXISTS images_fts_ai AFTER INSERT ON images BEGIN
		INSERT INTO images_fts(rowid, name, prompt, negative_prompt, path, params_json)
		VALUES (new.rowid, new.name, new.prompt, new.negative_prompt, new.path, new.params_json);
	END;
	CREATE TRIGGER IF NOT EXISTS images_fts_au AFTER UPDATE ON images BEGIN
		INSERT INTO images_fts(images_fts, rowid, name, prompt, negative_prompt, path, params_json)
		VALUES ('delete', old.rowid, old.name, old.prompt, old.negative_prompt, old.path, old.params_json);
		INSERT INTO images_fts(rowid, name, prompt, negative_prompt, path, params_json)
		VALUES (new.rowid, new.name, new.prompt, new.negative_prompt, new.path, new.params_json);
	END;

	-- ★ FTS 重建标记：external-content 表 COUNT(*) 返回内容表行数（≠已索引），
	--   用标记记录"是否已完成一次全量 rebuild"，避免每次启动误判跳过重建。
	CREATE TABLE IF NOT EXISTS search_meta (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL DEFAULT ''
	);

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
	// ★ 全文索引删除触发器单独执行（定义在 ftsDeleteTriggerSQL）：
	//   大目录删除会临时 DROP 它、删完再按同一份定义重建，保证两处定义永远一致。
	//   每次启动都执行一次 IF NOT EXISTS，即使上次删除中途崩溃也能自动补回。
	if _, err := idb.db.Exec(ftsDeleteTriggerSQL); err != nil {
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
	// ★ content_hash 索引：去重回填 GetNullContentHashBatch 每次启动查
	//   "content_hash=''" 时不再全表扫描 1.5M 行（走索引覆盖）。
	//   必须在列迁移之后创建。
	if _, err := idb.db.Exec(`CREATE INDEX IF NOT EXISTS idx_image_cache_content_hash ON image_cache(content_hash)`); err != nil {
		fmt.Printf("[数据库迁移] content_hash 索引创建失败: %v\n", err)
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
	// ★ 超大目录走"临时禁用全文索引删除触发器 + 删完重建索引"的快路径：
	//   逐行维护 FTS 删除实测约 50µs/行（删 17.8 万行 ≈ 9 秒），而整表重建索引
	//   约 15µs/行。当待删行数够多、且占整表比例够高时，重建明显更快。
	if idb.shouldRebuildFTSForDelete(rootPath) {
		return idb.deleteByRootWithFTSRebuild(rootPath)
	}
	// 普通规模：分批删除，且每批单独加锁：images 表带 FTS5 全文搜索触发器（images_fts_ad），
	//   每删一行都会同步更新搜索索引。若整个删除过程一直持有 idb.mu（原实现如此），
	//   删除大文件夹期间所有读查询（GetImages 等）都会被阻塞 ——
	//   表现为"删除文件夹时点别的文件夹加载不出图"。批间释放锁，读操作可穿插执行。
	const batchSize = 1000
	var total int
	for {
		affected, err := idb.deleteByRootBatch(rootPath, batchSize)
		total += affected
		if err != nil {
			return total, err
		}
		if affected < batchSize {
			return total, nil
		}
	}
}

// ftsDeleteTriggerSQL 是 images 表的全文索引删除触发器定义。
// ★ 单独定义成常量：DeleteByRoot 的快路径会临时 DROP 它、删完再用这份定义重建，
//   保证"建触发器"与"恢复触发器"永远用同一份 SQL。
const ftsDeleteTriggerSQL = `CREATE TRIGGER IF NOT EXISTS images_fts_ad AFTER DELETE ON images BEGIN
	INSERT INTO images_fts(images_fts, rowid, name, prompt, negative_prompt, path, params_json)
	VALUES ('delete', old.rowid, old.name, old.prompt, old.negative_prompt, old.path, old.params_json);
END;`

// shouldRebuildFTSForDelete 判断本次删除是否值得走"删完重建索引"的快路径。
// 判据：待删行数 ≥ minRows，且占 images 总行数 ≥ minPercent%。
// ★ 阈值按实测标定：不删触发器约 50µs/行、删触发器约 26µs/行、重建索引约 18µs/行，
//   代入 删D×50 = 删D×26 + 剩余R×18 得临界 D/R ≈ 0.75（即待删 ≥ 总行数的 ~43%）。
//   取 50% 留出余量，避免在临界点附近反而变慢。
func (idb *ImageDB) shouldRebuildFTSForDelete(rootPath string) bool {
	const minRows = 30000
	const minPercent = 50

	idb.mu.RLock()
	var toDelete int
	err := idb.db.QueryRow(`SELECT COUNT(*) FROM images WHERE root_path = ?`, rootPath).Scan(&toDelete)
	idb.mu.RUnlock()
	if err != nil || toDelete < minRows {
		return false
	}

	idb.mu.RLock()
	var total int
	err = idb.db.QueryRow(`SELECT COUNT(*) FROM images`).Scan(&total)
	idb.mu.RUnlock()
	if err != nil || total <= 0 {
		return false
	}
	return toDelete*100 >= total*minPercent
}

// deleteByRootWithFTSRebuild 快路径：移除 FTS 删除触发器 → 快速批量删除 →
// 恢复触发器 → 用独立连接重建全文索引（顺带清掉期间所有残留的旧索引条目）。
func (idb *ImageDB) deleteByRootWithFTSRebuild(rootPath string) (int, error) {
	// 1. 移除删除触发器。★ 崩溃兜底：下次启动 initSchema 的
	//    CREATE TRIGGER IF NOT EXISTS 会把它补回来。
	if _, err := idb.execLocked(`DROP TRIGGER IF EXISTS images_fts_ad`); err != nil {
		return 0, err
	}

	// 2. 快速批量删除（此期间不再逐行维护全文索引）
	const batchSize = 5000
	var total int
	var delErr error
	for {
		res, err := idb.execLocked(
			`DELETE FROM images WHERE id IN (SELECT id FROM images WHERE root_path = ? LIMIT ?)`,
			rootPath, batchSize)
		if err != nil {
			delErr = err
			break
		}
		n, _ := res.RowsAffected()
		total += int(n)
		if int(n) < batchSize {
			break
		}
	}

	// 3. 先恢复触发器（无论删除成败），再做重建
	_, trigErr := idb.execLocked(ftsDeleteTriggerSQL)
	if delErr != nil {
		return total, delErr
	}
	if trigErr != nil {
		return total, trigErr
	}

	// 4. 重建全文索引
	if err := idb.rebuildFTSIndex(); err != nil {
		return total, err
	}
	fmt.Printf("[搜索索引] 大目录删除完成（%d 行），已重建全文索引\n", total)
	return total, nil
}

// execLocked 持 idb.mu 执行一条 SQL。
func (idb *ImageDB) execLocked(query string, args ...interface{}) (sql.Result, error) {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	return idb.db.Exec(query, args...)
}

// rebuildFTSIndex 用独立连接重建全文索引。
// ★ 主连接池 MaxOpenConns=1，在它上面跑几十秒的重建会把应用所有 DB 操作冻结；
//   独立连接下 WAL 并发读不受影响（与 rebuildSearchFTSIfNeeded 同一套做法）。
func (idb *ImageDB) rebuildFTSIndex() error {
	rebuildDB, err := sql.Open("sqlite", idb.dbPath+"?cache=shared&_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=60000")
	if err != nil {
		return err
	}
	defer rebuildDB.Close()
	start := time.Now()
	if _, err := rebuildDB.Exec(`INSERT INTO images_fts(images_fts) VALUES('rebuild')`); err != nil {
		return err
	}
	fmt.Printf("[搜索索引] FTS 重建完成，耗时 %s\n", time.Since(start).Round(time.Millisecond))
	return nil
}

// deleteByRootBatch 删除一批属于 rootPath 的 images 行（独立加锁，批间释放 idb.mu）。
func (idb *ImageDB) deleteByRootBatch(rootPath string, batchSize int) (int, error) {
	idb.mu.Lock()
	defer idb.mu.Unlock()
	tx, err := idb.db.Begin()
	if err != nil {
		return 0, err
	}
	res, err := tx.Exec(
		"DELETE FROM images WHERE id IN (SELECT id FROM images WHERE root_path = ? LIMIT ?)",
		rootPath, batchSize,
	)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	affected, _ := res.RowsAffected()
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
// buildTrigramMatch 将用户查询转成 FTS5 trigram MATCH 表达式：
// 按空白分词、去掉引号/反斜杠等 FTS 特殊字符，仅保留 >=3 字符的词
// （trigram 分词器无法索引 1-2 字符），每词用双引号包裹（短语子串匹配），
// 词间空格 = AND。返回空串表示无法用 FTS（应回退 LIKE）。
func buildTrigramMatch(query string) string {
	fields := strings.Fields(query)
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ReplaceAll(f, `"`, "")
		f = strings.ReplaceAll(f, `\`, "")
		if utf8.RuneCountInString(f) < 3 {
			continue
		}
		parts = append(parts, `"`+f+`"`)
	}
	return strings.Join(parts, " ")
}

func (idb *ImageDB) SearchImagesBySubstring(query string, folder string, offset int, limit int) ([]SearchResult, int, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()

	// 转义通配符后再拼模糊模式
	searchPattern := "%" + escapeLike(query) + "%"

	// 相关度打分：命中字段越重要分越高（单表 LIKE 路径用）
	scoreExpr := `(CASE WHEN name LIKE ? ESCAPE '\' THEN 5 ELSE 0 END
		+ CASE WHEN prompt LIKE ? ESCAPE '\' THEN 4 ELSE 0 END
		+ CASE WHEN negative_prompt LIKE ? ESCAPE '\' THEN 2 ELSE 0 END
		+ CASE WHEN path LIKE ? ESCAPE '\' THEN 2 ELSE 0 END
		+ CASE WHEN params_json LIKE ? ESCAPE '\' THEN 1 ELSE 0 END)`
	scoreArgs := []interface{}{searchPattern, searchPattern, searchPattern, searchPattern, searchPattern}
	// 相关度打分（FTS JOIN 路径：images_fts 也暴露同名列，必须加 i. 前缀消除歧义）
	scoreExprJoin := `(CASE WHEN i.name LIKE ? ESCAPE '\' THEN 5 ELSE 0 END
		+ CASE WHEN i.prompt LIKE ? ESCAPE '\' THEN 4 ELSE 0 END
		+ CASE WHEN i.negative_prompt LIKE ? ESCAPE '\' THEN 2 ELSE 0 END
		+ CASE WHEN i.path LIKE ? ESCAPE '\' THEN 2 ELSE 0 END
		+ CASE WHEN i.params_json LIKE ? ESCAPE '\' THEN 1 ELSE 0 END)`

	// 文件夹过滤（FTS 与 LIKE 两路径共用）
	folderClause := ""
	folderArgs := []interface{}{}
	if folder != "" {
		normalizedFolder := strings.ReplaceAll(folder, "\\", "/")
		folderClause = " AND (i.root_path = ? OR i.folder LIKE ? ESCAPE '\\')"
		folderArgs = append(folderArgs, normalizedFolder, normalizedFolder+"/%")
	}

	// ★ FTS5(trigram) 快路径：查询 >=3 字符时走全文索引，命中行才做 LIKE 打分，
	//   避免每次搜索全表扫描 57 万行（多列 OR LIKE）。
	//   注意：MATCH 运算符必须直接作用在 FTS 表名上，不能使用表别名。
	if matchExpr := buildTrigramMatch(query); matchExpr != "" {
		countSQL := "SELECT COUNT(*) FROM images_fts JOIN images i ON i.rowid = images_fts.rowid WHERE images_fts MATCH ?" + folderClause
		countArgs := append([]interface{}{matchExpr}, folderArgs...)
		var total int
		if err := idb.db.QueryRow(countSQL, countArgs...).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("搜索计数失败: %w", err)
		}
		querySQL := `SELECT i.id, i.path, i.name, i.size, i.last_modified, i.created_at, i.folder, i.root_path,
			i.prompt, i.negative_prompt, i.params_json, ` + scoreExprJoin + ` AS score
			FROM images_fts JOIN images i ON i.rowid = images_fts.rowid
			WHERE images_fts MATCH ?` + folderClause + `
			ORDER BY score DESC, i.last_modified DESC LIMIT ? OFFSET ?`
		// 占位符顺序：scoreExprJoin(5) + MATCH(1) + folderArgs(0|2) + limit/offset(2)
		queryArgs := append([]interface{}{}, scoreArgs...)
		queryArgs = append(queryArgs, matchExpr)
		queryArgs = append(queryArgs, folderArgs...)
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
		return results, total, rows.Err()
	}

	// LIKE 回退路径（短词 <3 字符时 trigram 不可用）
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
//
//	直接 json_extract 会抛 "malformed JSON" 导致整条查询失败。
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
	// ★ 与 DeleteByRoot 同理：每批独立加锁，批间释放 idb.mu，
	//   避免删除大文件夹期间独占数据库锁、卡住其它文件夹的读查询。
	const batchSize = 1000
	var total int
	for {
		idb.mu.Lock()
		res, err := idb.db.Exec(
			"DELETE FROM image_cache WHERE id IN (SELECT id FROM image_cache WHERE root_path = ? LIMIT ?)",
			rootPath, batchSize,
		)
		idb.mu.Unlock()
		if err != nil {
			return total, err
		}
		affected, _ := res.RowsAffected()
		total += int(affected)
		if int(affected) < batchSize {
			return total, nil
		}
	}
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

// SaveImageCacheByRoot 保存某根目录的完整图片缓存（DELETE-then-replace）。
// ★ 分批落库：删除与插入都按小批执行、批间释放 idb.mu，避免大根目录（数十万张）
//   的单事务长期独占锁，导致刷新期间 GetImages 全部阻塞（表现为"刷新后一直不出图"）。
//   非原子写入：中途崩溃由刷新标记（setScanInProgress）在下次启动时补扫恢复。
func (idb *ImageDB) SaveImageCacheByRoot(rootPath string, entries []ImageCacheEntry) error {
	const delBatch = 1000
	const insBatch = 2000

	// 1. 分批删除该根目录旧记录
	for {
		idb.mu.Lock()
		res, err := idb.db.Exec(
			"DELETE FROM image_cache WHERE id IN (SELECT id FROM image_cache WHERE root_path = ? LIMIT ?)",
			rootPath, delBatch,
		)
		if err != nil {
			idb.mu.Unlock()
			return err
		}
		affected, _ := res.RowsAffected()
		idb.mu.Unlock()
		if affected < delBatch {
			break
		}
	}
	if len(entries) == 0 {
		return nil
	}

	// 2. 分批插入（INSERT OR REPLACE，批间释放锁让读查询可插队）
	stmt := `INSERT OR REPLACE INTO image_cache
		(id, path, name, size, last_modified, created_at, folder, root_path, width, height, is_video, content_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	for i := 0; i < len(entries); i += insBatch {
		end := i + insBatch
		if end > len(entries) {
			end = len(entries)
		}
		idb.mu.Lock()
		tx, err := idb.db.Begin()
		if err == nil {
			for _, e := range entries[i:end] {
				isVideo := 0
				if e.IsVideo {
					isVideo = 1
				}
				if _, err2 := tx.Exec(stmt, e.ID, e.Path, e.Name, e.Size, e.LastModified,
					e.CreatedAt, e.Folder, e.RootPath, e.Width, e.Height, isVideo, e.ContentHash); err2 != nil {
					err = err2
					break
				}
			}
			if err == nil {
				err = tx.Commit()
			} else {
				tx.Rollback()
			}
		}
		idb.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

// LoadAllImageCache 加载全部图片缓存
func (idb *ImageDB) LoadAllImageCache() ([]ImageCacheEntry, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
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
	idb.mu.RLock()
	defer idb.mu.RUnlock()
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
	idb.mu.RLock()
	defer idb.mu.RUnlock()
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
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	var n int
	err := idb.db.QueryRow(`SELECT COUNT(*) FROM image_cache WHERE root_path=? AND folder=?`, rootPath, folder).Scan(&n)
	return n, err
}

// GetImageByFolderOffset 按 ORDER BY id 分页取某文件夹直接图片的 ID、修改时间与路径（侧边栏预览用，LIMIT 1 开销极低）
func (idb *ImageDB) GetImageByFolderOffset(rootPath, folder string, offset int) (string, int64, string, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	var id string
	var lm int64
	var p string
	err := idb.db.QueryRow(`SELECT id, last_modified, path FROM image_cache WHERE root_path=? AND folder=? ORDER BY id LIMIT 1 OFFSET ?`,
		rootPath, folder, offset).Scan(&id, &lm, &p)
	return id, lm, p, err
}

// LoadImageCacheByRoot 加载整个根目录所有子文件夹的图片。
func (idb *ImageDB) LoadImageCacheByRoot(rootPath string) ([]ImageCacheEntry, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
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

// LoadImageCacheByPathPrefix 加载某个文件夹（含其所有子目录）下的全部图片缓存记录。
// ★ 专为"嵌套虚拟根"：这些记录在库里 root_path 记的是外层父根目录，按 root_path
// 精确匹配会得到 0 条（点索引按钮就报"无图片需索引"）。必须按完整路径前缀取。
// image_cache 有 path 索引，范围查询走索引。
func (idb *ImageDB) LoadImageCacheByPathPrefix(pathPrefix string) ([]ImageCacheEntry, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	lower := pathPrefix + "\\"
	upper := lower + "\uFFFF"
	rows, err := idb.db.Query(`SELECT id, path, name, size, last_modified, created_at,
		folder, root_path, width, height, is_video FROM image_cache WHERE path >= ? AND path < ?`, lower, upper)
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
// ★ 下界用路径分隔符限定 [prefix+'\', prefix+'\'+\uFFFF)：只匹配"该文件夹内或其子目录"的记录，
//
//	避免把"名称是前缀延伸"的兄弟文件夹（如 "A (1)" 之于 "A"）误算进来。
//	原上界 prefix+\uFFFF 会把 "A (1)"（空格 0x20 < 0x5C）一并匹配，导致点击某文件夹时
//	把全部同名变体文件夹的记录都查回来（如 Imageye 重复下载的 18 个文件夹合成上千条），
//	而前端过滤按 "folder + /" 精确匹配又排除变体 → 图廊空、计数与图量对不上。
func (idb *ImageDB) LoadImageCacheByPathPrefixPaged(pathPrefix string, offset, limit int, sortOrder string) ([]ImageCacheEntry, int, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	if limit <= 0 {
		limit = 500
	}
	// 下界：路径分隔符限定；上界：下界 + U+FFFF，匹配所有以此开头的路径
	lower := pathPrefix + "\\"
	upper := lower + "\uFFFF"
	where := "path >= ? AND path < ?"
	args := []interface{}{lower, upper}
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

// LoadImageCacheIDsUnderPath 返回位于该文件夹"子树"内的全部 image_cache ID（不含该文件夹本身，
// 文件夹路径不会是图片文件）。用于刷新时以磁盘为权威补全 oldIDs：
// 内存 folderIndex 里已被清出的孤儿记录（源文件删除后 image_cache 表残留）也能被纳入差异对比。
// ★ 用路径分隔符限定范围 [folder+'\', folder+'\'+\uFFFF)：只匹配"该文件夹内或其子目录"的记录，
//
//	避免把"名称是前缀延伸"的兄弟文件夹（如 "A (1)" 之于 "A"）误算进来（P+\uFFFF 上界会误吞）。
func (idb *ImageDB) LoadImageCacheIDsUnderPath(folderPath string) ([]string, error) {
	idb.mu.RLock()
	defer idb.mu.RUnlock()
	lower := strings.ReplaceAll(folderPath, "/", "\\") + "\\"
	upper := lower + "\uFFFF"
	rows, err := idb.db.Query(`SELECT id FROM image_cache WHERE path >= ? AND path < ?`, lower, upper)
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

func imageCacheOrderBy(sortOrder string) string {
	switch sortOrder {
	case "name-asc", "name":
		return "name ASC, path ASC"
	case "name-desc":
		return "name DESC, path ASC"
	case "folder-asc":
		return "folder ASC, name ASC, path ASC"
	case "folder-desc":
		return "folder DESC, name ASC, path ASC"
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
	idb.mu.RLock()
	defer idb.mu.RUnlock()
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
	idb.mu.RLock()
	defer idb.mu.RUnlock()
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
	idb.mu.RLock()
	defer idb.mu.RUnlock()
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
