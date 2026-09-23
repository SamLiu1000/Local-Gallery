package main

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"strings"
	"unicode/utf16"
)

// ==================== 音频元数据（纯 Go，无需 ffmpeg） ====================
//
// AI 音乐生成工具（ComfyUI SaveAudioAdvanced 等）把完整节点图写进 MP3 的
// ID3v2 自定义文本帧（TXXX，帧名 prompt / workflow，见分析样本实测）。
// 这里只做最小解析：读出 TXXX/COMM 文本帧，交给与视频相同的
// extractComfyMetadata 管线提取 prompt / 负面词 / 参数。
// 支持 ID3v2.3 / v2.4；v2.2（"PLS"帧名 3 字节）罕见，直接跳过。

// extractAudioTextFrames 读取 MP3 的 ID3v2 文本帧，返回 {帧名: 文本}。
func extractAudioTextFrames(filePath string) map[string]string {
	frames := make(map[string]string)
	data, err := os.ReadFile(filePath)
	if err != nil || len(data) < 10 {
		return frames
	}
	if string(data[0:3]) != "ID3" {
		return frames // 无 ID3v2（可能只有 ID3v1，跳过）
	}
	version := data[3]
	if version < 3 {
		return frames
	}
	flags := data[5]
	// synchsafe size：4 字节每字节最高位为 0
	tagSize := int(data[6]&0x7F)<<21 | int(data[7]&0x7F)<<14 | int(data[8]&0x7F)<<7 | int(data[9]&0x7F)
	pos := 10
	end := pos + tagSize
	if end > len(data) {
		end = len(data)
	}
	// 扩展头（v2.3 4字节大小 + 内容；v2.4 synchsafe 大小）
	if flags&0x40 != 0 && pos+4 <= end {
		if version == 4 {
			extSize := synchsafe(data[pos : pos+4])
			pos += extSize
		} else {
			extSize := int(binary.BigEndian.Uint32(data[pos : pos+4]))
			pos += 4 + extSize
		}
	}

	headerLen := 10
	for pos+headerLen <= end {
		id := string(data[pos : pos+4])
		if id[0] == 0 { // padding
			break
		}
		var frameSize int
		if version == 4 {
			frameSize = synchsafe(data[pos+4 : pos+8])
		} else {
			frameSize = int(binary.BigEndian.Uint32(data[pos+4 : pos+8]))
		}
		frameFlags := binary.BigEndian.Uint16(data[pos+8 : pos+10])
		pos += headerLen
		if frameSize <= 0 || pos+frameSize > end {
			break
		}
		body := data[pos : pos+frameSize]
		pos += frameSize
		// 跳过压缩/加密帧
		if frameFlags&(0x00E0) != 0 || frameFlags&(0x4000|0x8000) != 0 && version == 3 {
			continue
		}

		switch {
		case strings.HasPrefix(id, "TXXX"):
			// 编码字节 + null 结尾描述 + 值
			if len(body) < 3 {
				continue
			}
			key, value := splitID3TextPair(body)
			if key != "" {
				frames[key] = value
			}
		case id == "COMM":
			if len(body) < 5 {
				continue
			}
			// 编码 + 语言3字节 + 描述(null结尾) + 文本
			enc := body[0]
			rest := body[4:]
			descLen := id3TextTermLen(enc, rest)
			if descLen < 0 || descLen >= len(rest) {
				continue
			}
			value := decodeID3String(enc, rest[descLen:])
			if key := decodeID3String(enc, rest[:descLen]); key != "" {
				frames[key] = value
			} else if _, exists := frames["COMMENT"]; !exists {
				frames["COMMENT"] = value
			}
		case len(id) == 4 && id[0] == 'T' && id != "TXXX":
			if _, exists := frames[id]; !exists {
				if len(body) > 1 {
					frames[id] = decodeID3String(body[0], body[1:])
				}
			}
		}
	}
	return frames
}

func synchsafe(b []byte) int {
	return int(b[0]&0x7F)<<21 | int(b[1]&0x7F)<<14 | int(b[2]&0x7F)<<7 | int(b[3]&0x7F)
}

