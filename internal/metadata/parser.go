package metadata

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strings"

	"github.com/rwcarlsen/goexif/exif"
)

// dataItem 数据池项
type dataItem struct {
	source string
	key    string
	value  string
}

// ImageMetadata 图片元数据
type ImageMetadata struct {
	Prompt         string            `json:"prompt"`
	NegativePrompt string            `json:"negativePrompt"`
	Params         map[string]string `json:"params"`
	Raw            map[string]string `json:"raw"`
}

// ParseFile 从字节数据解析元数据
func ParseFile(data []byte, filename string) *ImageMetadata {
	ext := ""
	for i := len(filename) - 1; i >= 0; i-- {
		if filename[i] == '.' {
			ext = strings.ToLower(filename[i:])
			break
		}
	}

	switch ext {
	case ".png":
		return parsePNG(data)
	case ".jpg", ".jpeg":
		result := parseJPEG(data)
		if exifTags := extractExifFromBytes(data); exifTags != nil {
			if result.Raw == nil {
				result.Raw = make(map[string]string)
			}
			for k, v := range exifTags {
				result.Raw[k] = v
			}
		}
		return result
	case ".webp":
		return parseWebP(data)
	default:
		return createEmptyResult()
	}
}

// ==================== PNG 解析 ====================

func parsePNG(data []byte) *ImageMetadata {
	if len(data) < 8 {
		return createEmptyResult()
	}
	// 验证 PNG 签名
	sig := []byte{137, 80, 78, 71, 13, 10, 26, 10}
	if !bytes.Equal(data[:8], sig) {
		return createEmptyResult()
	}

	textChunks := make(map[string]string)
	offset := 8

	for offset+8 <= len(data) {
		length := binary.BigEndian.Uint32(data[offset : offset+4])
		chunkType := string(data[offset+4 : offset+8])

		if offset+8+int(length) > len(data) {
			break
		}

		chunkData := data[offset+8 : offset+8+int(length)]

		switch chunkType {
		case "tEXt":
			nullIdx := bytes.IndexByte(chunkData, 0)
			if nullIdx > 0 {
				key := string(chunkData[:nullIdx])
				value := string(chunkData[nullIdx+1:])
				textChunks[key] = value
			}
		case "iTXt":
			nullIdx := bytes.IndexByte(chunkData, 0)
			if nullIdx > 0 {
				key := string(chunkData[:nullIdx])
				pos := nullIdx + 1
				if pos < len(chunkData) {
					compressionFlag := chunkData[pos]
					pos++
					// 跳过 language_tag
					for pos < len(chunkData) && chunkData[pos] != 0 {
						pos++
					}
					pos++ // 跳过 \0
					// 跳过 translated_keyword
					for pos < len(chunkData) && chunkData[pos] != 0 {
						pos++
					}
					pos++ // 跳过 \0
					// 跳过额外的 \0
					for pos < len(chunkData) && chunkData[pos] == 0 {
						pos++
					}

					if compressionFlag == 1 && pos < len(chunkData) {
						// 压缩数据
						decompressed, err := inflateZlib(chunkData[pos:])
						if err == nil {
							textChunks[key] = string(decompressed)
						} else {
							textChunks[key] = "[compressed]"
						}
					} else if pos < len(chunkData) {
						textChunks[key] = string(chunkData[pos:])
					}
				}
			}
		case "zTXt":
			nullIdx := bytes.IndexByte(chunkData, 0)
			if nullIdx > 0 {
				key := string(chunkData[:nullIdx])
				dataStart := nullIdx + 2 // +1 for \0, +1 for compression_method
				if dataStart < len(chunkData) {
					decompressed, err := inflateZlib(chunkData[dataStart:])
					if err == nil {
						textChunks[key] = string(decompressed)
					} else {
						textChunks[key] = "[compressed]"
					}
				}
			}
		}

		if chunkType == "IEND" {
			break
		}
		offset += 12 + int(length)
	}

	return universalParse(textChunks)
}

// ==================== JPEG 解析 ====================

func parseJPEG(data []byte) *ImageMetadata {
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		return createEmptyResult()
	}

	textChunks := make(map[string]string)
	offset := 2

	for offset+1 < len(data) {
		if data[offset] != 0xFF {
			break
		}
		marker := binary.BigEndian.Uint16(data[offset : offset+2])

		if marker == 0xFFE1 {
			// EXIF APP1
			if offset+4 < len(data) {
				length := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
				if offset+2+length <= len(data) {
					exifData := data[offset+4 : offset+2+length]
					exifText := extractEXIFText(exifData)
					if exifText != "" {
						textChunks["exif"] = exifText
					}
				}
				offset += 2 + length
			} else {
				break
			}
		} else if marker == 0xFFFE {
			// COM
			if offset+4 < len(data) {
				length := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
				if offset+2+length <= len(data) {
					textChunks["Comment"] = string(data[offset+4 : offset+2+length])
				}
				offset += 2 + length
			} else {
				break
			}
		} else if marker == 0xFFDA {
			// SOS
			break
		} else if (marker >= 0xFFE0 && marker <= 0xFFEF) ||
			marker == 0xFFDB || marker == 0xFFC4 ||
			marker == 0xFFC0 || marker == 0xFFC2 {
			if offset+4 < len(data) {
				length := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
				offset += 2 + length
			} else {
				break
			}
		} else {
			offset += 2
		}
	}

	if len(textChunks) == 0 {
		return createEmptyResult()
	}
	return universalParse(textChunks)
}

func extractEXIFText(data []byte) string {
	// 尝试找 JSON
	str := string(data)
	jsonStart := strings.Index(str, "{")
	if jsonStart >= 0 {
		jsonEnd := strings.LastIndex(str, "}")
		if jsonEnd > jsonStart {
			candidate := str[jsonStart : jsonEnd+1]
			if json.Valid([]byte(candidate)) {
				return candidate
			}
		}
	}

	// 尝试找 "parameters" 模式
	paramIdx := strings.Index(str, "parameters")
	if paramIdx >= 0 {
		after := str[paramIdx+10:]
		// 清理 null 字节
		after = strings.ReplaceAll(after, "\x00", " ")
		after = strings.TrimSpace(after)
		if after != "" {
			return after
		}
	}

	// 清理 null 字节
	cleaned := strings.ReplaceAll(str, "\x00", " ")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned != "" {
		return cleaned
	}
	return ""
}

// ==================== WebP 解析 ====================

func parseWebP(data []byte) *ImageMetadata {
	if len(data) < 12 {
		return createEmptyResult()
	}
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return createEmptyResult()
	}

	textChunks := make(map[string]string)
	offset := 12

	for offset+8 <= len(data) {
		chunkType := string(data[offset : offset+4])
		chunkSize := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))

		if offset+8+chunkSize > len(data) {
			break
		}

		if chunkType == "EXIF" || chunkType == "XMP " {
			textChunks[chunkType] = string(data[offset+8 : offset+8+chunkSize])
		}

		offset += 8 + chunkSize
		if chunkSize%2 != 0 {
			offset++
		}
	}

	if len(textChunks) == 0 {
		return createEmptyResult()
	}
	return universalParse(textChunks)
}

// ==================== zlib 解压 ====================

