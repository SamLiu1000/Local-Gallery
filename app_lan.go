package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os/exec"
	"syscall"
	"path/filepath"
	"strings"
)

// startLANServer 启动局域网 HTTP Server，监听 0.0.0.0，供手机/平板访问
func (a *App) startLANServer() {
	a.startLANServerWithPort(25876)
}

// startLANServerWithPort 用自定义端口启动 LAN Server
func (a *App) startLANServerWithPort(port int) {
	if a.lanServer != nil {
		return // 已经在运行
	}
	lanPort := port
	addr := fmt.Sprintf("0.0.0.0:%d", lanPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Printf("[LAN] 端口 %d 不可用，使用随机端口\n", lanPort)
		listener, err = net.Listen("tcp", "0.0.0.0:0")
		if err != nil {
			fmt.Printf("[LAN] 无法启动局域网服务: %v\n", err)
			return
		}
		lanPort = listener.Addr().(*net.TCPAddr).Port
	}
	listener.Close()

	mux := http.NewServeMux()

	// ★ REST API — 供浏览器直接调用，替代 WailsBridge
	mux.HandleFunc("/api/folders", a.handleLANGetFolders)
	mux.HandleFunc("/api/images", a.handleLANGetImages)
	mux.HandleFunc("/api/images-by-paths", a.handleLANGetImagesByPaths)
	mux.HandleFunc("/api/roots", a.handleLANGetRoots)
	mux.HandleFunc("/api/full-rescan", a.handleLANFullRescanFolder)
	mux.HandleFunc("/api/user-data/", a.handleLANUserData)

	// 缩略图
	mux.HandleFunc("/thumb/", func(w http.ResponseWriter, r *http.Request) {
		imageID := strings.TrimPrefix(r.URL.Path, "/thumb/")
		jpegBytes, err := a.serveThumbnail(imageID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(jpegBytes)
	})

	// 原图
	mux.HandleFunc("/image/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		imageID := strings.TrimPrefix(r.URL.Path, "/image/")
		imagePath := a.resolveImagePath(imageID)
		if imagePath == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=600")
		w.Header().Set("Content-Type", getMIMEType(strings.ToLower(imagePath[strings.LastIndex(imagePath, "."):])))
		w.Header().Set("Accept-Ranges", "bytes")
		http.ServeFile(w, r, imagePath)
	})

	// 图标
	mux.HandleFunc("/icons/", func(w http.ResponseWriter, r *http.Request) {
		iconName := strings.TrimPrefix(r.URL.Path, "/icons/")
		if iconName == "" || strings.Contains(iconName, "..") {
			http.NotFound(w, r)
			return
		}
		embedPath := "static/icons/" + iconName
		data, err := assets.ReadFile(embedPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(data)
	})

	// 前端静态文件（SPA）
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}
		cleanPath := strings.TrimPrefix(path, "/")
		if strings.Contains(cleanPath, "..") {
			http.NotFound(w, r)
			return
		}
		data, err := fs.ReadFile(assets, "static/"+cleanPath)
		if err != nil {
			data, err = fs.ReadFile(assets, "static/index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
		}
		contentType := getContentType(cleanPath)
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Write(data)
	})

	// 提前获取 IP 和端口，供前端 GetLANInfo 立即查询
	a.lanIP = getLocalIP()
	a.lanPort = lanPort
	a.lanServer = &http.Server{Addr: fmt.Sprintf("0.0.0.0:%d", lanPort), Handler: mux}

	// ★ 自动添加 Windows 防火墙入站规则
	addFirewallRule(lanPort)

	go func() {
		fmt.Printf("[LAN] 局域网服务: http://%s:%d\n", a.lanIP, a.lanPort)
		if err := a.lanServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("[LAN] 局域网服务错误: %v\n", err)
		}
	}()
}

// StopLANServer 停止局域网服务
func (a *App) StopLANServer() error {
	if a.lanServer != nil {
		fmt.Println("[LAN] 正在停止局域网服务...")
		err := a.lanServer.Shutdown(context.Background())
		a.lanServer = nil
		a.lanPort = 0
		a.lanIP = ""
		// 删除防火墙规则
		deleteFirewallRule()
		return err
	}
	return nil
}

// GetLANInfo 获取局域网服务信息
func (a *App) GetLANInfo() map[string]interface{} {
	running := a.lanServer != nil
	info := map[string]interface{}{
		"running": running,
		"ip":      a.lanIP,
		"port":    a.lanPort,
	}
	if running {
		info["url"] = fmt.Sprintf("http://%s:%d", a.lanIP, a.lanPort)
	}
	return info
}

// StartLANServer 前端可调用的启动方法
func (a *App) StartLANServer(port int) map[string]interface{} {
	if a.lanServer != nil {
		return a.GetLANInfo()
	}
	a.startLANServerWithPort(port)
	return a.GetLANInfo()
}

