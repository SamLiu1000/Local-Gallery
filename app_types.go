package main

import "local-gallery/internal/database"

// ==================== 类型定义 ====================

type ScanResult struct {
	Success    bool        `json:"success"`
	FileCount  int         `json:"fileCount"`
	FolderPath string      `json:"folderPath,omitempty"`
	RootCount  int         `json:"rootCount,omitempty"`
	Message    string      `json:"message"`
	Folder     *FolderNode `json:"folder,omitempty"`
}

// FolderDiffResult 增量刷新结果：只包含新增和删除的图片
type FolderDiffResult struct {
	Added     []SafeImage `json:"added"`
	Removed   []string    `json:"removed"` // image IDs
	Unchanged int         `json:"unchanged"`
	Success   bool        `json:"success"`
	Error     string      `json:"error,omitempty"`
}

type SafeImage struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	Folder       string `json:"folder"`
	RootPath     string `json:"rootPath"`
	URL          string `json:"url"`
	ThumbURL     string `json:"thumbUrl"`
	Size         int64  `json:"size"`
	LastModified int64  `json:"lastModified"`
	CreatedAt    int64  `json:"createdAt"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	IsVideo      bool   `json:"isVideo"`
}

type ImageListResult struct {
	Items  []SafeImage `json:"items"`
	Total  int         `json:"total"`
	Offset int         `json:"offset"`
	Limit  int         `json:"limit"`
}

type IndexStatus struct {
	RegisteredRoots []string `json:"registeredRoots"`
	FileCount       int      `json:"fileCount"`
	FolderCount     int      `json:"folderCount"`
}

type ImageFileData struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
	Data     []byte `json:"data"`
}

type CacheSetItem struct {
	FilePath     string                 `json:"filePath"`
	FileName     string                 `json:"fileName"`
	RootPath     string                 `json:"rootPath"`
	FileSize     int64                  `json:"fileSize"`
	FileModified int64                  `json:"fileModified"`
	Metadata     map[string]interface{} `json:"metadata"`
}

type CacheKeyItem struct {
	FilePath     string `json:"filePath"`
	FileSize     int64  `json:"fileSize"`
	FileModified int64  `json:"fileModified"`
}

type CacheItem struct {
	FilePath     string                 `json:"filePath"`
	FileName     string                 `json:"fileName"`
	RootPath     string                 `json:"rootPath"`
	Metadata     map[string]interface{} `json:"metadata"`
	CachedAt     int64                  `json:"cachedAt"`
	FileSize     int64                  `json:"fileSize"`
	FileModified int64                  `json:"fileModified"`
}

type CacheStats struct {
	Total    int    `json:"total"`
	Oldest   int64  `json:"oldest"`
	Newest   int64  `json:"newest"`
	Size     string `json:"size"`
	CacheDir string `json:"cacheDir"`
}

type ProxyRequest struct {
	RequestID string            `json:"requestId"`
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	ProxyHost string            `json:"proxyHost"`
	ProxyPort int               `json:"proxyPort"`
}

type ProxyResponse struct {
	StatusCode int                 `json:"statusCode"`
	Headers    map[string][]string `json:"headers"`
	Body       string              `json:"body"`
}

// ImageEntry 图片索引条目
type ImageEntry struct {
	ID           string `json:"id"`
	Path         string `json:"path"`
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	LastModified int64  `json:"lastModified"`
	CreatedAt    int64  `json:"createdAt"`
	Folder       string `json:"folder"`
	RootPath     string `json:"rootPath"`
	URL          string `json:"url"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	IsVideo      bool   `json:"isVideo"`
	ContentHash  string `json:"-"`
}