// splitID3TextPair 解析 TXXX body：编码字节 + null 结尾描述 + 值
func splitID3TextPair(body []byte) (string, string) {
	enc := body[0]
	rest := body[1:]
	termLen := id3TextTermLen(enc, rest)
	if termLen < 0 {
		return "", ""
	}
	key := decodeID3String(enc, rest[:termLen])
	value := decodeID3String(enc, rest[termLen:])
	return key, value
}

// id3TextTermLen 返回编码对应的终止符字节数位置；-1 表示找不到
func id3TextTermLen(enc byte, data []byte) int {
	if enc == 1 || enc == 2 {
		// UTF-16：BOM 开头 + 0x00 0x00 结尾（对齐 2 字节）
		for i := 0; i+1 < len(data); i += 2 {
			if data[i] == 0 && data[i+1] == 0 {
				return i + 2
			}
		}
		return -1
	}
	for i := 0; i < len(data); i++ {
		if data[i] == 0 {
			return i + 1
		}
	}
	return -1
}

func decodeID3String(enc byte, data []byte) string {
	// 去掉尾部终止符
	switch enc {
	case 1, 2:
		if len(data)%2 != 0 {
			data = data[:len(data)-len(data)%2]
		}
		u := make([]uint16, 0, len(data)/2)
		littleEndian := true
		if enc == 2 {
			littleEndian = false
		} else if len(data) >= 2 {
			if data[0] == 0xFF && data[1] == 0xFE {
				littleEndian = true
				data = data[2:]
			} else if data[0] == 0xFE && data[1] == 0xFF {
				littleEndian = false
				data = data[2:]
			}
		}
		for i := 0; i+1 < len(data); i += 2 {
			b1, b2 := data[i], data[i+1]
			if !littleEndian {
				b1, b2 = b2, b1
			}
			u = append(u, uint16(b1)|uint16(b2)<<8)
		}
		return strings.TrimRight(string(utf16.Decode(u)), "\x00")
	case 3:
		return strings.TrimRight(string(data), "\x00")
	default: // 0: ISO-8859-1，按 UTF-8 近似处理（AI 工具写的基本是 UTF-8/UTF-16）
		return strings.TrimRight(string(data), "\x00")
	}
}

// extractAudioMetadata 从音频文件提取 AI 元数据（prompt/negativePrompt/params）
func extractAudioMetadata(filePath string) map[string]interface{} {
	lower := strings.ToLower(filePath)
	var frames map[string]string
	switch {
	case strings.HasSuffix(lower, ".mp3"):
		frames = extractAudioTextFrames(filePath)
	default:
		// flac(Vorbis comment)/ogg/m4a 待后续版本，不阻塞主流程
		return nil
	}
	if len(frames) == 0 {
		return nil
	}

	// prompt 帧 → ComfyUI 节点图 JSON，与视频同一条解析管线
	// 两种形态：{"prompt": "<节点图 JSON 字符串>"}（MP4 keyed atoms）或
	// 直接就是节点图 {"35": {...class_type...}}（PNG prompt 块 / MP3 prompt 帧）。
	// 统一包成前者交给 extractComfyMetadata。
	for _, key := range []string{"prompt", "Prompt", "PROMPT"} {
		if raw, ok := frames[key]; ok {
			var jsonData map[string]interface{}
			if err := json.Unmarshal([]byte(raw), &jsonData); err == nil {
				if _, hasPrompt := jsonData["prompt"]; !hasPrompt && looksLikeNodeGraph(jsonData) {
					jsonData = map[string]interface{}{"prompt": raw}
				}
				return extractComfyMetadata(jsonData)
			}
			// 非 JSON：当作纯文本 prompt
			if len(raw) > 20 {
				return map[string]interface{}{
					"prompt":         raw,
					"negativePrompt": "",
					"params":         map[string]string{},
				}
			}
		}
	}
	// 退而求其次：COMMENT 里找
	if c, ok := frames["COMMENT"]; ok && len(c) > 20 {
		var jsonData map[string]interface{}
		if err := json.Unmarshal([]byte(c), &jsonData); err == nil {
			return extractComfyMetadata(jsonData)
		}
	}
	return nil
}