func inflateZlib(data []byte) ([]byte, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("data too short")
	}

	// 去掉 zlib header (2 bytes) 和 adler32 (4 bytes)
	rawData := data
	if data[0] == 0x78 && len(data) > 6 {
		rawData = data[2 : len(data)-4]
	}

	r, err := zlib.NewReader(bytes.NewReader(rawData))
	if err != nil {
		// 尝试直接 deflate
		r, err = zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
	}
	defer r.Close()

	return io.ReadAll(r)
}

// ==================== 通用语义解析 ====================

// 参数名标准化映射表
var paramAliases = map[string][]string{
	"Steps":               {"steps", "step", "num_steps", "num_inference_steps", "Steps", "sampling_steps", "n_steps", "iterations"},
	"CFG Scale":           {"cfg", "cfg_scale", "cfg scale", "guidance", "guidance_scale", "classifier_free_guidance", "CFG Scale", "cfg-scale", "cfgScale"},
	"Sampler":             {"sampler", "sampler_name", "samplerName", "sample", "sample_method", "sampling_method", "scheduler_type", "Sampler"},
	"Scheduler":           {"scheduler", "scheduler_name", "schedulerName", "noise_scheduler", "Scheduler"},
	"Seed":                {"seed", "Seed", "noise_seed", "random_seed", "rand_seed", "initial_seed", "global_seed"},
	"Size":                {"size", "Size", "resolution", "image_size", "output_size", "dimensions"},
	"Width":               {"width", "w", "image_width", "img_width", "output_width", "Width", "W", "latent_width"},
	"Height":              {"height", "h", "image_height", "img_height", "output_height", "Height", "H", "latent_height"},
	"Model hash":          {"model hash", "model_hash", "modelHash", "Model hash", "checkpoint_hash", "ckpt_hash", "sd_hash", "sd_model_hash", "model_sha256"},
	"Model":               {"model", "model_name", "modelName", "checkpoint", "ckpt_name", "ckpt", "base_model", "sd_model", "sd_checkpoint", "unet_name", "diffusion_model", "Model", "model_id"},
	"VAE":                 {"vae", "vae_name", "vaeName", "VAE", "vae_model"},
	"CLIP":                {"clip", "clip_name", "clipName", "CLIP", "text_encoder", "clip_model", "clip_skip", "clipSkip"},
	"LoRA":                {"lora", "lora_name", "loraName", "LoRA", "lora_model", "lora_weight", "lora_strength", "lycoris", "locon", "lora_hashes"},
	"ControlNet":          {"controlnet", "control_net", "controlNet", "ControlNet", "cn_model"},
	"Denoise":             {"denoise", "denoising", "denoising_strength", "Denoise", "denoise_strength"},
	"Batch Size":          {"batch", "batch_size", "batchSize", "n_iter", "batch_count"},
	"Upscaler":            {"upscaler", "upscale", "upscale_model", "upscaler_name", "Upscaler", "hr_upscaler"},
	"Hires Fix":           {"hires", "hires_fix", "hiresFix", "highres", "highres_fix", "enable_hr"},
	"Clip Skip":           {"clip_skip", "clipSkip", "clip_layer", "clip_stop_at_last_layers"},
	"ENSD":                {"ensd", "eta_noise_seed_delta", "ENSD"},
	"Token Merging":       {"token_merging", "tokenMerging", "tome"},
	"Refiner":             {"refiner", "refiner_model", "refinerName", "refiner_switch_at"},
	"Flux Guidance":       {"flux_guidance", "fluxGuidance", "flux_guidance_scale"},
	"Schedule type":       {"schedule type", "schedule_type", "scheduleType", "Schedule type", "noise_schedule"},
	"Distilled CFG Scale": {"distilled cfg scale", "distilled_cfg_scale", "distilledCFGScale", "Distilled CFG Scale"},
	"Version":             {"version", "Version", "app_version", "sd_version", "forge_version", "webui_version"},
	"Hypernet":            {"hypernet", "hyper_net", "hypernetwork", "Hypernet"},
	"ADetailer":           {"adetailer", "ADetailer", "ad_model"},
	"Face Restoration":    {"face_restoration", "face_restore", "face_restorer", "codeformer", "gfpgan"},
	"Style":               {"style", "Style", "style_name", "style_preset"},
}

// 重要参数白名单
var importantParams = map[string]bool{
	"Steps": true, "Sampler": true, "Scheduler": true, "Schedule type": true,
	"CFG Scale": true, "Distilled CFG Scale": true, "Flux Guidance": true,
	"Seed": true, "Size": true, "Width": true, "Height": true,
	"Model": true, "Model hash": true, "VAE": true, "CLIP": true, "Clip Skip": true,
	"LoRA": true, "Upscaler": true, "Refiner": true, "Hires Fix": true,
	"Denoise": true, "ControlNet": true, "Batch Size": true, "ENSD": true,
	"Token Merging": true, "Version": true, "Hypernet": true, "ADetailer": true,
	"Face Restoration": true, "Style": true,
}

func universalParse(textChunks map[string]string) *ImageMetadata {
	result := createEmptyResult()
	result.Raw = make(map[string]string)
	for k, v := range textChunks {
		result.Raw[k] = v
	}

	// 构建数据池
	var dataPool []dataItem

	for key, value := range textChunks {
		if value == "" {
			continue
		}

		// 尝试解析 JSON
		var jsonData interface{}
		if err := json.Unmarshal([]byte(value), &jsonData); err == nil {
			flattenJSON(jsonData, key, "", &dataPool, 0)
		} else {
			dataPool = append(dataPool, dataItem{source: key, key: key, value: value})
		}
	}

	// 提取 prompt
	extractPrompts(dataPool, result)

	// 提取参数
	extractAllParams(dataPool, result)

	// 后处理
	postProcessParams(result)

	return result
}

func flattenJSON(obj interface{}, source, path string, pool *[]dataItem, depth int) {
	if depth > 20 || obj == nil {
		return
	}

	switch v := obj.(type) {
	case map[string]interface{}:
		for key, val := range v {
			currentPath := path
			if currentPath != "" {
				currentPath += "." + key
			} else {
				currentPath = key
			}
			flattenJSON(val, source, currentPath, pool, depth+1)
		}
	case []interface{}:
		for i, val := range v {
			currentPath := fmt.Sprintf("%s[%d]", path, i)
			flattenJSON(val, source, currentPath, pool, depth+1)
		}
	case string:
		if v != "" {
			*pool = append(*pool, dataItem{source: source, key: path, value: v})
		}
	case float64, bool:
		*pool = append(*pool, dataItem{source: source, key: path, value: fmt.Sprintf("%v", v)})
	}
}

