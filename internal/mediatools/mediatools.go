// Package mediatools 封装可选外置 ffmpeg/ffprobe 组件的发现与调用。
//
// ffmpeg 是纯增强组件：未检测到时调用方必须能回退到纯 Go 实现
// （视频缩略图黑占位、时长等仅部分格式可解析），不得报错中断。
//
// 发现顺序（与 docs/media-components.md 的契约一致）：
//  1. exe 同级 ffmpeg/ 目录
//  2. 工作目录 ffmpeg/ 目录（开发模式）
//  3. 系统 PATH
//  4. 用户在设置中手动指定的路径
package mediatools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Tools 一次检测结果。任何一个路径为空即表示该能力不可用。
type Tools struct {
	FFmpegPath   string `json:"ffmpegPath"`
	FFprobePath  string `json:"ffprobePath"`
	Source       string `json:"source"` // exe_dir | work_dir | path | user_config | not_found
	Version      string `json:"version"`
	FFmpegFound  bool   `json:"ffmpegFound"`
	FFprobeFound bool   `json:"ffprobeFound"`
}

var (
	mu        sync.RWMutex
	cached    *Tools
	userPath  string
	detected  bool
	lookupErr error
)

// SetUserPath 更新用户手动指定的 ffmpeg 目录（含 ffmpeg.exe 的目录或其父目录均可），
// 并使缓存失效。传空串表示清除用户配置。
func SetUserPath(p string) {
	mu.Lock()
	defer mu.Unlock()
	userPath = strings.TrimSpace(p)
	cached = nil
	detected = false
}

// GetUserPath 返回当前用户手动配置的路径。
func GetUserPath() string {
	mu.RLock()
	defer mu.RUnlock()
	return userPath
}

// Detect 返回（带缓存的）检测结果。
func Detect() *Tools {
	mu.Lock()
	defer mu.Unlock()
	if detected {
		return cached
	}
	lookupErr = nil
	cached = detectLocked()
	detected = true
	if cached.FFmpegFound {
		fmt.Printf("[mediatools] ffmpeg 已就绪: %s (%s)\n", cached.FFmpegPath, cached.Version)
	} else if lookupErr != nil {
		fmt.Printf("[mediatools] ffmpeg 不可用: %v\n", lookupErr)
	}
	return cached
}

// Redetect 清缓存并重新检测（设置页"重新检测"按钮）。
func Redetect() *Tools {
	mu.Lock()
	cached = nil
	detected = false
	mu.Unlock()
	return Detect()
}

func detectLocked() *Tools {
	t := &Tools{}
	var ffmpegCandidates []struct{ path, source string }
	var ffprobeCandidates []struct{ path, source string }

	appendDir := func(dir, source string) {
		if dir == "" {
			return
		}
		if p := findBinary(dir, "ffmpeg"); p != "" {
			ffmpegCandidates = append(ffmpegCandidates, struct{ path, source string }{p, source})
		}
		if p := findBinary(dir, "ffprobe"); p != "" {
			ffprobeCandidates = append(ffprobeCandidates, struct{ path, source string }{p, source})
		}
	}

	// 4. 用户配置优先级最高（用户明确指定的目录，覆盖自动发现）
	if userPath != "" {
		appendDir(normalizeDir(userPath), "user_config")
	}
	// 1/2. exe 同级与工作目录
	appendDir(filepath.Join(exeDir(), "ffmpeg"), "exe_dir")
	if wd, err := os.Getwd(); err == nil {
		appendDir(filepath.Join(wd, "ffmpeg"), "work_dir")
	}
	// 3. PATH
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		ffmpegCandidates = append(ffmpegCandidates, struct{ path, source string }{p, "path"})
	}
	if p, err := exec.LookPath("ffprobe"); err == nil {
		ffprobeCandidates = append(ffprobeCandidates, struct{ path, source string }{p, "path"})
	}

	// 候选按发现顺序取第一个可用者，并保证 ffmpeg/ffprobe 来自同一能力集时版本一致：
	// 简化处理——ffmpeg 与 ffprobe 各自独立取最优候选（跨目录混用仅影响信息增强，无风险）。
	if len(ffmpegCandidates) > 0 {
		t.FFmpegPath = ffmpegCandidates[0].path
		t.Source = ffmpegCandidates[0].source
		t.FFmpegFound = true
	}
	if len(ffprobeCandidates) > 0 {
		t.FFprobePath = ffprobeCandidates[0].path
		t.FFprobeFound = true
	}
	if t.Source == "" && t.FFprobeFound {
		t.Source = "path"
	}
	if t.FFmpegFound {
		t.Version = ffmpegVersion(t.FFmpegPath)
	}
	return t
}

// normalizeDir 接受"目录"或"指向 ffmpeg.exe/ffprobe.exe 的文件路径"两种形式。
func normalizeDir(p string) string {
	if p == "" {
		return ""
	}
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return filepath.Dir(p)
	}
	return p
}

func findBinary(dir, name string) string {
	p := filepath.Join(dir, exeName(name))
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	return ""
}

