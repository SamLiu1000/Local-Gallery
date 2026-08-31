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
			// APP1: EXIF 或 XMP
			if offset+4 < len(data) {
				length := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
				if offset+2+length <= len(data) {
					payload := data[offset+4 : offset+2+length]
					switch {
					case len(payload) > 6 && string(payload[:6]) == "Exif\x00\x00":
						// 标准 EXIF APP1
						if exifText := extractEXIFText(payload); exifText != "" {
							textChunks["exif"] = exifText
						}
					case len(payload) > 29 && string(payload[:29]) == "http://ns.adobe.com/xap/1.0/\x00":
						// 标准 XMP APP1（Photoshop / Google AI 等写入，含 DigitalSourceType / Credit）
						if xmp := strings.TrimSpace(string(payload[29:])); xmp != "" {
							textChunks["XML:com.adobe.xmp"] = xmp
						}
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
		} else if marker == 0xFFED {
			// APP13 Photoshop IRB（8BIM 资源，如 "Made with Google AI" 来源说明）
			if offset+4 < len(data) {
				length := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
				if offset+2+length <= len(data) {
					if caption := ExtractPhotoshopCaption(data[offset+4 : offset+2+length]); caption != "" {
						textChunks["photoshop_caption"] = caption
					}
				}
				offset += 2 + length
			} else {
				break
			}
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

	// ★ 优先走 ParseTextChunks 专门解析（ComfyUI 递归追踪 / SD / SwarmUI / XMP），
	//   它比泛化 flatten 更准确。拿不到有效参数时才回退到泛化解析。
	//   XMP 元数据可能没有 prompt（如 Google AI 的 DigitalSourceType/Credit），
	//   此时 SourceTool 非空也应走专门解析以保留 Extra 信息。
	if pp := ParseTextChunks(textChunks); pp != nil && (pp.Prompt != "" || pp.NegativePrompt != "" || len(pp.LoRAs) > 0 || pp.SourceTool != "") {
		result.Prompt = pp.Prompt
		result.NegativePrompt = pp.NegativePrompt
		if result.Params == nil {
			result.Params = make(map[string]string)
		}
		// 复制 params（ParsedParams 无直接方法，这里手动转）
		if pp.Steps != 0 {
			result.Params["Steps"] = fmt.Sprintf("%d", pp.Steps)
		}
		if pp.Sampler != "" {
			result.Params["Sampler"] = pp.Sampler
		}
		if pp.Scheduler != "" {
			result.Params["Scheduler"] = pp.Scheduler
		}
		if pp.CFGScale != 0 {
			result.Params["CFG Scale"] = fmt.Sprintf("%.1f", pp.CFGScale)
		}
		if pp.Seed != 0 {
			result.Params["Seed"] = fmt.Sprintf("%d", pp.Seed)
		}
		if pp.Width != 0 && pp.Height != 0 {
			result.Params["Size"] = fmt.Sprintf("%dx%d", pp.Width, pp.Height)
			result.Params["Width"] = fmt.Sprintf("%d", pp.Width)
			result.Params["Height"] = fmt.Sprintf("%d", pp.Height)
		}
		if pp.Model != "" {
			result.Params["Model"] = pp.Model
		}
		if pp.VAE != "" {
			result.Params["VAE"] = pp.VAE
		}
		if pp.ClipSkip != 0 {
			result.Params["Clip Skip"] = fmt.Sprintf("%d", pp.ClipSkip)
		}
		if pp.DenoisingStr != 0 {
			result.Params["Denoising strength"] = fmt.Sprintf("%.2f", pp.DenoisingStr)
		}
		if len(pp.LoRAs) > 0 {
			var parts []string
			for _, l := range pp.LoRAs {
				if l.Weight != 0 {
					parts = append(parts, l.Name+":"+fmt.Sprintf("%.2f", l.Weight))
				} else {
					parts = append(parts, l.Name)
				}
			}
			result.Params["LoRA"] = strings.Join(parts, ", ")
		}
		for k, v := range pp.Extra {
			result.Params[k] = v
		}
		if pp.SourceTool != "" {
			result.Params["SourceTool"] = pp.SourceTool
		}
		return result
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
		parsed := parseParamText(paramText)
		// ★ ComfyUI 增强：用 workflow chunk 的 widgets_values 补全缺失参数。
		//   prompt 节点图往往缺少 Size / 模型路径目录 / 部分采样参数，
		//   workflow 里存有完整的 UI widgets_values（有序数组），可精确补全。
		if parsed != nil && parsed.SourceTool == "ComfyUI" {
			if wfRaw, ok := textChunks["workflow"]; ok && wfRaw != "" {
				mergeWorkflowParams(parsed, wfRaw)
			}
		}
		return parsed
	}
	if xmpText != "" {
		return parseXMPText(xmpText)
	}

	return nil
}

// parseWorkflowWidgets 解析 ComfyUI workflow chunk 的 widgets_values，
// 返回按节点 ID 索引的 {nodeType, widgets} 映射。
func parseWorkflowWidgets(wfRaw string) (map[string]workflowNode, bool) {
	var wf struct {
		Nodes []struct {
			ID    json.RawMessage `json:"id"`
			Type  string          `json:"type"`
			Wvals json.RawMessage `json:"widgets_values"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(wfRaw), &wf); err != nil {
		return nil, false
	}
	nodes := make(map[string]workflowNode, len(wf.Nodes))
	for _, n := range wf.Nodes {
		id := strings.Trim(string(n.ID), `"`)
		if id == "" {
			continue
		}
		var widgets []json.RawMessage
		if err := json.Unmarshal(n.Wvals, &widgets); err != nil {
			widgets = nil
		}
		nodes[id] = workflowNode{Type: n.Type, Widgets: widgets}
	}
	return nodes, len(nodes) > 0
}

type workflowNode struct {
	Type    string
	Widgets []json.RawMessage
}

// wfStr 读取 workflow widgets 数组中指定下标的字符串值。
func wfStr(w []json.RawMessage, idx int) string {
	if idx < 0 || idx >= len(w) {
		return ""
	}
	var s string
	if json.Unmarshal(w[idx], &s) == nil {
		return s
	}
	return ""
}

// wfNum 读取 workflow widgets 数组中指定下标的数值（int 或 float）。
func wfNum(w []json.RawMessage, idx int) float64 {
	if idx < 0 || idx >= len(w) {
		return 0
	}
	var f float64
	if json.Unmarshal(w[idx], &f) == nil {
		return f
	}
	var i int
	if json.Unmarshal(w[idx], &i) == nil {
		return float64(i)
	}
	return 0
}

// mergeWorkflowParams 用 workflow 的 widgets_values 补全 ComfyUI 解析结果中
// 缺失的字段。只补缺失，不覆盖已有值，避免覆盖 prompt 节点图的精确结果。
func mergeWorkflowParams(p *ParsedParams, wfRaw string) {
	nodes, ok := parseWorkflowWidgets(wfRaw)
	if !ok {
		return
	}
	for _, node := range nodes {
		w := node.Widgets
		switch node.Type {
		case "EmptyLatentImage", "EmptySD3LatentImage", "EmptyHunyuanLatentVideo", "EmptyFluxLatentImage":
			// widgets_values: [width, height, batch_size]
			if p.Width == 0 {
				if v := int(wfNum(w, 0)); v > 0 {
					p.Width = v
				}
			}
			if p.Height == 0 {
				if v := int(wfNum(w, 1)); v > 0 {
					p.Height = v
				}
			}
		case "CheckpointLoaderSimple", "UNETLoader", "LoaderGGUF", "CheckpointLoader":
			// [ckpt_name, ...] 或 [unet_name, ...]
			if p.Model == "" {
				if s := wfStr(w, 0); s != "" {
					p.Model = loraDisplayName(s)
				}
			}
		case "CLIPLoader", "DualCLIPLoader", "TripleCLIPLoader":
			// [clip_name, type, ...] / [clip_name1, clip_name2, type, ...]
			if p.Extra["CLIP"] == "" {
				if s := wfStr(w, 0); s != "" {
					p.Extra["CLIP"] = loraDisplayName(s)
				}
			}
		case "VAELoader":
			if p.VAE == "" {
				if s := wfStr(w, 0); s != "" {
					p.VAE = loraDisplayName(s)
				}
			}
		case "KSampler", "KSamplerAdvanced", "KSamplerWithNAG", "SamplerCustomAdvanced":
			// [seed, control_after_generate, steps, cfg, sampler, scheduler, denoise]
			if p.Seed == 0 {
				if v := wfNum(w, 0); v != 0 {
					p.Seed = int64(v)
				}
			}
			if p.Steps == 0 {
				if v := int(wfNum(w, 2)); v > 0 {
					p.Steps = v
				}
			}
			if p.CFGScale == 0 {
				if v := wfNum(w, 3); v != 0 {
					p.CFGScale = v
				}
			}
			if p.Sampler == "" {
				if s := wfStr(w, 4); s != "" {
					p.Sampler = s
				}
			}
			if p.Scheduler == "" {
				if s := wfStr(w, 5); s != "" {
					p.Scheduler = s
				}
			}
			if p.DenoisingStr == 0 {
				if v := wfNum(w, 6); v != 0 {
					p.DenoisingStr = v
				}
			}
		case "LoraLoader", "LoraLoaderModelOnly", "HunyuanVideoLoraLoader":
			// [lora_name, strength_model, strength_clip]
			if s := wfStr(w, 0); s != "" {
				sm := wfNum(w, 1)
				sc := wfNum(w, 2)
				weight := sm
				if weight == 0 {
					weight = sc
				}
				p.LoRAs = append(p.LoRAs, LoRAEntry{Name: loraDisplayName(s), Weight: weight})
			}
		case "Power Lora Loader (rgthree)":
			// widgets_values 里 lora_1.. 以对象形式存在
			for _, raw := range w {
				var obj map[string]json.RawMessage
				if json.Unmarshal(raw, &obj) == nil {
					var lora struct {
						On       bool    `json:"on"`
						Lora     string  `json:"lora"`
						Strength float64 `json:"strength"`
					}
					if json.Unmarshal(raw, &lora) == nil && lora.On && lora.Lora != "" {
						p.LoRAs = append(p.LoRAs, LoRAEntry{
							Name:   loraDisplayName(lora.Lora),
							Weight: lora.Strength,
						})
					}
				}
			}
		case "NunchakuQwenImageLoraStack":
			// widgets_values: [count, cpu_offload, name1, w1, name2, w2, ...]
			for i := 2; i+1 < len(w); i += 2 {
				if s := wfStr(w, i); s != "" && s != "None" {
					p.LoRAs = append(p.LoRAs, LoRAEntry{Name: loraDisplayName(s), Weight: wfNum(w, i+1)})
				}
			}
		case "NunchakuQwenImageLoraStackV3":
			// widgets_values: ["1", 1, true, "disable", true, name, weight, ...]
			// 成对的 name/weight 从 index 5 开始
			for i := 5; i+1 < len(w); i += 2 {
				if s := wfStr(w, i); s != "" && s != "None" {
					p.LoRAs = append(p.LoRAs, LoRAEntry{Name: loraDisplayName(s), Weight: wfNum(w, i+1)})
				}
			}
		case "RandomNoise":
			// [noise_seed, ...]
			if p.Seed == 0 {
				if v := wfNum(w, 0); v != 0 {
					p.Seed = int64(v)
				}
			}
		}
	}

	// ★ 去重：prompt 节点图与 workflow widgets 可能重复解析同一批 LoRA，
	// 按 (名称, 权重) 去重，保持 prompt 解析的顺序优先。
	if len(p.LoRAs) > 1 {
		seen := make(map[string]bool, len(p.LoRAs))
		dedup := p.LoRAs[:0]
		for _, l := range p.LoRAs {
			key := fmt.Sprintf("%s|%.3f", l.Name, l.Weight)
			if !seen[key] {
				seen[key] = true
				dedup = append(dedup, l)
			}
		}
		p.LoRAs = dedup
	}
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

	// 保存所有节点的 inputs（按字段名），供递归追踪提示词/连接使用
	allInputs := make(map[string]map[string]json.RawMessage)
	classByID := make(map[string]string)
	for nodeID, nodeRaw := range nodes {
		var node struct {
			ClassType string                     `json:"class_type"`
			Inputs    map[string]json.RawMessage `json:"inputs"`
		}
		if json.Unmarshal(nodeRaw, &node) != nil {
			continue
		}
		classByID[nodeID] = node.ClassType
		allInputs[nodeID] = node.Inputs
	}

	// First pass: identify positive/negative node IDs from sampler connections.
	// Check ALL nodes for positive/negative inputs instead of matching specific class
	// names, so KSampler, KSamplerAdvanced, KSamplerWithNAG, SamplerCustomAdvanced
	// and any future variants are all handled.
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
		var inputs struct {
			Positive json.RawMessage `json:"positive"`
			Negative json.RawMessage `json:"negative"`
		}
		if json.Unmarshal(node.Inputs, &inputs) != nil {
			continue
		}
		if posNodeID == "" {
			if id := extractNodeID(inputs.Positive); id != "" {
				posNodeID = id
			}
		}
		if negNodeID == "" {
			if id := extractNodeID(inputs.Negative); id != "" {
				negNodeID = id
			}
		}
		if posNodeID != "" && negNodeID != "" {
			break
		}
	}

	// 兜底：无 positive/negative 接线时，找带 prompt 连接输入的任务节点
	// （如 MiniMaxH3ReferenceToVideo 的 inputs.prompt: ["id", 0]），
	// 将该连接指向的文本节点作为正向提示词来源。
	if posNodeID == "" {
		for nodeID, nodeRaw := range nodes {
			var node struct {
				ClassType string          `json:"class_type"`
				Inputs    json.RawMessage `json:"inputs"`
			}
			if json.Unmarshal(nodeRaw, &node) != nil {
				continue
			}
			var inputs struct {
				Prompt json.RawMessage `json:"prompt"`
			}
			if json.Unmarshal(node.Inputs, &inputs) != nil {
				continue
			}
			if id := extractNodeID(inputs.Prompt); id != "" && id != nodeID {
				posNodeID = id
				break
			}
		}
	}

	// resolveText 沿节点连接递归追踪，返回叶子文本节点中的提示词文本。
	// ComfyUI 的 positive/negative 可能不直接连到 CLIPTextEncode，而是经过
	// easy ifElse / ConditioningCombine / ConditioningSetArea 等中间节点，
	// 需要沿 on_true/on_false/positive/conditioning 等字段层层解引用。
	var resolveText func(nodeID string, depth int) string
	resolveText = func(nodeID string, depth int) string {
		if nodeID == "" || depth > 12 {
			return ""
		}
		inputs, ok := allInputs[nodeID]
		if !ok {
			return ""
		}
		// 1. 节点自身是文本节点：text / prompt / value 字段直接取值
		//    （PrimitiveStringMultiline 等叶子文本节点用 value 字段）
		var textVal string
		if raw, ok := inputs["text"]; ok {
			json.Unmarshal(raw, &textVal)
		}
		if textVal == "" {
			if raw, ok := inputs["prompt"]; ok {
				json.Unmarshal(raw, &textVal)
			}
		}
		if textVal == "" {
			if raw, ok := inputs["value"]; ok {
				json.Unmarshal(raw, &textVal)
			}
		}
		if textVal != "" {
			return textVal
		}

		// 2. 沿连接字段递归：先试语义明确的文本连接，再试通用字段
		candidates := []string{
			"positive", "negative", "conditioning", "conditioning_positive",
			"conditioning_negative", "on_true", "on_false", "text", "prompt",
			"clip", "conditioning_to", "base_conditioning", "refiner_conditioning",
		}
		for _, field := range candidates {
			raw, ok := inputs[field]
			if !ok {
				continue
			}
			if id := extractNodeID(raw); id != "" {
				if txt := resolveText(id, depth+1); txt != "" {
					return txt
				}
			}
		}

		// 3. 兜底：任意字段里是数组连接 ["id", idx] 的，逐一尝试
		for _, raw := range inputs {
			if id := extractNodeID(raw); id != "" {
				if txt := resolveText(id, depth+1); txt != "" {
					return txt
				}
			}
		}
		return ""
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

		case "KSampler", "KSamplerAdvanced", "KSamplerWithNAG", "SamplerCustomAdvanced":
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
				p.Model = loraDisplayName(inputs.CkptName)
			}

			case "UNETLoader", "LoaderGGUF":
				var inputs struct {
					UnetName string `json:"unet_name"`
				}
				if json.Unmarshal(node.Inputs, &inputs) == nil && inputs.UnetName != "" {
					p.Model = loraDisplayName(inputs.UnetName)
				}

			case "NunchakuQwenImageDiTLoader", "DiTLoader", "FluxDiTLoader":
				var inputs struct {
					ModelName string `json:"model_name"`
					UnetName  string `json:"unet_name"`
					Model     string `json:"model"`
				}
				if json.Unmarshal(node.Inputs, &inputs) == nil {
					name := inputs.ModelName
					if name == "" {
						name = inputs.UnetName
					}
					if name == "" {
						name = inputs.Model
					}
					if name != "" {
						p.Model = loraDisplayName(name)
					}
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

			case "Power Lora Loader (rgthree)":
				// rgthree Power Lora Loader stores loras as lora_1, lora_2, ...
				// Each value is {on: bool, lora: string, strength: float}.
				var inputs map[string]json.RawMessage
				if json.Unmarshal(node.Inputs, &inputs) == nil {
					keys := make([]string, 0, len(inputs))
					for k := range inputs {
						if strings.HasPrefix(k, "lora_") {
							keys = append(keys, k)
						}
					}
					sort.Strings(keys)
					for _, key := range keys {
						var lora struct {
							On       bool    `json:"on"`
							Lora     string  `json:"lora"`
							Strength float64 `json:"strength"`
						}
						if json.Unmarshal(inputs[key], &lora) == nil && lora.On && lora.Lora != "" {
							p.LoRAs = append(p.LoRAs, LoRAEntry{
								Name:   loraDisplayName(lora.Lora),
								Weight: lora.Strength,
							})
						}
					}
				}

			case "NunchakuQwenImageLoraStack", "NunchakuQwenImageLoraStackV3":
				// Nunchaku 系 Lora 堆叠节点：lora_name_N + lora_strength_N + enabled_N(V3)
				// "None" 表示空槽位，跳过；V3 用 enabled_N 控制开关。
				var inputs map[string]json.RawMessage
				if json.Unmarshal(node.Inputs, &inputs) == nil {
					// 收集序号 1..N，按 lora_name_N 判定槽位
					var idxs []int
					for k := range inputs {
						var n int
						if _, err := fmt.Sscanf(k, "lora_name_%d", &n); err == nil {
							idxs = append(idxs, n)
						}
					}
					sort.Ints(idxs)
					for _, n := range idxs {
						var name string
						if err := json.Unmarshal(inputs[fmt.Sprintf("lora_name_%d", n)], &name); err != nil || name == "" || name == "None" {
							continue
						}
						// V3 的 enabled_N 若为 false 则跳过
						if raw, ok := inputs[fmt.Sprintf("enabled_%d", n)]; ok {
							var enabled bool
							if json.Unmarshal(raw, &enabled) == nil && !enabled {
								continue
							}
						}
						weight := 1.0
						if raw, ok := inputs[fmt.Sprintf("lora_strength_%d", n)]; ok {
							json.Unmarshal(raw, &weight)
						}
						p.LoRAs = append(p.LoRAs, LoRAEntry{
							Name:   loraDisplayName(name),
							Weight: weight,
						})
					}
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

	// Resolve positive/negative prompts using KSampler wiring.
	// ★ 支持中间节点（easy ifElse / ConditioningCombine 等）：递归追踪到叶子文本节点。
	if posNodeID != "" {
		if txt := resolveText(posNodeID, 0); txt != "" {
			p.Prompt = txt
		}
	}
	if negNodeID != "" {
		if txt := resolveText(negNodeID, 0); txt != "" {
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
	// 兼容 Windows 反斜杠与 Unix 斜杠路径分隔
	path = strings.ReplaceAll(path, "\\", "/")
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
	} else if strings.Contains(raw, "Made with Google AI") {
		// Google AI 生图（Whisk / Imagen / Gemini 等导出）: photoshop:Credit="Made with Google AI"
		p.SourceTool = "Google AI"
		p.Model = "Google AI"
	} else if strings.Contains(raw, "adobe.com") || strings.Contains(raw, "adobe:ns:meta") {
		p.SourceTool = "AdobeXMP"
	} else if strings.Contains(raw, "DigitalSourceType") {
		// IPTC DigitalSourceType=trainedAlgorithmicMedia 等 AI 来源标记（Bing/Designer 等）
		p.SourceTool = "AI-XMP"
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

	// DigitalSourceFileType
	if m := regexp.MustCompile(`DigitalSourceFileType="([^"]+)"`).FindStringSubmatch(raw); m != nil {
		p.Extra["digital_source_file_type"] = m[1]
	}

	// photoshop:Credit（如 "Made with Google AI"）
	if m := regexp.MustCompile(`photoshop:Credit="([^"]+)"`).FindStringSubmatch(raw); m != nil {
		p.Extra["credit"] = htmlUnescape(m[1])
	}

	return p
}

// ExtractPhotoshopCaption 解析 Photoshop APP13 (IRB, 8BIM 资源) 中的 Caption 资源 (0x0404)，
// 提取其中的可读文本（如 Google AI 图片的 "Made with Google AI" 来源说明）。
// 无法解析或没有可读文本时返回空字符串。
func ExtractPhotoshopCaption(payload []byte) string {
	const psSig = "Photoshop 3.0\x00"
	if len(payload) < len(psSig) || string(payload[:len(psSig)]) != psSig {
		return ""
	}
	pos := len(psSig)
	for pos+12 <= len(payload) {
		// 8BIM 资源签名
		if string(payload[pos:pos+4]) != "8BIM" {
			break
		}
		resID := binary.BigEndian.Uint16(payload[pos+4 : pos+6])
		pos += 6
		// Pascal 字符串名称（补齐到偶数长度）
		nameLen := int(payload[pos])
		pos += 1 + nameLen
		if pos%2 == 1 {
			pos++
		}
		if pos+4 > len(payload) {
			break
		}
		size := int(binary.BigEndian.Uint32(payload[pos : pos+4]))
		pos += 4
		if pos+size > len(payload) {
			break
		}
		resData := payload[pos : pos+size]
		if resID == 0x0404 { // Caption 资源
			// Caption 数据含编码/长度前缀等二进制头部，取最长的可读 ASCII 连续段
			best := ""
			var cur []byte
			flush := func() {
				if len(cur) >= 3 && len(cur) > len(best) {
					best = string(cur)
				}
				cur = cur[:0]
			}
			for _, b := range resData {
				if b >= 0x20 && b < 0x7f {
					cur = append(cur, b)
				} else {
					flush()
				}
			}
			flush()
			best = strings.TrimSpace(best)
			if best != "" {
				return best
			}
		}
		pos += size
		if pos%2 == 1 {
			pos++
		}
	}
	return ""
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