func extractPrompts(pool []dataItem, result *ImageMetadata) {
	var positiveCandidates []struct {
		text     string
		priority int
	}
	var negativeCandidates []struct {
		text     string
		priority int
	}
	var allTextItems []string

	for _, item := range pool {
		keyLower := strings.ToLower(item.key)
		val := item.value

		if len(val) < 3 {
			continue
		}

		if isPromptKey(keyLower) {
			positiveCandidates = append(positiveCandidates, struct {
				text     string
				priority int
			}{val, getKeyPriority(keyLower, true)})
		} else if isNegativeKey(keyLower) {
			negativeCandidates = append(negativeCandidates, struct {
				text     string
				priority int
			}{val, getKeyPriority(keyLower, false)})
		} else if len(val) > 20 {
			allTextItems = append(allTextItems, val)
		}
	}

	// 处理 A1111 格式
	for _, text := range allTextItems {
		if strings.Contains(text, "\nNegative prompt:") {
			parts := strings.SplitN(text, "\nNegative prompt:", 2)
			if len(parts) == 2 && len(parts[0]) > 10 {
				positiveCandidates = append(positiveCandidates, struct {
					text     string
					priority int
				}{strings.TrimSpace(parts[0]), 10})
			}
			if len(parts) == 2 {
				negClean := strings.TrimSpace(parts[1])
				if idx := strings.Index(negClean, "\n"); idx >= 0 {
					negClean = negClean[:idx]
				}
				if len(negClean) > 1 {
					negativeCandidates = append(negativeCandidates, struct {
						text     string
						priority int
					}{negClean, 10})
				}
			}
		}
	}

	sort.Slice(positiveCandidates, func(i, j int) bool {
		return positiveCandidates[i].priority > positiveCandidates[j].priority
	})
	sort.Slice(negativeCandidates, func(i, j int) bool {
		return negativeCandidates[i].priority > negativeCandidates[j].priority
	})

	if len(positiveCandidates) > 0 {
		result.Prompt = positiveCandidates[0].text
	} else if len(allTextItems) > 0 {
		// 取最长的文本
		sort.Slice(allTextItems, func(i, j int) bool {
			return len(allTextItems[i]) > len(allTextItems[j])
		})
		if len(allTextItems[0]) > 30 {
			result.Prompt = allTextItems[0]
		}
	}

	if len(negativeCandidates) > 0 {
		result.NegativePrompt = negativeCandidates[0].text
	}
}

func isPromptKey(key string) bool {
	patterns := []string{"prompt", "positive", "pos", "text", "caption", "description", "input_text", "positive_prompt", "pos_prompt"}
	for _, p := range patterns {
		if key == p || strings.HasSuffix(key, "."+p) {
			return true
		}
	}
	return false
}

func isNegativeKey(key string) bool {
	patterns := []string{"negative", "neg", "negative_prompt", "neg_prompt", "uc", "unconditioned", "negativeprompt"}
	for _, p := range patterns {
		if key == p || strings.HasSuffix(key, "."+p) {
			return true
		}
	}
	return false
}

func getKeyPriority(key string, isPositive bool) int {
	if isPositive {
		if key == "prompt" || key == "positive_prompt" || key == "pos_prompt" {
			return 100
		}
		if key == "text" || key == "caption" {
			return 80
		}
		if strings.Contains(key, "prompt") {
			return 60
		}
		return 40
	}
	if key == "negative_prompt" || key == "neg_prompt" || key == "uc" {
		return 100
	}
	if key == "negative" || key == "neg" {
		return 80
	}
	if strings.Contains(key, "negative") {
		return 60
	}
	return 40
}

func extractAllParams(pool []dataItem, result *ImageMetadata) {
	rawParams := make(map[string]string)

	for _, item := range pool {
		// 从文本中提取 Key: Value
		extractParamsFromText(item.value, rawParams)

		// 从 JSON 叶子节点提取
		keyName := extractLeafKeyName(item.key)
		if keyName != "" && !isPromptKey(strings.ToLower(keyName)) && !isNegativeKey(strings.ToLower(keyName)) {
			if item.value != "" {
				rawParams[keyName] = item.value
			}
		}
	}

	// 标准化参数名
	for rawKey, rawValue := range rawParams {
		standardKey := normalizeParamName(rawKey)
		if standardKey == "" {
			continue
		}
		if !importantParams[standardKey] {
			continue
		}
		if result.Params == nil {
			result.Params = make(map[string]string)
		}

		// 合并 LoRA
		if standardKey == "LoRA" {
			if existing, ok := result.Params[standardKey]; ok {
				if existing != rawValue {
					result.Params[standardKey] = existing + " | " + rawValue
					continue
				}
			}
		}

		if _, exists := result.Params[standardKey]; !exists {
			result.Params[standardKey] = rawValue
		}
	}
}

var paramRegex = regexp.MustCompile(`([A-Za-z][A-Za-z0-9_\s\-\.]*?)\s*[:=]\s*(.+)`)

func extractParamsFromText(text string, rawParams map[string]string) {
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || len(line) > 300 {
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "negative prompt:") {
			continue
		}

		// 按逗号分割多参数行
		segments := smartSplitParams(line)
		for _, segment := range segments {
			if strings.Contains(strings.ToLower(segment), "<lora:") || strings.Contains(strings.ToLower(segment), "<lyco:") {
				continue
			}
			matches := paramRegex.FindStringSubmatch(segment)
			if len(matches) == 3 {
				key := strings.TrimSpace(matches[1])
				value := strings.TrimSpace(matches[2])
				if len(key) <= 50 && len(value) <= 200 {
					rawParams[key] = value
				}
			}
		}
	}
}

