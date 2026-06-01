package main

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func formatFileSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
}

func getMIMEType(ext string) string {
	mimeTypes := map[string]string{
		".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
		".gif": "image/gif", ".svg": "image/svg+xml", ".ico": "image/x-icon",
		".webp": "image/webp", ".bmp": "image/bmp", ".avif": "image/avif",
		".mp4": "video/mp4", ".webm": "video/webm", ".mkv": "video/x-matroska",
	}
	if mime, ok := mimeTypes[strings.ToLower(ext)]; ok {
		return mime
	}
	return "application/octet-stream"
}

func isImageFile(name string) bool {
	ext := ""
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			ext = strings.ToLower(name[i:])
			break
		}
	}
	imageExts := map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true,
		".webp": true, ".bmp": true, ".gif": true, ".avif": true, ".svg": true,
	}
	return imageExts[ext]
}

func isVideoFile(name string) bool {
	ext := ""
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			ext = strings.ToLower(name[i:])
			break
		}
	}
	videoExts := map[string]bool{
		".mp4": true, ".webm": true, ".mkv": true,
	}
	return videoExts[ext]
}

func isMediaFile(name string) bool {
	return isImageFile(name) || isVideoFile(name)
}

func generateStableID(filePath string, fileSize int64, fileModified int64) string {
	data := fmt.Sprintf("%s::%d::%d", filePath, fileSize, fileModified)
	hash := md5.Sum([]byte(data))
	return hex.EncodeToString(hash[:])
}

func generateUUID() string {
	bytes := make([]byte, 16)
	rand.Read(bytes)
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(bytes[0:4]),
		hex.EncodeToString(bytes[4:6]),
		hex.EncodeToString(bytes[6:8]),
		hex.EncodeToString(bytes[8:10]),
		hex.EncodeToString(bytes[10:16]),
	)
}

func generateCacheKeyStr(filePath string, fileSize, fileModified int64) string {
	data := fmt.Sprintf("%s::%d::%d", filePath, fileSize, fileModified)
	hash := md5.Sum([]byte(data))
	return fmt.Sprintf("%x.json", hash)
}

func extractPNGTextParams(data []byte) map[string]string {
	params := make(map[string]string)
	content := string(data)
	if idx := strings.Index(content, `"prompt"`); idx >= 0 {
		start := idx
		depth := 0
		inString := false
		escaped := false
		end := start
		for i := start; i < len(content); i++ {
			c := content[i]
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = !inString
			}
			if !inString {
				if c == '{' {
					depth++
				} else if c == '}' {
					depth--
					if depth == 0 {
						end = i + 1
						break
					}
				}
			}
		}
		if end > start {
			jsonStr := content[start:end]
			var parsed map[string]interface{}
			if err := json.Unmarshal([]byte(jsonStr), &parsed); err == nil {
				for k, v := range parsed {
					if s, ok := v.(string); ok {
						params[k] = s
					}
				}
			}
		}
	}
	return params
}

// ==================== MP4 视频元数据提取 ====================

// extractMP4Comment 从 MP4 文件中提取 moov/udta/©cmt 中的 comment 标签
// 不依赖 ffprobe，直接解析 ISO BMFF box 结构
func extractMP4Comment(filePath string) string {
	data, err := os.ReadFile(filePath)
	if err != nil || len(data) < 8 {
		return ""
	}
	pos := 0
	for pos+8 <= len(data) {
		size := binary.BigEndian.Uint32(data[pos : pos+4])
		if size < 8 {
			break
		}
		boxType := string(data[pos+4 : pos+8])
		if boxType == "moov" {
			moovEnd := pos + int(size)
			if moovEnd > len(data) {
				moovEnd = len(data)
			}
			return findUDTAComment(data, pos+8, moovEnd)
		}
		pos += int(size)
	}
	return ""
}

func findUDTAComment(data []byte, start, end int) string {
	pos := start
	for pos+8 <= end {
		size := binary.BigEndian.Uint32(data[pos : pos+4])
		if size < 8 {
			break
		}
		boxType := string(data[pos+4 : pos+8])
		childStart := pos + 8
		childEnd := pos + int(size)
		if childEnd > end {
			childEnd = end
		}

		switch boxType {
		case "udta":
			return findUDTAComment(data, childStart, childEnd)
		case "meta":
			return findUDTAComment(data, childStart+4, childEnd)
		case "ilst":
			return findUDTAComment(data, childStart, childEnd)
		default:
			// ©cmt: 0xA9 0x63 0x6D 0x74
			if boxType == "\xa9cmt" || boxType == "©cmt" {
				return readMP4DataAtom(data, childStart, childEnd)
			}
		}
		pos += int(size)
	}
	return ""
}

