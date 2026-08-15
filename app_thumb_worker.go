//go:build !bindings

package main

// ============================================================
// 缩略图生成独立 worker 进程
//
// 目标：把"读原图 + libvips 生成"放到一个独立子进程里。
// 用户切换文件夹时主进程 kill 掉它 —— OS 会立即终止它在途的
// 原图文件读取，等效"关闭程序"。比进程内 goroutine 强在：
// 进程内的 libvips 同步解码无法被打断，子进程可以。
//
// worker 不碰 bbolt（thumbnails.db 只允许单进程打开），只负责
// 读原图并返回 JPEG 字节，由主进程落库。
// ============================================================

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// thumbWorker 全局状态：当前 worker 进程与端口
var (
	thumbWorkerMu   sync.Mutex
	thumbWorkerCmd  *exec.Cmd
	thumbWorkerPort string

	// thumbWorkerSpawnDisabled 测试环境禁用 spawn：go test 的测试二进制
	// 不处理 --thumbgen，spawn 只会产生杂散进程。测试里置为 true。
	thumbWorkerSpawnDisabled bool
)

// runThumbGenWorker headless 模式入口（--thumbgen --port N [--concurrency N]）。
// 只提供 /gen 接口：POST {path,maxSize,quality} → JPEG 字节。
// concurrency > 0 时按该值设置 libvips 线程池（与主进程并发设置同步）。
func runThumbGenWorker(port string, concurrency int) {
	if port == "" {
		fmt.Println("[缩略图worker] 缺少 --port")
		os.Exit(1)
	}
	if concurrency > 0 {
		fmt.Printf("[缩略图worker] libvips 并发: %d\n", concurrency)
		initVipsWithConcurrency(concurrency)
	} else {
		initVips()
	}
	defer shutdownVips()

	mux := http.NewServeMux()
	mux.HandleFunc("/gen", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path    string `json:"path"`
			MaxSize int    `json:"maxSize"`
			Quality int    `json:"quality"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Path == "" || req.MaxSize <= 0 || req.Quality <= 0 {
			http.Error(w, "invalid params", http.StatusBadRequest)
			return
		}
		jpegBytes, err := generateThumbnailBytes(req.Path, req.MaxSize, req.Quality)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegBytes)
	})

	addr := "127.0.0.1:" + port
	fmt.Printf("[缩略图worker] 就绪 %s\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Printf("[缩略图worker] 退出: %v\n", err)
		os.Exit(1)
	}
}

// ensureThumbWorker 确保 worker 进程存活（未启动或已死则拉起）。返回是否可用。
// 存活判断以"后台 Wait 协程是否已回收"为准（进程退出即被清空），
// 不做端口探测——启动窗口内的短暂不可用由 generateViaWorker 的重试覆盖。
func (a *App) ensureThumbWorker() bool {
	thumbWorkerMu.Lock()
	defer thumbWorkerMu.Unlock()
	if thumbWorkerSpawnDisabled {
		return false
	}
	if thumbWorkerCmd != nil {
		return true // 存活（Wait 协程会在进程退出时清掉状态）
	}

	// 找空闲端口
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Printf("[缩略图worker] 端口分配失败: %v\n", err)
		return false
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()

	exe, err := os.Executable()
	if err != nil {
		fmt.Printf("[缩略图worker] 无法定位自身可执行文件: %v\n", err)
		return false
	}
	thumbSemMu.RLock()
	workerConcurrency := int(thumbSemSize)
	thumbSemMu.RUnlock()
	fmt.Printf("[缩略图worker] spawn: %s --thumbgen --port %s --concurrency %d\n", exe, port, workerConcurrency)
	nc := exec.Command(exe, "--thumbgen", "--port", port, "--concurrency", strconv.Itoa(workerConcurrency))
	// ★ stderr 重定向到主程序：worker 启动即崩溃（vips DLL 找不到等）时能看到原因
	nc.Stdout = os.Stderr
	nc.Stderr = os.Stderr
	if err := nc.Start(); err != nil {
		fmt.Printf("[缩略图worker] 启动失败: %v\n", err)
		return false
	}
	thumbWorkerCmd = nc
	thumbWorkerPort = port

	// ★ 后台回收：进程退出后清掉状态（否则 ProcessState 永远 nil，误判存活）
	go func(c *exec.Cmd) {
		_ = c.Wait()
		thumbWorkerMu.Lock()
		if thumbWorkerCmd == c {
			fmt.Printf("[缩略图worker] 进程已退出 pid=%d\n", c.Process.Pid)
			thumbWorkerCmd = nil
			thumbWorkerPort = ""
		}
		thumbWorkerMu.Unlock()
	}(nc)

	fmt.Printf("[缩略图worker] 已启动 pid=%d port=%s\n", nc.Process.Pid, port)
	return true
}

// killThumbWorker 杀死 worker 进程：OS 立即终止其在途的原图读取。
func (a *App) killThumbWorker() {
	thumbWorkerMu.Lock()
	defer thumbWorkerMu.Unlock()
	if thumbWorkerCmd != nil && thumbWorkerCmd.Process != nil {
		fmt.Printf("[缩略图worker] 杀进程 pid=%d\n", thumbWorkerCmd.Process.Pid)
		_ = thumbWorkerCmd.Process.Kill()
		_ = thumbWorkerCmd.Wait() // 回收僵尸
	} else {
		// ★ 诊断关键：无 worker 可杀 = worker 从未 spawn 或已退出，
		//   说明生成在走主进程 inline 回退（那 kill 机制就没生效）
		fmt.Println("[缩略图worker] kill 时无存活 worker（生成可能走主进程 inline 回退）")
	}
	thumbWorkerCmd = nil
	thumbWorkerPort = ""
}

// generateViaWorker 优先经 worker 进程生成缩略图字节。
// 失败返回 err，由调用方决定回退进程内生成或直接放弃。
func (a *App) generateViaWorker(srcPath string) ([]byte, error) {
	if !a.ensureThumbWorker() {
		return nil, fmt.Errorf("缩略图 worker 不可用")
	}
	thumbWorkerMu.Lock()
	port := thumbWorkerPort
	thumbWorkerMu.Unlock()
	if port == "" {
		return nil, fmt.Errorf("缩略图 worker 端口缺失")
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"path":    srcPath,
		"maxSize": thumbMaxSize,
		"quality": thumbJPEGQuality,
	})
	client := &http.Client{Timeout: 120 * time.Second}
	// ★ 重试几次：覆盖 worker 刚 spawn 尚未就绪（vips 初始化）的窗口；
	//   连续失败则清状态，让下次调用重新拉起。
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := client.Post("http://127.0.0.1:"+port+"/gen", "application/json", bytes.NewReader(payload))
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				data, readErr := io.ReadAll(resp.Body)
				resp.Body.Close()
				return data, readErr
			}
			lastErr = fmt.Errorf("worker 生成失败: HTTP %d", resp.StatusCode)
			resp.Body.Close()
		} else {
			lastErr = err
		}
		time.Sleep(150 * time.Millisecond)
	}
	a.killThumbWorker() // 清掉失效 worker，下次调用重新 spawn
	return nil, lastErr
}