func smartSplitParams(line string) []string {
	var segments []string
	var current strings.Builder
	inQuotes := false
	quoteChar := byte(0)

	for i := 0; i < len(line); i++ {
		ch := line[i]

		if (ch == '"' || ch == '\'') && (i == 0 || line[i-1] != '\\') {
			if !inQuotes {
				inQuotes = true
				quoteChar = ch
			} else if ch == quoteChar {
				inQuotes = false
			}
		}

		if !inQuotes && ch == ',' && (i+1 < len(line) && line[i+1] == ' ') {
			rest := strings.TrimSpace(line[i+1:])
			paramStartRegex := regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_\s]*[A-Za-z0-9_]\s*:`)
			if paramStartRegex.MatchString(rest) {
				if current.Len() > 0 {
					segments = append(segments, strings.TrimSpace(current.String()))
				}
				current.Reset()
				i++ // skip comma
				if i+1 < len(line) && line[i+1] == ' ' {
					i++ // skip space
				}
				continue
			}
		}
		current.WriteByte(ch)
	}

	if current.Len() > 0 {
		segments = append(segments, strings.TrimSpace(current.String()))
	}

	if len(segments) <= 1 {
		return []string{line}
	}
	return segments
}

func extractLeafKeyName(path string) string {
	if path == "" {
		return ""
	}
	// 取最后一个点或括号后的部分
	parts := regexp.MustCompile(`[\.\[\]]+`).Split(path, -1)
	if len(parts) == 0 {
		return ""
	}
	lastName := parts[len(parts)-1]
	if regexp.MustCompile(`^\d+$`).MatchString(lastName) && len(parts) > 1 {
		lastName = parts[len(parts)-2]
	}
	return lastName
}

func normalizeParamName(rawName string) string {
	lowerName := strings.ToLower(strings.TrimSpace(rawName))

	for standardName, aliases := range paramAliases {
		for _, alias := range aliases {
			lowerAlias := strings.ToLower(alias)
			if lowerName == lowerAlias {
				return standardName
			}
			if len(alias) >= 3 {
				boundaryRegex := regexp.MustCompile(`\b` + regexp.QuoteMeta(lowerAlias) + `\b`)
				if boundaryRegex.MatchString(lowerName) {
					return standardName
				}
			}
			if len(alias) >= 5 && strings.Contains(lowerName, lowerAlias) {
				return standardName
			}
		}
	}

	return ""
}

func postProcessParams(result *ImageMetadata) {
	if result.Params == nil {
		return
	}

	// 合并 Width + Height -> Size
	w, wOk := result.Params["Width"]
	h, hOk := result.Params["Height"]
	if wOk && hOk {
		if _, ok := result.Params["Size"]; !ok {
			result.Params["Size"] = w + "x" + h
		}
	}

	// 从 Size 解析 Width/Height
	if size, ok := result.Params["Size"]; ok {
		if _, wOk := result.Params["Width"]; !wOk {
			sizeRegex := regexp.MustCompile(`(\d+)\s*[x×X,]\s*(\d+)`)
			matches := sizeRegex.FindStringSubmatch(size)
			if len(matches) == 3 {
				result.Params["Width"] = matches[1]
				result.Params["Height"] = matches[2]
			}
		}
	}
}

func createEmptyResult() *ImageMetadata {
	return &ImageMetadata{
		Params: make(map[string]string),
		Raw:    make(map[string]string),
	}
}

// ==================== 格式检测 + 分发 ====================

// ParseTextChunks 是新的主入口：接收已解码的 PNG/JPEG/WebP text chunks，
// 自动检测来源工具格式并返回强类型的 ParsedParams。
func ParseTextChunks(textChunks map[string]string) *ParsedParams {
	if len(textChunks) == 0 {
		return nil
	}

	// DEBUG
	keys := make([]string, 0, len(textChunks))
	for k := range textChunks {
		keys = append(keys, k)
	}

	// 1. Midjourney 检测：Description chunk 含 --参数 格式
	if desc, ok := textChunks["Description"]; ok && IsMidjourneyDescription(desc) {
		return parseMidjourneyFromChunks(textChunks)
	}

	// 2. 收集 parameters / prompt / XMP chunk 的值
	paramText := ""
	if v, ok := textChunks["parameters"]; ok {
		paramText = v
	} else if v, ok := textChunks["prompt"]; ok {
		paramText = v
	}
	xmpText := ""
	if v, ok := textChunks["XML:com.adobe.xmp"]; ok {
		xmpText = v
	} else if v, ok := textChunks["xmp"]; ok {
		xmpText = v
	}

	// 3. SD/JSON/XMP 路径
	if paramText != "" {
		return parseParamText(paramText)
	}
	if xmpText != "" {
		return parseXMPText(xmpText)
	}

	return nil
}

// parseParamText 解析 parameters 或 prompt chunk 的文本内容，
// 自动判断 JSON（SwarmUI/ComfyUI）、XMP、或 SD WebUI 纯文本格式。
func parseParamText(raw string) *ParsedParams {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	// JSON 格式（SwarmUI sui_image_params、ComfyUI 节点图）
	if strings.HasPrefix(raw, "{") {
		var top map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &top); err == nil {
			_, hasSui := top["sui_image_params"]
			_, hasPrompt := top["prompt"]
			isNode := isComfyUINodeGraph(top)
			// SwarmUI
			if hasSui {
				return parseSwarmUIJSON(top)
			}
			// ComfyUI API 包装格式：{"prompt": {...}, "workflow": ...}
			if hasPrompt {
				return parseComfyUIJSON(top["prompt"], top["workflow"])
			}
			// ComfyUI 原始节点图（存储在 PNG prompt chunk 中）
			if isNode {
				return parseComfyUINodeGraph(top)
			}
			// 通用 JSON（扁平化为参数）
			return parseGenericJSON(top)
		} else {
		}
	}

	// XMP XML 格式
	if strings.Contains(raw, "x:xmpmeta") || strings.Contains(raw, "rdf:RDF") {
		return parseXMPText(raw)
	}

	// 默认：SD WebUI 纯文本
	return parseSDWebUIText(raw)
}

// parseSwarmUIJSON 解析 SwarmUI sui_image_params JSON
func parseSwarmUIJSON(top map[string]json.RawMessage) *ParsedParams {
	p := &ParsedParams{SourceTool: "SwarmUI", Extra: map[string]string{}}

	suiRaw := top["sui_image_params"]
	var sui struct {
		Prompt               string   `json:"prompt"`
		NegativePrompt       string   `json:"negativeprompt"`
		Model                string   `json:"model"`
		Seed                 int64    `json:"seed"`
		Steps                int      `json:"steps"`
		CFGScale             float64  `json:"cfgscale"`
		AspectRatio          string   `json:"aspectratio"`
		Width                int      `json:"width"`
		Height               int      `json:"height"`
		Sampler              string   `json:"sampler"`
		Scheduler            string   `json:"scheduler"`
		AutomaticVAE         bool     `json:"automaticvae"`
		VAE                  string   `json:"vae"`
		LoRAs                []string `json:"loras"`
		LoRAWeights          []string `json:"loraweights"`
		SwarmVersion         string   `json:"swarm_version"`
		RefinerControlPct    float64  `json:"refinercontrolpercentage"`
		RefinerMethod        string   `json:"refinermethod"`
		RefinerUpscale       float64  `json:"refinerupscale"`
		RefinerUpscaleMethod string   `json:"refinerupscalemethod"`
	}
	if err := json.Unmarshal(suiRaw, &sui); err != nil {
		return nil
	}

	p.Prompt = sui.Prompt
	p.NegativePrompt = sui.NegativePrompt
	p.Model = sui.Model
	p.Seed = sui.Seed
	p.Steps = sui.Steps
	p.CFGScale = sui.CFGScale
	p.AspectRatio = sui.AspectRatio
	p.Width = sui.Width
	p.Height = sui.Height
	p.Sampler = sui.Sampler
	p.Scheduler = sui.Scheduler
	p.AutomaticVAE = sui.AutomaticVAE
	p.VAE = sui.VAE
	p.SwarmVersion = sui.SwarmVersion
	p.RefinerControl = sui.RefinerControlPct
	p.RefinerMethod = sui.RefinerMethod
	p.RefinerUpscale = sui.RefinerUpscale
	p.RefinerUpscaleMethod = sui.RefinerUpscaleMethod

	// LoRAs
	for i, name := range sui.LoRAs {
		w := 1.0
		if i < len(sui.LoRAWeights) {
			if parsed, err := strconvParseFloat(sui.LoRAWeights[i]); err == nil {
				w = parsed
			}
		}
		p.LoRAs = append(p.LoRAs, LoRAEntry{Name: loraDisplayName(name), Weight: w})
	}

	// sui_extra_data
	if extraRaw, ok := top["sui_extra_data"]; ok {
		var extra struct {
			Date           string `json:"date"`
			GenerationTime string `json:"generation_time"`
		}
		if json.Unmarshal(extraRaw, &extra) == nil {
			p.Date = extra.Date
			p.GenerationTime = extra.GenerationTime
		}
	}

	// sui_models：补充模型哈希
	if modelsRaw, ok := top["sui_models"]; ok {
		var models []struct {
			Name  string `json:"name"`
			Param string `json:"param"`
			Hash  string `json:"hash"`
		}
		if json.Unmarshal(modelsRaw, &models) == nil {
			for _, m := range models {
				if m.Param == "model" {
					p.ModelHash = m.Hash
				} else if m.Param == "loras" {
					displayName := loraDisplayName(m.Name)
					for i, lora := range p.LoRAs {
						if strings.Contains(displayName, lora.Name) || strings.Contains(lora.Name, displayName) {
							p.LoRAs[i].Hash = m.Hash
						}
					}
				}
			}
		}
	}

	return p
}

// isComfyUINodeGraph 检测一个 JSON 对象是否是 ComfyUI 节点图（至少含一个 class_type 字段）
func isComfyUINodeGraph(top map[string]json.RawMessage) bool {
	for _, raw := range top {
		var node struct {
			ClassType string `json:"class_type"`
		}
		if json.Unmarshal(raw, &node) == nil && node.ClassType != "" {
			return true
		}
	}
	return false
}

// parseComfyUINodeGraph 解析 ComfyUI 原始节点图（无外层 prompt/workflow 包装）
func parseComfyUINodeGraph(nodes map[string]json.RawMessage) *ParsedParams {
	return parseComfyUIJSONNodes(nodes)
}

// parseComfyUIJSON 解析 ComfyUI 节点图 JSON（API 包装格式）
func parseComfyUIJSON(promptRaw, workflowRaw json.RawMessage) *ParsedParams {
	var nodes map[string]json.RawMessage
	if err := json.Unmarshal(promptRaw, &nodes); err != nil {
		return &ParsedParams{SourceTool: "ComfyUI", Extra: map[string]string{}}
	}
	return parseComfyUIJSONNodes(nodes)
}

func parseComfyUIJSONNodes(nodes map[string]json.RawMessage) *ParsedParams {
	p := &ParsedParams{SourceTool: "ComfyUI", Extra: map[string]string{}}
	classTypes := map[string]int{}

	// First pass: identify positive/negative node IDs from KSampler connections
	posNodeID := ""
	negNodeID := ""
	for _, nodeRaw := range nodes {
		var node struct {
			ClassType string          `json:"class_type"`
			Inputs    json.RawMessage `json:"inputs"`
		}
		if json.Unmarshal(nodeRaw, &node) != nil {
			continue
		}
		if node.ClassType == "KSampler" || node.ClassType == "KSamplerAdvanced" || node.ClassType == "SamplerCustomAdvanced" {
			var inputs struct {
				Positive json.RawMessage `json:"positive"`
				Negative json.RawMessage `json:"negative"`
			}
			if json.Unmarshal(node.Inputs, &inputs) == nil {
				posNodeID = extractNodeID(inputs.Positive)
				negNodeID = extractNodeID(inputs.Negative)
			}
			break
		}
	}

	// Collect CLIPTextEncode texts, keyed by node ID
	textNodes := map[string]string{}

	for nodeID, nodeRaw := range nodes {
		var node struct {
			ClassType string          `json:"class_type"`
			Inputs    json.RawMessage `json:"inputs"`
		}
		if json.Unmarshal(nodeRaw, &node) != nil {
			continue
		}
		classTypes[node.ClassType]++

		switch node.ClassType {
		case "CLIPTextEncode", "CLIPTextEncodeSDXL", "TextEncodeQwenImageEditPlus":
			var inputs struct {
				Text   string `json:"text"`
				Prompt string `json:"prompt"`
			}
			if json.Unmarshal(node.Inputs, &inputs) == nil {
				txt := inputs.Text
				if txt == "" {
					txt = inputs.Prompt
				}
				if txt != "" {
					textNodes[nodeID] = txt
				}
			}

		case "KSampler", "KSamplerAdvanced", "SamplerCustomAdvanced":
			var inputs struct {
				Seed      int64   `json:"seed"`
				NoiseSeed int64   `json:"noise_seed"`
				Steps     int     `json:"steps"`
				CFG       float64 `json:"cfg"`
				Sampler   string  `json:"sampler_name"`
				Scheduler string  `json:"scheduler"`
				Denoise   float64 `json:"denoise"`
			}
			if json.Unmarshal(node.Inputs, &inputs) == nil {
				sd := inputs.Seed
				if sd == 0 {
					sd = inputs.NoiseSeed
				}
				if sd != 0 {
					p.Seed = sd
				}
				if inputs.Steps != 0 {
					p.Steps = inputs.Steps
				}
				if inputs.CFG != 0 {
					p.CFGScale = inputs.CFG
				}
				if inputs.Sampler != "" {
					p.Sampler = inputs.Sampler
				}
				if inputs.Scheduler != "" {
					p.Scheduler = inputs.Scheduler
				}
				if inputs.Denoise != 0 {
					p.DenoisingStr = inputs.Denoise
				}
			}

		case "EmptyLatentImage", "EmptySD3LatentImage", "EmptyHunyuanLatentVideo":
			var inputs struct {
				Width  int `json:"width"`
				Height int `json:"height"`
			}
			if json.Unmarshal(node.Inputs, &inputs) == nil {
				if inputs.Width != 0 {
					p.Width = inputs.Width
				}
				if inputs.Height != 0 {
					p.Height = inputs.Height
				}
			}

		case "CheckpointLoaderSimple":
			var inputs struct {
				CkptName string `json:"ckpt_name"`
			}
			if json.Unmarshal(node.Inputs, &inputs) == nil && inputs.CkptName != "" {
				p.Model = inputs.CkptName
			}

		case "UNETLoader", "LoaderGGUF":
			var inputs struct {
				UnetName string `json:"unet_name"`
			}
			if json.Unmarshal(node.Inputs, &inputs) == nil && inputs.UnetName != "" {
				p.Model = loraDisplayName(inputs.UnetName)
			}

		case "DualCLIPLoader":
			var inputs struct {
				ClipName1 string `json:"clip_name1"`
			}
			if json.Unmarshal(node.Inputs, &inputs) == nil && inputs.ClipName1 != "" {
				if p.Model == "" {
					p.Model = loraDisplayName(inputs.ClipName1)
				}
			}

		case "VAELoader":
			var inputs struct {
				VAEName string `json:"vae_name"`
			}
			if json.Unmarshal(node.Inputs, &inputs) == nil && inputs.VAEName != "" {
				p.VAE = loraDisplayName(inputs.VAEName)
			}

		case "KSamplerSelect":
			var inputs struct {
				SamplerName string `json:"sampler_name"`
			}
			if json.Unmarshal(node.Inputs, &inputs) == nil && inputs.SamplerName != "" {
				p.Sampler = inputs.SamplerName
			}

		case "BasicScheduler":
			var inputs struct {
				Scheduler string `json:"scheduler"`
			}
			if json.Unmarshal(node.Inputs, &inputs) == nil && inputs.Scheduler != "" {
				p.Scheduler = inputs.Scheduler
			}

		case "BasicGuider", "FluxGuidance":
			var inputs struct {
				Guidance float64 `json:"guidance"`
			}
			if json.Unmarshal(node.Inputs, &inputs) == nil && inputs.Guidance != 0 {
				if p.CFGScale == 0 {
					p.CFGScale = inputs.Guidance
				}
			}

		case "RandomNoise":
			var inputs struct {
				NoiseSeed int64 `json:"noise_seed"`
			}
			if json.Unmarshal(node.Inputs, &inputs) == nil && inputs.NoiseSeed != 0 {
				if p.Seed == 0 {
					p.Seed = inputs.NoiseSeed
				}
			}

		case "LoraLoader", "LoraLoaderModelOnly", "HunyuanVideoLoraLoader":
			var inputs struct {
				LoraName      string  `json:"lora_name"`
				StrengthModel float64 `json:"strength_model"`
				StrengthClip  float64 `json:"strength_clip"`
				Strength      float64 `json:"strength"`
			}
			if json.Unmarshal(node.Inputs, &inputs) == nil && inputs.LoraName != "" {
				w := inputs.StrengthModel
				if w == 0 {
					w = inputs.StrengthClip
				}
				if w == 0 {
					w = inputs.Strength
				}
				p.LoRAs = append(p.LoRAs, LoRAEntry{Name: loraDisplayName(inputs.LoraName), Weight: w})
			}

		case "ModelSamplingSD3":
			var inputs struct {
				Shift float64 `json:"shift"`
			}
			if json.Unmarshal(node.Inputs, &inputs) == nil && inputs.Shift != 0 {
				p.Extra["sampling_shift"] = fmt.Sprintf("%.1f", inputs.Shift)
			}
		}

	}

	// Resolve positive/negative prompts using KSampler wiring
	if posNodeID != "" {
		if txt, ok := textNodes[posNodeID]; ok {
			p.Prompt = txt
		}
	}
	if negNodeID != "" {
		if txt, ok := textNodes[negNodeID]; ok {
			p.NegativePrompt = txt
		}
	}
	// Fallback: if KSampler wiring not found, use order-based assignment
	if len(textNodes) > 0 && p.Prompt == "" && p.NegativePrompt == "" {
		for _, txt := range textNodes {
			if p.Prompt == "" {
				p.Prompt = txt
			} else if p.NegativePrompt == "" {
				p.NegativePrompt = txt
			}
		}
	}

	return p
}

// extractNodeID extracts a node ID from a JSON value that may be encoded as ["id", index]
func extractNodeID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try direct string first
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	// Try ["id", index] array format
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil && len(arr) > 0 {
		var id string
		if json.Unmarshal(arr[0], &id) == nil {
			return id
		}
	}
	return ""
}

// parseGenericJSON 通用 JSON 参数对象 → ParsedParams
func parseGenericJSON(top map[string]json.RawMessage) *ParsedParams {
	p := &ParsedParams{SourceTool: "JSON", Extra: map[string]string{}}

	for k, v := range top {
		switch strings.ToLower(k) {
		case "prompt":
			if s, err := unmarshalString(v); err == nil {
				p.Prompt = s
			}
		case "negative_prompt", "negativeprompt":
			if s, err := unmarshalString(v); err == nil {
				p.NegativePrompt = s
			}
		case "model":
			if s, err := unmarshalString(v); err == nil {
				p.Model = s
			}
		case "seed":
			json.Unmarshal(v, &p.Seed)
		case "steps":
			json.Unmarshal(v, &p.Steps)
		case "cfg", "cfgscale", "cfg_scale":
			json.Unmarshal(v, &p.CFGScale)
		case "width":
			json.Unmarshal(v, &p.Width)
		case "height":
			json.Unmarshal(v, &p.Height)
		case "sampler", "sampler_name":
			if s, err := unmarshalString(v); err == nil {
				p.Sampler = s
			}
		case "scheduler":
			if s, err := unmarshalString(v); err == nil {
				p.Scheduler = s
			}
		default:
			p.Extra[k] = string(v)
		}
	}
	return p
}

// parseSDWebUIText 解析 SD WebUI / A1111 纯文本格式
func parseSDWebUIText(raw string) *ParsedParams {
	p := &ParsedParams{SourceTool: "SD WebUI", Extra: map[string]string{}}

	// 分离负向提示词
	negIdx := findNegPromptIdx(raw)
	paramIdx := findParamLineStart(raw)

	promptEnd := paramIdx
	if negIdx != -1 && negIdx < promptEnd {
		promptEnd = negIdx
	}
	if promptEnd > 0 {
		p.Prompt = strings.TrimSpace(raw[:promptEnd])
	} else if negIdx == -1 && paramIdx == -1 {
		p.Prompt = raw
		return p
	}

	if negIdx != -1 {
		negStart := negIdx + len("Negative prompt:")
		negEnd := paramIdx
		if negEnd == -1 || negEnd < negStart {
			negEnd = len(raw)
		}
		p.NegativePrompt = strings.TrimSpace(raw[negStart:negEnd])
	}

	// 提取内联 LoRA <lora:name:weight>
	p.LoRAs = extractInlineLoRAs(p.Prompt)

	// 解析参数行
	if paramIdx != -1 {
		parseSDParamLine(raw[paramIdx:], p)
	}

	return p
}

// ==================== 格式检测辅助 ====================

var mjDescRe = regexp.MustCompile(`(?:\s|^)--(?:ar|v|version|niji|stylize|chaos)\b|Job ID:\s*[0-9a-f]{8}-`)

// IsMidjourneyDescription 判断 Description chunk 值是否属于 Midjourney 格式
func IsMidjourneyDescription(desc string) bool {
	return mjDescRe.MatchString(desc)
}

// parseMidjourneyFromChunks 从 textChunks 构建 MidjourneyChunks 并解析
// Phase 2 会详细实现，此处仅做基础提取
func parseMidjourneyFromChunks(chunks map[string]string) *ParsedParams {
	p := &ParsedParams{SourceTool: "Midjourney", Extra: map[string]string{}}

	desc := strings.TrimSpace(chunks["Description"])
	p.Author = strings.TrimSpace(chunks["Author"])
	p.Date = strings.TrimSpace(chunks["Creation Time"])

	if desc == "" {
		return p
	}

	// 分离 prompt 与参数后缀
	promptText, paramSuffix := splitMJDesc(desc)
	p.Prompt = strings.TrimSpace(promptText)

	// Job ID
	if m := regexp.MustCompile(`Job ID:\s*([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`).FindStringSubmatch(desc); m != nil {
		p.JobID = m[1]
	}

	// 解析 --参数
	parseMJParamSuffix(paramSuffix, p)

	return p
}

// ==================== SD WebUI 纯文本解析辅助 ====================

var paramLineStartRe = regexp.MustCompile(`(?im)^(Steps|Sampler|CFG scale|Seed|Size|Model|Lora hashes|Version|VAE|Schedule type|Distilled CFG Scale|Denoising strength|Clip skip|Hires upscale|Hires steps|Hires upscaler|Face restoration|AddNet|freeu|sag|latent_modifier)\s*:`)

func findParamLineStart(raw string) int {
	loc := paramLineStartRe.FindStringIndex(raw)
	if loc == nil {
		return -1
	}
	start := loc[0]
	if start > 0 {
		if lineStart := strings.LastIndex(raw[:start], "\n"); lineStart != -1 {
			return lineStart + 1
		}
	}
	return start
}

func findNegPromptIdx(raw string) int {
	lower := strings.ToLower(raw)
	if idx := strings.Index(lower, "\nnegative prompt:"); idx != -1 {
		return idx + 1
	}
	if strings.HasPrefix(lower, "negative prompt:") {
		return 0
	}
	return -1
}

// 已知 SD 参数 key 白名单（正则）
var knownParamKeyRe = regexp.MustCompile(`(?i)\b(Steps|Sampler|Schedule type|CFG scale|Distilled CFG Scale|Seed|Size|Model hash|Model|VAE|Clip skip|Denoising strength|Denoising strength change factor|Face restoration|Hires upscale|Hires steps|Hires upscaler|Hires CFG Scale|Lora hashes|AddNet Enabled|AddNet Module \d+|AddNet Model \d+|AddNet Weight A \d+|AddNet Weight B \d+|Module \d+|freeu_enabled|freeu_b1|freeu_b2|freeu_s1|freeu_s2|freeu_start|freeu_end|sag_enabled|sag_scale|sag_blur_sigma|sag_threshold|Version|latent_modifier_\w+)\s*:`)

func parseSDParamLine(paramStr string, p *ParsedParams) {
	paramStr = strings.ReplaceAll(paramStr, "\n", ", ")

	// 用已知 Key 做定界点
	matches := knownParamKeyRe.FindAllStringIndex(paramStr, -1)
	type kv struct {
		key   string
		start int
	}
	var kvs []kv
	for _, m := range matches {
		key := strings.TrimSuffix(strings.TrimSpace(paramStr[m[0]:m[1]]), ":")
		kvs = append(kvs, kv{key: strings.TrimSpace(key), start: m[1]})
	}

	for i, item := range kvs {
		end := len(paramStr)
		if i+1 < len(kvs) {
			end = kvs[i+1].start - (len(kvs[i+1].key) + 1)
			if end < item.start {
				end = item.start
			}
		}
		if end > len(paramStr) {
			end = len(paramStr)
		}
		val := strings.TrimSpace(paramStr[item.start:end])
		val = strings.TrimSuffix(strings.TrimSpace(val), ",")
		assignParam(strings.ToLower(item.key), val, p)
	}

	// 单独解析 Lora hashes（引号内格式）
	if m := regexp.MustCompile(`(?i)Lora hashes:\s*"([^"]+)"`).FindStringSubmatch(paramStr); m != nil {
		applyLoraHashes(m[1], p)
	}
}

