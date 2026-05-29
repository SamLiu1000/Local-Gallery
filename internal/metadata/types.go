package metadata

import (
	"fmt"
	"strings"
)

// LoRAEntry 单条 LoRA 记录
type LoRAEntry struct {
	Name   string  `json:"name"`
	Weight float64 `json:"weight"`
	Hash   string  `json:"hash,omitempty"`
}

// ParsedParams AI 图片生成的标准化参数集合
type ParsedParams struct {
	// 提示词
	Prompt         string `json:"prompt,omitempty"`
	NegativePrompt string `json:"negative_prompt,omitempty"`

	// 模型相关
	Model     string      `json:"model,omitempty"`
	ModelHash string      `json:"model_hash,omitempty"`
	VAE       string      `json:"vae,omitempty"`
	LoRAs     []LoRAEntry `json:"loras,omitempty"`

	// 画面尺寸
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`

	// 采样相关
	Sampler      string  `json:"sampler,omitempty"`
	Scheduler    string  `json:"scheduler,omitempty"`
	Steps        int     `json:"steps,omitempty"`
	CFGScale     float64 `json:"cfg_scale,omitempty"`
	DistilledCFG float64 `json:"distilled_cfg_scale,omitempty"`

	// 生成控制
	Seed         int64   `json:"seed,omitempty"`
	ClipSkip     int     `json:"clip_skip,omitempty"`
	DenoisingStr float64 `json:"denoising_strength,omitempty"`
	FaceRestorer string  `json:"face_restoration,omitempty"`

	// Hires / Refiner
	HiresUpscale         float64 `json:"hires_upscale,omitempty"`
	HiresUpscaler        string  `json:"hires_upscaler,omitempty"`
	HiresSteps           int     `json:"hires_steps,omitempty"`
	HiresCFGScale        float64 `json:"hires_cfg_scale,omitempty"`
	RefinerControl       float64 `json:"refiner_control_percentage,omitempty"`
	RefinerMethod        string  `json:"refiner_method,omitempty"`
	RefinerUpscale       float64 `json:"refiner_upscale,omitempty"`
	RefinerUpscaleMethod string  `json:"refiner_upscale_method,omitempty"`

	// SwarmUI 专有
	AutomaticVAE bool   `json:"automatic_vae,omitempty"`
	SwarmVersion string `json:"swarm_version,omitempty"`
	AspectRatio  string `json:"aspect_ratio,omitempty"`

	// 生成信息
	Date           string `json:"date,omitempty"`
	GenerationTime string `json:"generation_time,omitempty"`
	Version        string `json:"version,omitempty"`

	// Midjourney 专有
	JobID     string  `json:"job_id,omitempty"`
	Author    string  `json:"author,omitempty"`
	MJVersion string  `json:"mj_version,omitempty"`
	Stylize   int     `json:"stylize,omitempty"`
	Chaos     int     `json:"chaos,omitempty"`
	Quality   float64 `json:"quality,omitempty"`
	Weird     int     `json:"weird,omitempty"`
	Raw       bool    `json:"raw,omitempty"`
	Tile      bool    `json:"tile,omitempty"`
	No        string  `json:"no,omitempty"`
	Stop      int     `json:"stop,omitempty"`
	Repeat    int     `json:"repeat,omitempty"`

	// 来源识别
	SourceTool string `json:"source_tool,omitempty"`

	// 未归类的原始 KV
	Extra map[string]string `json:"extra,omitempty"`
}

// MidjourneyChunks 调用方从 PNG chunks 收集的 MJ 相关字段
type MidjourneyChunks struct {
	Description  string
	Author       string
	CreationTime string
	XMPDigGUID   string
}

// ToLegacy 转换为旧版 map[string]interface{} 格式（向后兼容）
func (p *ParsedParams) ToLegacy() map[string]interface{} {
	if p == nil {
		return map[string]interface{}{
			"prompt":         "",
			"negativePrompt": "",
			"params":         map[string]interface{}{},
			"raw":            map[string]interface{}{},
		}
	}
	result := map[string]interface{}{
		"prompt":         p.Prompt,
		"negativePrompt": p.NegativePrompt,
	}

	params := map[string]interface{}{}
	if p.Steps != 0 {
		params["Steps"] = fmt.Sprintf("%d", p.Steps)
	}
	if p.Sampler != "" {
		params["Sampler"] = p.Sampler
	}
	if p.Scheduler != "" {
		params["Scheduler"] = p.Scheduler
	}
	if p.CFGScale != 0 {
		params["CFG Scale"] = fmt.Sprintf("%.1f", p.CFGScale)
	}
	if p.DistilledCFG != 0 {
		params["Distilled CFG Scale"] = fmt.Sprintf("%.1f", p.DistilledCFG)
	}
	if p.Seed != 0 {
		params["Seed"] = fmt.Sprintf("%d", p.Seed)
	}
	if p.Width != 0 && p.Height != 0 {
		params["Size"] = fmt.Sprintf("%dx%d", p.Width, p.Height)
	}
	if p.ModelHash != "" {
		params["Model hash"] = p.ModelHash
	}
	if p.Model != "" {
		params["Model"] = p.Model
	}
	if p.VAE != "" {
		params["VAE"] = p.VAE
	}
	if p.ClipSkip != 0 {
		params["Clip Skip"] = fmt.Sprintf("%d", p.ClipSkip)
	}
	if p.DenoisingStr != 0 {
		params["Denoising strength"] = fmt.Sprintf("%.2f", p.DenoisingStr)
	}
	if p.FaceRestorer != "" {
		params["Face Restoration"] = p.FaceRestorer
	}
	if p.HiresUpscale != 0 {
		params["Hires upscale"] = fmt.Sprintf("%.1f", p.HiresUpscale)
	}
	if p.HiresUpscaler != "" {
		params["Hires upscaler"] = p.HiresUpscaler
	}
	if p.HiresSteps != 0 {
		params["Hires steps"] = fmt.Sprintf("%d", p.HiresSteps)
	}
	if p.Version != "" {
		params["Version"] = p.Version
	}
	if len(p.LoRAs) > 0 {
		var parts []string
		for _, l := range p.LoRAs {
			if l.Weight != 0 {
				parts = append(parts, l.Name+":"+fmt.Sprintf("%.2f", l.Weight))
			} else {
				parts = append(parts, l.Name)
			}
		}
		params["LoRA"] = strings.Join(parts, ", ")
	}
	if p.SourceTool != "" {
		params["SourceTool"] = p.SourceTool
	}
	for k, v := range p.Extra {
		params[k] = v
	}

	result["params"] = params
	result["raw"] = map[string]interface{}{}
	return result
}