func readMP4DataAtom(data []byte, start, end int) string {
	pos := start
	for pos+8 <= end {
		size := binary.BigEndian.Uint32(data[pos : pos+4])
		if size < 8 {
			break
		}
		boxType := string(data[pos+4 : pos+8])
		if boxType == "data" {
			valStart := pos + 16
			if valStart < end {
				valEnd := valStart
				for valEnd < end && data[valEnd] != 0 {
					valEnd++
				}
				return string(data[valStart:valEnd])
			}
		}
		pos += int(size)
	}
	contentEnd := start
	for contentEnd < end && data[contentEnd] != 0 {
		contentEnd++
	}
	if contentEnd > start {
		return string(data[start:contentEnd])
	}
	return ""
}

// extractVideoMetadata 从视频文件提取 AI 元数据（prompt/negative_prompt/params）
func extractVideoMetadata(filePath string) map[string]interface{} {
	ext := ""
	for i := len(filePath) - 1; i >= 0; i-- {
		if filePath[i] == '.' {
			ext = strings.ToLower(filePath[i:])
			break
		}
	}

	var comment string
	switch ext {
	case ".mp4":
		comment = extractMP4Comment(filePath)
	case ".webm", ".mkv":
		comment = extractWebMComment(filePath)
	}

	if comment == "" {
		return nil
	}

	var jsonData map[string]interface{}
	if err := json.Unmarshal([]byte(comment), &jsonData); err != nil {
		if len(comment) > 20 {
			return map[string]interface{}{
				"prompt":         comment,
				"negativePrompt": "",
				"params":         map[string]string{},
			}
		}
		return nil
	}

	return extractComfyMetadata(jsonData)
}

func extractWebMComment(filePath string) string {
	data, err := os.ReadFile(filePath)
	if err != nil || len(data) < 4 {
		return ""
	}
	content := string(data)
	if idx := strings.Index(content, `"prompt"`); idx >= 0 {
		start := strings.LastIndex(content[:idx+1], "{")
		if start < 0 {
			return ""
		}
		depth := 0
		inString := false
		escaped := false
		for i := start; i < len(content); i++ {
			ch := content[i]
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = !inString
				continue
			}
			if inString {
				continue
			}
			if ch == '{' {
				depth++
			} else if ch == '}' {
				depth--
				if depth == 0 {
					return content[start : i+1]
				}
			}
		}
	}
	return ""
}

