package main

import (
	"encoding/json"
	"fmt"
	"runtime"

	"local-gallery/internal/mediatools"
)

// ==================== 媒体组件（ffmpeg/ffprobe）设置与状态 ====================
//
// ffmpeg 是可选增强组件，相关契约见 docs/media-components.md：
// 发现顺序 exe 旁 ffmpeg/ → 工作目录 ffmpeg/ → PATH → 用户配置；
// 未检测到时视频缩略图降级黑色占位、技术信息仅纯 Go 解析，不报错。

// init 启动时后台预检测一次 ffmpeg，检测结果进程内缓存，
// 首个视频缩略图请求即可命中（避免首次浏览延迟）。
func init() {
	go mediatools.Detect()
}

// GetMediaToolsStatus 返回 ffmpeg/ffprobe 检测状态（设置页"媒体组件"区块）。
func (a *App) GetMediaToolsStatus() map[string]interface{} {
	tools := mediatools.Detect()
	return map[string]interface{}{
		"ffmpegFound":  tools.FFmpegFound,
		"ffprobeFound": tools.FFprobeFound,
		"ffmpegPath":   tools.FFmpegPath,
		"ffprobePath":  tools.FFprobePath,
		"source":       tools.Source,
		"version":      tools.Version,
		"userPath":     mediatools.GetUserPath(),
		"enhanced":     tools.FFmpegFound, // 一句话结论：增强能力是否已启用
	}
}

// SetMediaToolsPath 保存用户手动指定的 ffmpeg 目录（空串清除）并立即重新检测。
func (a *App) SetMediaToolsPath(p string) map[string]interface{} {
	mediatools.SetUserPath(p)
	current := readGlobalSettings()
	current["mediaToolsPath"] = p
	if err := writeGlobalSettings(current); err != nil {
		return map[string]interface{}{"success": false, "message": "设置保存失败: " + err.Error()}
	}
	tools := mediatools.Redetect()
	return map[string]interface{}{
		"success":      true,
		"ffmpegFound":  tools.FFmpegFound,
		"ffprobeFound": tools.FFprobeFound,
		"version":      tools.Version,
	}
}

// RedetectMediaTools 重新检测（用户安装 ffmpeg 后无需重启即可启用）。
func (a *App) RedetectMediaTools() map[string]interface{} {
	tools := mediatools.Redetect()
	return map[string]interface{}{
		"success":      true,
		"ffmpegFound":  tools.FFmpegFound,
		"ffprobeFound": tools.FFprobeFound,
		"version":      tools.Version,
		"source":       tools.Source,
	}
}

// loadMediaToolsSettings 启动时恢复用户手动配置的 ffmpeg 路径。
func (a *App) loadMediaToolsSettings() {
	if p, ok := readGlobalSettings()["mediaToolsPath"].(string); ok && p != "" {
		mediatools.SetUserPath(p)
	}
}

// guardMediaPanic 媒体链路 panic 防护：捕获堆栈并写入日志，避免 Wails 绑定
// panic 导致整个程序退出（点击音频曾崩溃，此守卫保证留下定位信息）。
func guardMediaPanic(name string) {
	if r := recover(); r != nil {
		buf := make([]byte, 8192)
		n := runtime.Stack(buf, false)
		fmt.Printf("[媒体组件][PANIC] %s: %v\n%s\n", name, r, buf[:n])
	}
}

// GetMediaInfo 返回指定媒体文件的时长与技术信息（详情页按需调用）。
// 策略：duration_ms 优先来自扫描期纯 Go 解析（mp4）；media_info_json 为空且
// ffprobe 可用时现场探测一次并写库缓存，之后直接复用。ffprobe 不可用时
// 只返回已有信息，不报错（纯增强契约）。
func (a *App) GetMediaInfo(id string) map[string]interface{} {
	defer guardMediaPanic("GetMediaInfo")
	if a.imageDB == nil {
		return map[string]interface{}{"found": false}
	}
	durMS, infoJSON, ok := a.imageDB.GetMediaInfoRow(id)
	if !ok {
		return map[string]interface{}{"found": false}
	}
	if infoJSON == "" {
		tools := mediatools.Detect()
		if tools.FFprobeFound {
			if path := a.imageDB.GetImagePathForID(id); path != "" {
				if probe, err := mediatools.Probe(tools, path); err == nil {
					// 组装精简信息：主视频/音频流优先
					info := map[string]interface{}{
						"format": probe.Format.FormatName,
					}
					if probe.Format.DurationSec > 0 {
						info["durationSec"] = probe.Format.DurationSec
						if durMS == 0 {
							durMS = int64(probe.Format.DurationSec * 1000)
						}
					}
					if probe.Format.BitRate > 0 {
						info["bitRate"] = probe.Format.BitRate
					}
					for _, st := range probe.Streams {
						switch st.CodecType {
						case "video":
							info["videoCodec"] = st.CodecName
							if st.Width > 0 {
								info["width"] = st.Width
								info["height"] = st.Height
							}
						case "audio":
							info["audioCodec"] = st.CodecName
							if st.SampleRate > 0 {
								info["sampleRate"] = st.SampleRate
							}
							if st.Channels > 0 {
								info["channels"] = st.Channels
							}
						}
					}
					if b, err := json.Marshal(info); err == nil {
						infoJSON = string(b)
						a.imageDB.UpdateMediaInfoRow(id, durMS, infoJSON)
					}
				}
			}
		}
	}
	return map[string]interface{}{
		"found":      true,
		"durationMs": durMS,
		"infoJson":   infoJSON,
	}
}
