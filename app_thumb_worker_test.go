package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// initVipsTest 测试内启动 vips（幂等）
func initVipsTest(t *testing.T) {
	t.Helper()
	initVips()
}

// init:测试环境禁用真实 worker spawn（测试二进制不处理 --thumbgen）
func init() {
	thumbWorkerSpawnDisabled = true
}

// TestGenerateThumbnailBytesWorkerContract 验证：
// 1. generateThumbnailBytes 能生成合法 JPEG
// 2. worker 的 /gen HTTP 契约（JSON in → JPEG out）与 generateViaWorker 消费的格式一致
func TestGenerateThumbnailBytesWorkerContract(t *testing.T) {
	initVipsTest(t)
	defer shutdownVips()

	// 造一张真实测试图（用系统图片库没有就用程序生成的最小 PNG）
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "src.png")
	makeTestPNG(t, srcPath)

	// 1. 纯 vips 生成
	jpegBytes, err := generateThumbnailBytes(srcPath, thumbMaxSize, thumbJPEGQuality)
	if err != nil {
		t.Fatalf("generateThumbnailBytes: %v", err)
	}
	if len(jpegBytes) == 0 {
		t.Fatal("生成的缩略图为空")
	}
	if _, err := jpeg.Decode(bytes.NewReader(jpegBytes)); err != nil {
		t.Fatalf("生成的不是合法 JPEG: %v", err)
	}
	t.Logf("纯生成 OK: %d 字节", len(jpegBytes))

	// 2. 模拟 worker 的 /gen handler（与 runThumbGenWorker 中完全相同的逻辑）
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
		out, err := generateThumbnailBytes(req.Path, req.MaxSize, req.Quality)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(out)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 3. 模拟主进程 generateViaWorker 的 HTTP 请求
	payload, _ := json.Marshal(map[string]interface{}{
		"path":    srcPath,
		"maxSize": thumbMaxSize,
		"quality": thumbJPEGQuality,
	})
	resp, err := http.Post(srv.URL+"/gen", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("worker 请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("worker HTTP %d", resp.StatusCode)
	}
	got := make([]byte, 0)
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	got = buf.Bytes()
	if len(got) == 0 {
		t.Fatal("worker 返回空")
	}
	if _, err := jpeg.Decode(bytes.NewReader(got)); err != nil {
		t.Fatalf("worker 返回的不是合法 JPEG: %v", err)
	}
	t.Logf("worker HTTP 契约 OK: %d 字节", len(got))

	// 4. 无效请求应返回 4xx
	bad := strings.NewReader(`{"path":"","maxSize":0,"quality":0}`)
	rb, _ := http.Post(srv.URL+"/gen", "application/json", bad)
	if rb.StatusCode < 400 {
		t.Fatalf("无效参数应返回 4xx, got %d", rb.StatusCode)
	}
	rb.Body.Close()
	t.Log("无效参数校验 OK")
}

// makeTestPNG 生成一张最小可解码 PNG
func makeTestPNG(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for x := 0; x < 64; x++ {
		for y := 0; y < 64; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 4), B: 128, A: 255})
		}
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