func extractComfyMetadata(jsonData map[string]interface{}) map[string]interface{} {
	result := map[string]interface{}{
		"prompt":         "",
		"negativePrompt": "",
		"params":         map[string]string{},
	}

	promptStr := ""
	if p, ok := jsonData["prompt"].(string); ok {
		promptStr = p
	}

	var nodes map[string]interface{}

	if promptStr != "" {
		var workflow map[string]interface{}
		if err := json.Unmarshal([]byte(promptStr), &workflow); err == nil {
			nodes = workflow
		}
	}

	if nodes == nil {
		nodes = findNodes(jsonData)
	}

	if nodes == nil {
		return nil
	}

	var positiveText, negativeText string
	rawParams := make(map[string]string)
	var loraNames []string

	for _, rawNode := range nodes {
		node, ok := rawNode.(map[string]interface{})
		if !ok {
			continue
		}
		classType, _ := node["class_type"].(string)
		inputs, _ := node["inputs"].(map[string]interface{})

		switch classType {
		case "CLIPTextEncode":
			meta, _ := node["_meta"].(map[string]interface{})
			title, _ := meta["title"].(string)
			text, _ := inputs["text"].(string)
			if text != "" {
				titleLower := strings.ToLower(title)
				if strings.Contains(titleLower, "negative") {
					negativeText = text
				} else if strings.Contains(titleLower, "positive") {
					positiveText = text
				} else if positiveText == "" {
					positiveText = text
				}
			}

		case "KSampler", "KSamplerAdvanced":
			if v, ok := inputs["steps"]; ok {
				rawParams["Steps"] = formatIntParam(v)
			}
			if v, ok := inputs["cfg"]; ok {
				rawParams["CFG Scale"] = formatFloatParam(v)
			}
			if v, ok := inputs["sampler_name"]; ok {
				rawParams["Sampler"] = fmt.Sprintf("%v", v)
			}
			if v, ok := inputs["scheduler"]; ok {
				rawParams["Scheduler"] = fmt.Sprintf("%v", v)
			}
			if v, ok := inputs["noise_seed"]; ok {
				rawParams["Seed"] = formatIntParam(v)
			}
			if v, ok := inputs["distilled_cfg"]; ok {
				rawParams["Distilled CFG Scale"] = fmt.Sprintf("%v", v)
			}

		case "EmptyLatentImage":
			if w, ok := inputs["width"]; ok {
				rawParams["Width"] = fmt.Sprintf("%v", w)
			}
			if h, ok := inputs["height"]; ok {
				rawParams["Height"] = fmt.Sprintf("%v", h)
			}

		case "WanImageToVideo":
			if w, ok := inputs["width"]; ok {
				rawParams["Width"] = fmt.Sprintf("%v", w)
			}
			if h, ok := inputs["height"]; ok {
				rawParams["Height"] = fmt.Sprintf("%v", h)
			}

		case "CheckpointLoaderSimple", "CheckpointLoader":
			if v, ok := inputs["ckpt_name"]; ok {
				modelName := fmt.Sprintf("%v", v)
				rawParams["Model"] = filepathBase(modelName)
			}

		case "UnetLoaderGGUF":
			if v, ok := inputs["unet_name"]; ok {
				modelName := fmt.Sprintf("%v", v)
				rawParams["Model"] = filepathBase(modelName)
			}

		case "CLIPLoader":
			if v, ok := inputs["clip_name"]; ok && rawParams["CLIP"] == "" {
				rawParams["CLIP"] = filepathBase(fmt.Sprintf("%v", v))
			}

		case "VAELoader":
			if v, ok := inputs["vae_name"]; ok {
				rawParams["VAE"] = filepathBase(fmt.Sprintf("%v", v))
			}

		case "LoraLoader", "LoraLoaderModelOnly":
			if v, ok := inputs["lora_name"]; ok {
				name := filepathBase(fmt.Sprintf("%v", v))
				if strength, ok2 := inputs["strength_model"]; ok2 {
					loraNames = append(loraNames, fmt.Sprintf("%s:%v", name, strength))
				} else {
					loraNames = append(loraNames, name)
				}
			}

		case "Power Lora Loader (rgthree)":
			// rgthree's multi-lora loader: lora_1, lora_2, ... as nested objects
			for k, v := range inputs {
				if !strings.HasPrefix(k, "lora_") {
					continue
				}
				loraObj, ok2 := v.(map[string]interface{})
				if !ok2 {
					continue
				}
				on, _ := loraObj["on"].(bool)
				if !on {
					continue
				}
				loraPath, _ := loraObj["lora"].(string)
				if loraPath == "" {
					continue
				}
				name := filepathBase(loraPath)
				strength := ""
				if s, ok3 := loraObj["strength"]; ok3 {
					strength = fmt.Sprintf(":%v", s)
				}
				loraNames = append(loraNames, name+strength)
			}

		case "UpscaleModelLoader":
			if v, ok := inputs["model_name"]; ok {
				rawParams["Upscaler"] = filepathBase(fmt.Sprintf("%v", v))
			}

		case "ControlNetLoader":
			if v, ok := inputs["control_net_name"]; ok {
				rawParams["ControlNet"] = filepathBase(fmt.Sprintf("%v", v))
			}
		}
	}

	if positiveText == "" && negativeText == "" {
		return nil
	}

	// 合并 Width+Height -> Size
	if w, ok := rawParams["Width"]; ok {
		if h, ok2 := rawParams["Height"]; ok2 {
			rawParams["Size"] = w + "x" + h
			delete(rawParams, "Width")
			delete(rawParams, "Height")
		}
	}

	if len(loraNames) > 0 {
		rawParams["LoRA"] = strings.Join(loraNames, ", ")
	}

	result["prompt"] = positiveText
	result["negativePrompt"] = negativeText
	if len(rawParams) > 0 {
		result["params"] = rawParams
	}

	return result
}

func formatIntParam(v interface{}) string {
	switch val := v.(type) {
	case float64:
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%v", val)
	case int, int64:
		return fmt.Sprintf("%d", val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func formatFloatParam(v interface{}) string {
	switch val := v.(type) {
	case float64:
		return fmt.Sprintf("%.1f", val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func filepathBase(path string) string {
	// Handle both / and \\ separators
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[i+1:]
		}
	}
	return path
}

func findNodes(obj interface{}) map[string]interface{} {
	switch v := obj.(type) {
	case map[string]interface{}:
		if _, hasClassType := v["class_type"]; hasClassType {
			// 检查子元素是否是节点图
			for _, val := range v {
				if isNodeMap(val) {
					return v
				}
			}
		}
		for _, val := range v {
			if result := findNodes(val); result != nil {
				return result
			}
		}
	}
	return nil
}

func isNodeMap(val interface{}) bool {
	m, ok := val.(map[string]interface{})
	if !ok {
		return false
	}
	_, hasClassType := m["class_type"]
	return hasClassType
}