func assignParam(key, val string, p *ParsedParams) {
	switch {
	case key == "steps":
		p.Steps = atoi(val)
	case key == "sampler":
		p.Sampler = val
	case key == "schedule type":
		p.Scheduler = val
	case key == "cfg scale":
		p.CFGScale = atof(val)
	case key == "distilled cfg scale":
		p.DistilledCFG = atof(val)
	case key == "seed":
		p.Seed = atoi64(val)
	case key == "size":
		parts := strings.SplitN(val, "x", 2)
		if len(parts) == 2 {
			p.Width = atoi(strings.TrimSpace(parts[0]))
			p.Height = atoi(strings.TrimSpace(parts[1]))
		}
	case key == "model hash":
		p.ModelHash = val
	case key == "model":
		p.Model = val
	case key == "vae":
		p.VAE = val
	case key == "clip skip":
		p.ClipSkip = atoi(val)
	case key == "denoising strength":
		p.DenoisingStr = atof(val)
	case key == "face restoration":
		p.FaceRestorer = val
	case key == "hires upscale":
		p.HiresUpscale = atof(val)
	case key == "hires upscaler":
		p.HiresUpscaler = val
	case key == "hires steps":
		p.HiresSteps = atoi(val)
	case key == "hires cfg scale":
		p.HiresCFGScale = atof(val)
	case key == "version":
		p.Version = val
	default:
		if p.Extra == nil {
			p.Extra = map[string]string{}
		}
		p.Extra[key] = val
	}
}