// handleLANGetFolders 返回文件夹树（含图片数量）
func (a *App) handleLANGetFolders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	folders := a.GetFolders()
	json.NewEncoder(w).Encode(folders)
}

// handleLANGetImages 返回图片列表
func (a *App) handleLANGetImages(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	query := r.URL.Query()
	folder := query.Get("folder")
	offset := parseIntParam(query.Get("offset"), 0)
	limit := parseIntParam(query.Get("limit"), 250)
	sortOrder := query.Get("sort")
	if sortOrder == "" {
		sortOrder = "date-desc"
	}

	images := a.GetImages(folder, offset, limit, sortOrder)
	json.NewEncoder(w).Encode(images)
}

// handleLANGetImagesByPaths 根据路径列表获取图片
func (a *App) handleLANGetImagesByPaths(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Paths     []string `json:"paths"`
		Offset    int      `json:"offset"`
		Limit     int      `json:"limit"`
		SortOrder string   `json:"sortOrder"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": err.Error()})
		return
	}
	result := a.GetImagesByPaths(body.Paths, body.Offset, body.Limit, body.SortOrder)
	json.NewEncoder(w).Encode(result)
}

// handleLANGetRoots 返回导入的根文件夹列表
func (a *App) handleLANGetRoots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	roots := a.GetImportedRoots()
	json.NewEncoder(w).Encode(roots)
}

// handleLANFullRescanFolder 全量重新扫描文件夹
func (a *App) handleLANFullRescanFolder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != "POST" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": "无效请求体"})
		return
	}
	result := a.FullRescanFolder(body.Path)
	json.NewEncoder(w).Encode(result)
}

func parseIntParam(s string, defaultVal int) int {
	if s == "" {
		return defaultVal
	}
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

// handleLANUserData 处理 /api/user-data/* 请求，代理到 App 的用户数据方法
func (a *App) handleLANUserData(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	subPath := strings.TrimPrefix(r.URL.Path, "/api/user-data/")

	switch subPath {
	case "get-all":
		result := a.GetAllUserData()
		// 注入 registeredRoots：Storage.getRegisteredRoots() 在非 Wails 环境
		// 需要这个字段来恢复导入文件夹列表
		if data, ok := result["data"].(map[string]interface{}); ok {
			roots := a.GetImportedRoots()
			rootPaths := make([]string, len(roots))
			for i, r := range roots {
				rootPaths[i] = r.Path
			}
			data["registeredRoots"] = rootPaths
		}
		json.NewEncoder(w).Encode(result)
	case "save":
		if r.Method != "POST" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		data, ok := body["data"].(map[string]interface{})
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "missing data field"})
			return
		}
		json.NewEncoder(w).Encode(a.SaveUserData(data))
	case "save-roots":
		if r.Method != "POST" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Roots []string `json:"roots"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(a.SaveRoots(body.Roots))
	default:
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "unknown endpoint"})
	}
}

// getLocalIP 获取本机局域网 IPv4 地址
func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "0.0.0.0"
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
			return ipNet.IP.String()
		}
	}
	return "0.0.0.0"
}


// addFirewallRule 自动添加 Windows 防火墙入站规则
func addFirewallRule(port int) {
	ruleName := "Local Gallery LAN"
	// 隐藏命令行窗口
	hideWindow := func(cmd *exec.Cmd) {
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	}
	// 先删除旧规则（忽略错误）
	delCmd := exec.Command("netsh", "advfirewall", "firewall", "delete", "rule",
		"name="+ruleName, "dir=in")
	hideWindow(delCmd)
	delCmd.Run()
	// 添加新规则
	cmd := exec.Command("netsh", "advfirewall", "firewall", "add", "rule",
		"name="+ruleName,
		"dir=in",
		"action=allow",
		"protocol=TCP",
		fmt.Sprintf("localport=%d", port),
		"profile=any",
		"enable=yes")
	hideWindow(cmd)
	if err := cmd.Run(); err != nil {
		fmt.Printf("[LAN] 防火墙规则添加失败: %v\n", err)
	} else {
		fmt.Printf("[LAN] 已添加防火墙入站规则: 端口 %d\n", port)
	}
}

// deleteFirewallRule 删除防火墙规则
func deleteFirewallRule() {
	ruleName := "Local Gallery LAN"
	cmd := exec.Command("netsh", "advfirewall", "firewall", "delete", "rule",
		"name="+ruleName, "dir=in")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Run()
}

// getContentType 根据文件路径返回 HTTP Content-Type
func getContentType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	mimeTypes := map[string]string{
		".html": "text/html; charset=utf-8",
		".css":  "text/css; charset=utf-8",
		".js":   "application/javascript; charset=utf-8",
		".json": "application/json",
		".svg":  "image/svg+xml",
		".png":  "image/png",
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".gif":  "image/gif",
		".webp": "image/webp",
		".bmp":  "image/bmp",
		".ico":  "image/x-icon",
	}
	if ct, ok := mimeTypes[ext]; ok {
		return ct
	}
	return "application/octet-stream"
}