// parseAudioToLegacy 与 parseVideoToLegacy 同构：旧版 map 格式
func (a *App) parseAudioToLegacy(filePath string) map[string]interface{} {
	meta := extractAudioMetadata(filePath)
	if meta == nil {
		return nil
	}
	prompt, _ := meta["prompt"].(string)
	negativePrompt, _ := meta["negativePrompt"].(string)
	result := map[string]interface{}{
		"prompt":         prompt,
		"negativePrompt": negativePrompt,
		"params":         meta["params"],
	}
	if params, ok := meta["params"].(map[string]string); ok {
		rawMap := make(map[string]interface{}, len(params)+2)
		rawMap["prompt"] = prompt
		rawMap["negativePrompt"] = negativePrompt
		for k, v := range params {
			rawMap[k] = v
		}
		result["raw"] = rawMap
	}
	return result
}

// ==================== MP4 时长（纯 Go，无需 ffmpeg） ====================

// extractMP4DurationSec 纯 Go 解析 MP4/MOV 时长（秒）：
// 遍历顶层 box 找 moov → mvhd，读 timescale + duration。
// 只做少量 seek 读取，不加载整个文件；失败返回 0（调用方降级/由 ffprobe 增强）。
func extractMP4DurationSec(filePath string) float64 {
	f, err := os.Open(filePath)
	if err != nil {
		return 0
	}
	defer f.Close()

	buf8 := make([]byte, 8)
	readAt := func(b []byte, off int64) bool {
		_, err := f.ReadAt(b, off)
		return err == nil
	}

	// 找顶层 moov box
	var moovOffset, moovSize int64 = -1, 0
	var off int64 = 0
	for {
		if !readAt(buf8, off) {
			return 0
		}
		size := int64(binary.BigEndian.Uint32(buf8[0:4]))
		boxType := string(buf8[4:8])
		headerLen := int64(8)
		if size == 1 {
			if !readAt(buf8, off+8) {
				return 0
			}
			size = int64(binary.BigEndian.Uint64(buf8))
			headerLen = 16
		} else if size == 0 {
			break // size 到文件尾：moov 已不可能存在
		}
		if size < headerLen {
			return 0
		}
		if boxType == "moov" {
			moovOffset = off + headerLen
			moovSize = size - headerLen
			break
		}
		off += size
	}
	if moovOffset < 0 || moovSize <= 8 {
		return 0
	}

	// 在 moov 内找 mvhd
	childOff := moovOffset
	end := moovOffset + moovSize
	for childOff+8 <= end {
		if !readAt(buf8, childOff) {
			return 0
		}
		size := int64(binary.BigEndian.Uint32(buf8[0:4]))
		boxType := string(buf8[4:8])
		headerLen := int64(8)
		if size == 1 {
			if !readAt(buf8, childOff+8) {
				return 0
			}
			size = int64(binary.BigEndian.Uint64(buf8))
			headerLen = 16
		}
		if size < headerLen {
			return 0
		}
		if boxType == "mvhd" {
			hdr := make([]byte, 32)
			if !readAt(hdr, childOff+headerLen) {
				return 0
			}
			version := hdr[0]
			var timescale, duration uint64
			if version == 1 {
				if len(hdr) < 24 {
					return 0
				}
				timescale = uint64(binary.BigEndian.Uint32(hdr[20:24]))
				duration = binary.BigEndian.Uint64(hdr[24:32])
			} else {
				if len(hdr) < 20 {
					return 0
				}
				timescale = uint64(binary.BigEndian.Uint32(hdr[12:16]))
				duration = uint64(binary.BigEndian.Uint32(hdr[16:20]))
			}
			if timescale == 0 {
				return 0
			}
			return float64(duration) / float64(timescale)
		}
		childOff += size
	}
	return 0
}

// looksLikeNodeGraph 判断 map 是否直接是 ComfyUI 节点图（值为带 class_type 的节点）。
func looksLikeNodeGraph(m map[string]interface{}) bool {
	for _, v := range m {
		if node, ok := v.(map[string]interface{}); ok {
			if _, has := node["class_type"]; has {
				return true
			}
		}
	}
	return false
}