// ==================== LoRA 解析 ====================

var inlineLoraRe = regexp.MustCompile(`<lora:([^:>]+):([0-9.]+)>`)

func extractInlineLoRAs(prompt string) []LoRAEntry {
	matches := inlineLoraRe.FindAllStringSubmatch(prompt, -1)
	var result []LoRAEntry
	for _, m := range matches {
		w, _ := strconvParseFloat(m[2])
		result = append(result, LoRAEntry{Name: m[1], Weight: w})
	}
	return result
}

func applyLoraHashes(hashesStr string, p *ParsedParams) {
	hashMap := map[string]string{}
	for _, pair := range strings.Split(hashesStr, ",") {
		kv := strings.SplitN(pair, ":", 2)
		if len(kv) == 2 {
			hashMap[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	for i, lora := range p.LoRAs {
		if h, ok := hashMap[lora.Name]; ok {
			p.LoRAs[i].Hash = h
		}
	}
}

func loraDisplayName(path string) string {
	parts := strings.Split(path, "/")
	name := parts[len(parts)-1]
	if idx := strings.LastIndex(name, "."); idx != -1 {
		name = name[:idx]
	}
	return name
}

// ==================== XMP 解析 ====================

func parseXMPText(raw string) *ParsedParams {
	raw = strings.TrimSpace(raw)
	// 裁剪到 xmpmeta 范围
	if idx := strings.Index(raw, "<x:xmpmeta"); idx > 0 {
		raw = raw[idx:]
	}
	if idx := strings.Index(raw, "</x:xmpmeta>"); idx != -1 {
		raw = raw[:idx+len("</x:xmpmeta>")]
	}

	p := &ParsedParams{SourceTool: "XMP", Extra: map[string]string{}}

	// 细化来源
	if strings.Contains(raw, "canva.com") {
		p.SourceTool = "CanvaXMP"
	} else if strings.Contains(raw, "adobe.com") || strings.Contains(raw, "adobe:ns:meta") {
		p.SourceTool = "AdobeXMP"
	} else if strings.Contains(raw, "DigitalSourceType") {
		p.SourceTool = "WebP-XMP"
	}

	// dc:description
	descRe := regexp.MustCompile(`(?s)<dc:description>.*?<rdf:li[^>]*>([^<]+)</rdf:li>`)
	if m := descRe.FindStringSubmatch(raw); m != nil {
		content := htmlUnescape(m[1])
		// 检查是否内嵌 SD 参数
		if strings.Contains(content, "Negative prompt:") || strings.Contains(content, "\nSteps:") {
			sub := parseSDWebUIText(content)
			sub.SourceTool = p.SourceTool
			return sub
		}
		p.Prompt = content
	}

	// AI generation source
	if strings.Contains(raw, "AI-generation-source") {
		if m := regexp.MustCompile(`AI-generation-source="([^"]+)"`).FindStringSubmatch(raw); m != nil {
			p.Extra["ai_generation_source"] = m[1]
		}
	}

	// DigitalSourceType
	if m := regexp.MustCompile(`DigitalSourceType="([^"]+)"`).FindStringSubmatch(raw); m != nil {
		p.Extra["digital_source_type"] = m[1]
	}

	return p
}

// ==================== Midjourney 解析辅助 ====================

var mjParamBlockRe = regexp.MustCompile(`(?i)\s--(?:ar|v|version|q|quality|s|stylize|c|chaos|w|weird|stop|repeat|tile|no|raw|hd|seed|iw|cref|sref|sw|cw|style|niji|turbo|relax|fast)\b`)

func splitMJDesc(desc string) (prompt, params string) {
	loc := mjParamBlockRe.FindStringIndex(desc)
	if loc == nil {
		return desc, ""
	}
	return desc[:loc[0]], desc[loc[0]:]
}

func parseMJParamSuffix(suffix string, p *ParsedParams) {
	suffix = strings.TrimSpace(suffix)
	// 去掉 Job ID 部分
	jobIDRe := regexp.MustCompile(`Job ID:\s*[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	suffix = jobIDRe.ReplaceAllString(suffix, "")

	paramRe := regexp.MustCompile(`--(\S+)(?:\s+([^-][^\s-]*(?:\s+[^-][^\s-]*)*))?`)
	matches := paramRe.FindAllStringSubmatch(suffix, -1)

	for _, m := range matches {
		key := strings.ToLower(strings.TrimSpace(m[1]))
		val := strings.TrimSpace(m[2])

		switch key {
		case "ar":
			p.AspectRatio = val
		case "v", "version":
			p.MJVersion = val
			p.Model = "Midjourney v" + val
		case "s", "stylize":
			p.Stylize = atoi(val)
		case "c", "chaos":
			p.Chaos = atoi(val)
		case "q", "quality":
			p.Quality = atof(val)
		case "w", "weird":
			p.Weird = atoi(val)
		case "stop":
			p.Stop = atoi(val)
		case "repeat", "r":
			p.Repeat = atoi(val)
		case "seed":
			p.Seed = atoi64(val)
		case "raw":
			p.Raw = true
		case "tile":
			p.Tile = true
		case "no":
			p.No = val
			if p.NegativePrompt == "" {
				p.NegativePrompt = val
			}
		case "niji":
			if val == "" {
				val = "latest"
			}
			p.Model = "Niji " + val
			p.MJVersion = "niji-" + val
		case "style":
			if p.Extra == nil {
				p.Extra = map[string]string{}
			}
			p.Extra["style"] = val
		case "iw":
			if p.Extra == nil {
				p.Extra = map[string]string{}
			}
			p.Extra["image_weight"] = val
		case "cref":
			if p.Extra == nil {
				p.Extra = map[string]string{}
			}
			p.Extra["character_reference"] = val
		case "sref":
			if p.Extra == nil {
				p.Extra = map[string]string{}
			}
			p.Extra["style_reference"] = val
		default:
			if p.Extra == nil {
				p.Extra = map[string]string{}
			}
			if val != "" {
				p.Extra[key] = val
			} else {
				p.Extra[key] = "true"
			}
		}
	}
}

// ==================== 工具函数 ====================

func atoi(s string) int {
	var n int
	fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	return n
}

func atoi64(s string) int64 {
	var n int64
	fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	return n
}

func atof(s string) float64 {
	var f float64
	fmt.Sscanf(strings.TrimSpace(s), "%f", &f)
	return f
}

func strconvParseFloat(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%f", &f)
	return f, err
}

func htmlUnescape(s string) string {
	s = strings.ReplaceAll(s, "&#xA;", "\n")
	s = strings.ReplaceAll(s, "&#x0A;", "\n")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")
	return s
}

func unmarshalString(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return string(raw), nil
	}
	return s, nil
}

// extractExifFromBytes 用 goexif 从 JPEG 字节中提取标准相机 EXIF 标签
func extractExifFromBytes(data []byte) map[string]string {
	x, err := exif.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}

	result := make(map[string]string)

	tag := func(name exif.FieldName, label string) {
		if v, err := x.Get(name); err == nil {
			result[label] = v.String()
		}
	}
	ratF := func(name exif.FieldName) (float64, bool) {
		if v, err := x.Get(name); err == nil {
			var r *big.Rat
			func() {
				defer func() {
					recover()
				}()
				r, _ = v.Rat(0)
			}()
			if r != nil && r.Denom().Int64() > 0 {
				f, _ := r.Float64()
				return f, true
			}
		}
		return 0, false
	}

	tag(exif.Make, "相机厂商")
	tag(exif.Model, "设备型号")
	tag(exif.Software, "软件")
	tag(exif.LensModel, "镜头型号")

	if dt, err := x.DateTime(); err == nil {
		result["拍摄时间"] = dt.Format("2006-01-02 15:04:05")
	}

	if v, err := x.Get(exif.ExposureTime); err == nil {
		var r *big.Rat
		func() {
			defer func() {
				recover()
			}()
			r, _ = v.Rat(0)
		}()
		if r != nil && r.Denom().Int64() > 0 {
			if r.Denom().Int64() > 1 {
				result["曝光时间"] = fmt.Sprintf("%d/%d sec", r.Num().Int64(), r.Denom().Int64())
			} else {
				f, _ := r.Float64()
				result["曝光时间"] = fmt.Sprintf("%.4f sec", f)
			}
		}
	}

	if f, ok := ratF(exif.FNumber); ok {
		result["光圈值"] = fmt.Sprintf("F%.1f", f)
	}
	if f, ok := ratF(exif.MaxApertureValue); ok {
		result["最大光圈"] = fmt.Sprintf("F%.1f", f)
	}
	if f, ok := ratF(exif.FocalLength); ok {
		result["焦距"] = fmt.Sprintf("%.1f mm", f)
	}
	if f, ok := ratF(exif.ExposureBiasValue); ok {
		result["曝光补偿"] = fmt.Sprintf("%.2f EV", f)
	}

	if v, err := x.Get(exif.ISOSpeedRatings); err == nil {
		if n, err := v.Int(0); err == nil {
			result["ISO感光度"] = fmt.Sprintf("%d", n)
		}
	}

	if v, err := x.Get(exif.Flash); err == nil {
		if n, err := v.Int(0); err == nil {
			if n&1 != 0 {
				result["闪光灯"] = "闪光灯开启"
			} else {
				result["闪光灯"] = "未闪光"
			}
		}
	}

	if v, err := x.Get(exif.MeteringMode); err == nil {
		if n, err := v.Int(0); err == nil {
			result["测光模式"] = meteringName(n)
		}
	}
	if v, err := x.Get(exif.ExposureProgram); err == nil {
		if n, err := v.Int(0); err == nil {
			result["曝光程序"] = expProgramName(n)
		}
	}
	if v, err := x.Get(exif.WhiteBalance); err == nil {
		if n, err := v.Int(0); err == nil {
			result["白平衡"] = wbName(n)
		}
	}

	if lat, lon, err := x.LatLong(); err == nil {
		result["GPS纬度"] = fmt.Sprintf("%.6f", lat)
		result["GPS经度"] = fmt.Sprintf("%.6f", lon)
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

func meteringName(n int) string {
	switch n {
	case 1:
		return "平均测光"
	case 2:
		return "中央重点平均测光"
	case 3:
		return "点测光"
	case 4:
		return "多区测光"
	case 5:
		return "多模式测光"
	case 6:
		return "局部测光"
	case 255:
		return "其他"
	default:
		return fmt.Sprintf("未知(%d)", n)
	}
}

func expProgramName(n int) string {
	switch n {
	case 1:
		return "手动"
	case 2:
		return "标准程序"
	case 3:
		return "光圈优先"
	case 4:
		return "快门优先"
	case 5:
		return "创意程序"
	case 6:
		return "运动程序"
	case 7:
		return "人像模式"
	case 8:
		return "风景模式"
	default:
		return fmt.Sprintf("未知(%d)", n)
	}
}

func wbName(n int) string {
	switch n {
	case 0:
		return "自动"
	case 1:
		return "手动"
	default:
		return fmt.Sprintf("未知(%d)", n)
	}
}