func exeName(name string) string {
	if os.PathSeparator == '\\' {
		return name + ".exe"
	}
	return name
}

func exeDir() string {
	if p, err := os.Executable(); err == nil {
		return filepath.Dir(p)
	}
	return ""
}

func ffmpegVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-version")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	line := strings.SplitN(string(out), "\n", 2)
	if len(line) == 0 {
		return ""
	}
	fields := strings.Fields(line[0])
	if len(fields) >= 3 {
		return strings.Join(fields[0:3], " ")
	}
	return strings.TrimSpace(line[0])
}

// ExtractVideoFrame 用 ffmpeg 抽取视频单帧，返回 JPEG 字节。
// maxSize 限制输出最长边（像素），quality 为 JPEG 质量 2-31（值越小质量越高）。
// 定位策略：优先在 1s 处取帧；若视频不足 1s（定位越界产不出帧）则回退 0s。
// 超时 15s；调用方须自行通过限流信号量控制并发。
func ExtractVideoFrame(tools *Tools, srcPath string, maxSize, quality int) ([]byte, error) {
	if tools == nil || !tools.FFmpegFound {
		return nil, errors.New("ffmpeg 不可用")
	}
	if maxSize <= 0 {
		maxSize = 500
	}
	if quality <= 0 {
		quality = 3
	}

	tmp, err := os.CreateTemp("", "lg-frame-*.jpg")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	filter := fmt.Sprintf("scale='min(iw,%d)':'min(ih,%d)':force_original_aspect_ratio=decrease", maxSize, maxSize)
	var lastErr error
	for _, seek := range []string{"1", "0"} {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cmd := exec.CommandContext(ctx, tools.FFmpegPath,
			"-hide_banner", "-loglevel", "error",
			"-an", "-sn", "-dn",
			"-ss", seek, "-i", srcPath,
			"-frames:v", "1", "-q:v", fmt.Sprint(quality),
			"-vf", filter, "-y", tmpPath)
		hideWindow(cmd)
		err := cmd.Run()
		cancel()
		if err == nil {
			if data, statErr := os.ReadFile(tmpPath); statErr == nil && len(data) > 0 {
				return data, nil
			}
			lastErr = fmt.Errorf("ffmpeg 未产出帧 (seek=%s)", seek)
			continue
		}
		lastErr = err
	}
	return nil, fmt.Errorf("视频抽帧失败: %w", lastErr)
}

// ProbeResult ffprobe -show_format -show_streams 的结果（仅保留关心的字段）。
// ffprobe 的数值字段可能是字符串或数字（不同字段不一致），因此手动转换。
type ProbeResult struct {
	Format struct {
		DurationSec float64
		BitRate     int64
		FormatName  string
		Tags        map[string]string
	}
	Streams []ProbeStream
}

type ProbeStream struct {
	CodecType  string
	CodecName  string
	Width      int
	Height     int
	Duration   float64
	BitRate    int64
	SampleRate int
	Channels   int
}

// Probe 调用 ffprobe 读取媒体技术信息。ffprobe 不可用时返回错误（调用方降级）。
// 超时 10s；用于"详细信息增强"，不应进入热路径。
func Probe(tools *Tools, srcPath string) (*ProbeResult, error) {
	if tools == nil || !tools.FFprobeFound {
		return nil, errors.New("ffprobe 不可用")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	probeCmd := exec.CommandContext(ctx, tools.FFprobePath,
		"-v", "error", "-show_format", "-show_streams", "-of", "json", srcPath)
	hideWindow(probeCmd)
	out, err := probeCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe 失败: %w", err)
	}
	var raw struct {
		Format  map[string]interface{}   `json:"format"`
		Streams []map[string]interface{} `json:"streams"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("ffprobe 输出解析失败: %w", err)
	}

	r := &ProbeResult{}
	if raw.Format != nil {
		r.Format.DurationSec = toFloat(raw.Format["duration"])
		r.Format.BitRate = toInt64(raw.Format["bit_rate"])
		r.Format.FormatName, _ = raw.Format["format_name"].(string)
		if tags, ok := raw.Format["tags"].(map[string]interface{}); ok {
			r.Format.Tags = make(map[string]string, len(tags))
			for k, v := range tags {
				r.Format.Tags[k], _ = v.(string)
			}
		}
	}
	for _, st := range raw.Streams {
		var ps ProbeStream
		ps.CodecType, _ = st["codec_type"].(string)
		ps.CodecName, _ = st["codec_name"].(string)
		ps.Width = int(toFloat(st["width"]))
		ps.Height = int(toFloat(st["height"]))
		ps.Duration = toFloat(st["duration"])
		ps.BitRate = toInt64(st["bit_rate"])
		ps.SampleRate = int(toFloat(st["sample_rate"]))
		ps.Channels = int(toFloat(st["channels"]))
		r.Streams = append(r.Streams, ps)
	}
	return r, nil
}

func toFloat(v interface{}) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	default:
		return 0
	}
}

func toInt64(v interface{}) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	default:
		return 0
	}
}