// SearchRequest 搜索请求
type SearchRequest struct {
	Query  string `json:"query"`
	Folder string `json:"folder,omitempty"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// SearchResponse 搜索响应
type SearchResponse struct {
	Success bool                    `json:"success"`
	Items   []database.SearchResult `json:"items"`
	Total   int                     `json:"total"`
	Offset  int                     `json:"offset"`
	Limit   int                     `json:"limit"`
	Message string                  `json:"message,omitempty"`
	Code    string                  `json:"code,omitempty"` // 错误码，前端映射 i18n
}

// SearchCondition 高级搜索的单个条件
type SearchCondition struct {
	Field string `json:"field"` // "all", "path", "prompt", "negative_prompt", "params_json"
	Value string `json:"value"`
	Mode  string `json:"mode"` // "contains", "exact", "exclude", "word"
}

// AdvancedSearchRequest 高级搜索请求
type AdvancedSearchRequest struct {
	Conditions []SearchCondition `json:"conditions"`
	Folders    []string          `json:"folders"`
	DateFrom   int64             `json:"dateFrom"`  // unix milliseconds, 0 = no lower bound
	DateTo     int64             `json:"dateTo"`    // unix milliseconds, 0 = no upper bound
	MatchMode  string            `json:"matchMode"` // "and" | "or"
	Offset     int               `json:"offset"`
	Limit      int               `json:"limit"`
}

// AdvancedSearchResponse 高级搜索响应
type AdvancedSearchResponse struct {
	Success bool                    `json:"success"`
	Items   []database.SearchResult `json:"items"`
	Total   int                     `json:"total"`
	Offset  int                     `json:"offset"`
	Limit   int                     `json:"limit"`
	Message string                  `json:"message,omitempty"`
	Code    string                  `json:"code,omitempty"` // 错误码，前端映射 i18n
}

// ParamTagItem 生成参数标签面板的单个标签（参数值 + 出现次数）
type ParamTagItem struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// ParamTagGroup 生成参数标签面板的单个类别分组（如"模型"类别下的所有模型标签）
type ParamTagGroup struct {
	Key   string         `json:"key"`   // params_json 中的参数键，如 "Model" / "CFG Scale"
	Field string         `json:"field"` // 高级搜索字段名，如 "json:Model"，点击标签时直接作为条件 field
	Mode  string         `json:"mode"`  // 点击标签时的匹配模式："contains" / "exact"
	Tags  []ParamTagItem `json:"tags"`
}

// ParamTagsResponse 生成参数标签面板响应
type ParamTagsResponse struct {
	Success bool            `json:"success"`
	Groups  []ParamTagGroup `json:"groups"`
	Message string          `json:"message,omitempty"`
	Code    string          `json:"code,omitempty"`
}

// FolderPreview 子文件夹缩略图预览条目（含 lastModified 与 path，供前端复用图廊缩略图 URL、点击定位图片）
type FolderPreview struct {
	ID           string `json:"id"`
	LastModified int64  `json:"lastModified"`
	Path         string `json:"path,omitempty"`
}

// FolderNode 文件夹树节点
type FolderNode struct {
	Name       string          `json:"name"`
	Path       string          `json:"path"`
	ImageCount int             `json:"imageCount"`
	ThumbCount int             `json:"thumbCount"`
	FolderType string          `json:"folderType,omitempty"`
	Previews   []FolderPreview `json:"previews,omitempty"` // 缩略图预览：该文件夹直接包含的图片（确定性随机 ≤4）
	Children   []*FolderNode   `json:"children"`
}

// IndexResult 索引操作结果
type IndexResult struct {
	Success      bool   `json:"success"`
	RootPath     string `json:"rootPath"`
	IndexedCount int    `json:"indexedCount"`
	TotalCount   int    `json:"totalCount"`
	Message      string `json:"message"`
}

// imgInfo 内部结构体（用于 collectImages）
type imgInfo struct {
	id           string
	path         string
	name         string
	size         int64
	lastModified int64
	folder       string
}

// PreGenStatus 预生成缩略图进度状态（供 Wails 绑定使用）
type PreGenStatus struct {
	Running bool   `json:"running"`
	Paused  bool   `json:"paused"`
	Folder  string `json:"folder"`
	Total   int    `json:"total"`
	Done    int    `json:"done"`
	Skipped int    `json:"skipped"`
	Failed  int    `json:"failed"`
}

// DebugScanResult 诊断扫描结果，用于排查图片计数不匹配问题
type DebugScanResult struct {
	RootPath       string   `json:"rootPath"`
	DiskTotalFiles int      `json:"diskTotalFiles"` // 磁盘上所有文件数
	DiskImageFiles int      `json:"diskImageFiles"` // 磁盘上图片/视频文件数（经 isImageFile/isVideoFile 过滤）
	AppScanCount   int      `json:"appScanCount"`   // 应用 scanWalk 扫描到的数量
	SkippedDirs    []string `json:"skippedDirs"`    // 因权限等原因被跳过的目录
	FailedFiles    []string `json:"failedFiles"`    // Info() 失败被跳过的文件
	SampleMissing  []string `json:"sampleMissing"`  // 磁盘有但应用未扫到的文件样本（最多50条）
}
