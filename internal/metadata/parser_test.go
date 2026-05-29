package metadata

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func pretty(v interface{}) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

// ==================== SD WebUI 测试 ====================

func TestSDWebUI_WithLoRA(t *testing.T) {
	raw := `This is a high-resolution photograph featuring a young woman...
<lora:Brooke Shields_Flux_V1-000002:1>
Steps: 30, Sampler: Euler, Schedule type: SGM Uniform, CFG scale: 1, Distilled CFG Scale: 3, Seed: 1606328335, Size: 1056x1408, Model hash: bea01d51bd, Model: flux1-dev-bnb-nf4-v2, Lora hashes: "Brooke Shields_Flux_V1-000002: 8677b0d4a9e2"`

	p := parseParamText(raw)
	if p == nil {
		t.Fatal("should not be nil")
	}

	fmt.Println("=== SD WebUI + LoRA ===")
	fmt.Println(pretty(p))

	assertEq(t, "Euler", p.Sampler, "Sampler")
	assertEq(t, "SGM Uniform", p.Scheduler, "Scheduler")
	assertEq(t, 30, p.Steps, "Steps")
	assertEq(t, 1.0, p.CFGScale, "CFGScale")
	assertEq(t, 3.0, p.DistilledCFG, "DistilledCFG")
	assertEq(t, int64(1606328335), p.Seed, "Seed")
	assertEq(t, 1056, p.Width, "Width")
	assertEq(t, 1408, p.Height, "Height")
	assertEq(t, "flux1-dev-bnb-nf4-v2", p.Model, "Model")
	assertEq(t, "bea01d51bd", p.ModelHash, "ModelHash")
	if len(p.LoRAs) == 0 {
		t.Error("expected LoRA entries")
	} else {
		assertEq(t, "Brooke Shields_Flux_V1-000002", p.LoRAs[0].Name, "LoRA Name")
		assertEq(t, 1.0, p.LoRAs[0].Weight, "LoRA Weight")
		assertEq(t, "8677b0d4a9e2", p.LoRAs[0].Hash, "LoRA Hash")
	}
}

func TestSDWebUI_Hires_MultiParam(t *testing.T) {
	raw := `score_9, score_8_up, best quality,<lora:elsa_xl4:0.95>
Negative prompt: EasyNegative,paintings, sketches,
Steps: 30, Sampler: DPM++ 2M, Schedule type: Karras, CFG scale: 6, Seed: 1797644848, Size: 768x1024, Model hash: 8cd86b11ad, Model: ponyDiffusionV6XL_v6StartWithThisOne, Denoising strength: 0.25, Clip skip: 2, Hires CFG Scale: 5, Hires upscale: 1.5, Hires steps: 30, Hires upscaler: 4x_foolhardy_Remacri, Lora hashes: "elsa_xl4: 32422982cceb", Version: f2.0.1v1.10.1`

	p := parseParamText(raw)
	fmt.Println("=== SD WebUI Hires + ClipSkip ===")
	fmt.Println(pretty(p))

	assertEq(t, "Karras", p.Scheduler, "Scheduler")
	assertEq(t, 2, p.ClipSkip, "ClipSkip")
	assertEq(t, 0.25, p.DenoisingStr, "DenoisingStr")
	assertEq(t, 1.5, p.HiresUpscale, "HiresUpscale")
	assertEq(t, 30, p.HiresSteps, "HiresSteps")
	assertEq(t, "4x_foolhardy_Remacri", p.HiresUpscaler, "HiresUpscaler")
	assertEq(t, 5.0, p.HiresCFGScale, "HiresCFGScale")
	assertEq(t, "f2.0.1v1.10.1", p.Version, "Version")
	assertEq(t, "EasyNegative,paintings, sketches,", p.NegativePrompt, "NegativePrompt")
	if len(p.LoRAs) == 0 {
		t.Error("expected LoRA")
	} else {
		assertEq(t, "elsa_xl4", p.LoRAs[0].Name, "LoRA Name")
		assertEq(t, 0.95, p.LoRAs[0].Weight, "LoRA Weight")
		assertEq(t, "32422982cceb", p.LoRAs[0].Hash, "LoRA Hash")
	}
}

func TestSDWebUI_AddNet(t *testing.T) {
	raw := `8k, RAW photo, portrait, <lora:koreanDollLikeness_v15:0>, <lora:taiwanDollLikeness_v10:0>, <lora:breastinClass:0.7>
Negative prompt: paintings, cartoon,
Steps: 20, Sampler: DPM++ SDE Karras, CFG scale: 7, Seed: 3708370322, Face restoration: CodeFormer, Size: 512x968, Model hash: fc2511737a, Model: chilloutmix_NiPrunedFp32Fix, Denoising strength: 0.75, Clip skip: 2, AddNet Enabled: True, AddNet Module 1: LoRA, AddNet Model 1: gakkiAragakiYui_v2(04bbe4c40f7d), AddNet Weight A 1: 1, AddNet Weight B 1: 1`

	p := parseParamText(raw)
	fmt.Println("=== SD WebUI AddNet LoRA ===")
	fmt.Println(pretty(p))

	assertEq(t, "CodeFormer", p.FaceRestorer, "FaceRestorer")
	assertEq(t, "chilloutmix_NiPrunedFp32Fix", p.Model, "Model")
	if len(p.LoRAs) < 3 {
		t.Errorf("expected >=3 LoRAs, got %d", len(p.LoRAs))
	}
}

// ==================== SwarmUI 测试 ====================

func TestSwarmUI_JSON(t *testing.T) {
	raw := `{
  "sui_image_params": {
    "prompt": "The photograph depicts an adult woman...",
    "model": "Flux-svdq/transformer_blocks",
    "seed": 1301058440,
    "steps": 30,
    "cfgscale": 1.0,
    "aspectratio": "Custom",
    "width": 1216,
    "height": 1536,
    "scheduler": "sgm_uniform",
    "automaticvae": true,
    "loras": ["flux/Gemma_Chan_Flux_V1-000002"],
    "loraweights": ["1.1"],
    "negativeprompt": "",
    "swarm_version": "0.9.6.0"
  },
  "sui_extra_data": {
    "date": "2025-05-20",
    "prep_time": "0.00 sec",
    "generation_time": "34.08 sec"
  },
  "sui_models": [
    {
      "name": "Flux-svdq/transformer_blocks.safetensors",
      "param": "model",
      "hash": "0xaaee67f1"
    },
    {
      "name": "flux/Gemma_Chan_Flux_V1-000002.safetensors",
      "param": "loras",
      "hash": "0xa53c55a0"
    }
  ]
}`

	p := parseParamText(raw)
	fmt.Println("=== SwarmUI JSON ===")
	fmt.Println(pretty(p))

	assertEq(t, "SwarmUI", p.SourceTool, "SourceTool")
	assertEq(t, "Flux-svdq/transformer_blocks", p.Model, "Model")
	assertEq(t, int64(1301058440), p.Seed, "Seed")
	assertEq(t, 30, p.Steps, "Steps")
	assertEq(t, 1.0, p.CFGScale, "CFGScale")
	assertEq(t, 1216, p.Width, "Width")
	assertEq(t, 1536, p.Height, "Height")
	assertEq(t, "sgm_uniform", p.Scheduler, "Scheduler")
	assertEq(t, true, p.AutomaticVAE, "AutomaticVAE")
	assertEq(t, "0.9.6.0", p.SwarmVersion, "SwarmVersion")
	assertEq(t, "2025-05-20", p.Date, "Date")
	assertEq(t, "34.08 sec", p.GenerationTime, "GenerationTime")
	if len(p.LoRAs) == 0 {
		t.Error("expected LoRA")
	} else {
		assertEq(t, "Gemma_Chan_Flux_V1-000002", p.LoRAs[0].Name, "LoRA Name")
		assertEq(t, 1.1, p.LoRAs[0].Weight, "LoRA Weight")
	}
}

func TestSwarmUI_Refiner(t *testing.T) {
	raw := `{
  "sui_image_params": {
    "prompt": "masterpiece, best quality",
    "negativeprompt": "worst quality",
    "model": "SDXL/noob/noobaiXLNAIXL_vPred10Version",
    "seed": 570186683,
    "steps": 30,
    "cfgscale": 4.0,
    "aspectratio": "Custom",
    "width": 1216,
    "height": 832,
    "sampler": "euler",
    "scheduler": "sgm_uniform",
    "refinercontrolpercentage": 0.3,
    "refinermethod": "PostApply",
    "refinerupscale": 1.5,
    "refinerupscalemethod": "model-4x_IllustrationJaNai_V1_ESRGAN_135k.pth",
    "automaticvae": true,
    "loras": ["SD_XL/noobai/test/Shashara_noob-vpred_V1-000001"],
    "loraweights": ["1"],
    "vae": "SDXL-VAE/sdxl.vae",
    "swarm_version": "0.9.5.0"
  },
  "sui_extra_data": {
    "date": "2025-02-20",
    "generation_time": "34.11 sec"
  }
}`

	p := parseParamText(raw)
	fmt.Println("=== SwarmUI Refiner ===")
	fmt.Println(pretty(p))

	assertEq(t, 0.3, p.RefinerControl, "RefinerControl")
	assertEq(t, "PostApply", p.RefinerMethod, "RefinerMethod")
	assertEq(t, 1.5, p.RefinerUpscale, "RefinerUpscale")
	assertEq(t, "model-4x_IllustrationJaNai_V1_ESRGAN_135k.pth", p.RefinerUpscaleMethod, "RefinerUpscaleMethod")
	assertEq(t, "SDXL-VAE/sdxl.vae", p.VAE, "VAE")
}

// ==================== ComfyUI 测试 ====================

func TestComfyUI_Basic(t *testing.T) {
	raw := `{
  "3": {
    "class_type": "CLIPTextEncode",
    "inputs": {"text": "a beautiful landscape, mountains, sunset"}
  },
  "6": {
    "class_type": "CLIPTextEncode",
    "inputs": {"text": "ugly, blurry, low quality"}
  },
  "10": {
    "class_type": "KSampler",
    "inputs": {"seed": 42, "steps": 20, "cfg": 7.0, "sampler_name": "euler", "scheduler": "normal", "denoise": 1.0}
  }
}`

	// ComfyUI 走的路径是 JSON 顶层有 "prompt" key
	comfyRaw := fmt.Sprintf(`{"prompt": %s}`, raw)

	p := parseParamText(comfyRaw)
	fmt.Println("=== ComfyUI Basic ===")
	fmt.Println(pretty(p))

	assertEq(t, "ComfyUI", p.SourceTool, "SourceTool")
	assertEq(t, int64(42), p.Seed, "Seed")
	assertEq(t, 20, p.Steps, "Steps")
	assertEq(t, 7.0, p.CFGScale, "CFGScale")
	assertEq(t, "euler", p.Sampler, "Sampler")
	assertEq(t, "normal", p.Scheduler, "Scheduler")
}

// ==================== XMP 测试 ====================

func TestXMP_WithSDPrompt(t *testing.T) {
	xmp := `<x:xmpmeta xmlns:x="adobe:ns:meta/" x:xmptk="XMP Core 6.0.0">
   <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
      <rdf:Description rdf:about=""
            xmlns:dc="http://purl.org/dc/elements/1.1/">
         <dc:description>
            <rdf:Alt>
               <rdf:li xml:lang="x-default">gzgvr Very sexy woman, copper color hair, blue eyes&#xA;-&lt;sdxl_cyberrealistic_simpleneg&gt; elongated torso&#xA;Steps: 25, Sampler: DPM++ 2M, CFG scale: 7, Seed: 999, Size: 1024x1536, Model: sdxl_base</rdf:li>
            </rdf:Alt>
         </dc:description>
      </rdf:Description>
   </rdf:RDF>
</x:xmpmeta>`

	p := parseXMPText(xmp)
	fmt.Println("=== XMP + embedded SD params ===")
	fmt.Println(pretty(p))

	if p.Steps != 0 {
		assertEq(t, 25, p.Steps, "Steps from XMP dc:description")
		assertEq(t, int64(999), p.Seed, "Seed from XMP")
	}
}

// ==================== Midjourney 测试 ====================

func TestMidjourney_v7(t *testing.T) {
	desc := `photograph of Queen Elsa looking directly at the viewer with a tender smile, standing beside the bed of sleeping Princess Anna, soft morning light gently illuminating the room, Anna with auburn hair tousled on the pillow, serene expression, Elsa with ice-blonde braid, pale blue gown with delicate embellishments, crystalline accents on braid, lavender and pink patterned wallpaper, heavy draped mauve curtains, dark wood furniture, sophisticated bedchamber, detailed and romantic scene, soft shadows, intimate and evocative mood, captured with a Canon EOS 5D Mark IV, 50mm lens, f/2, ISO 400, warm color palette, painterly style, inspired by classic Disney animation, highres, fine art. --ar 3:4 --raw --stylize 0 --v 7 Job ID: 8323d5b0-1888-4fd7-a239-9d4bb8222ce3`

	if !IsMidjourneyDescription(desc) {
		t.Fatal("IsMidjourneyDescription should return true")
	}

	chunks := map[string]string{
		"Description":   desc,
		"Author":        "u1431619417",
		"Creation Time": "Thu, 11 Sep 2025 09:48:26 GMT",
	}

	p := ParseTextChunks(chunks)
	fmt.Println("=== Midjourney v7 ===")
	fmt.Println(pretty(p))

	assertEq(t, "Midjourney", p.SourceTool, "SourceTool")
	assertEq(t, "u1431619417", p.Author, "Author")
	assertEq(t, "8323d5b0-1888-4fd7-a239-9d4bb8222ce3", p.JobID, "JobID")
	assertEq(t, "7", p.MJVersion, "MJVersion")
	assertEq(t, "Midjourney v7", p.Model, "Model")
	assertEq(t, "3:4", p.AspectRatio, "AspectRatio")
	assertEq(t, true, p.Raw, "Raw")
	assertEq(t, 0, p.Stylize, "Stylize")

	if strings.Contains(p.Prompt, "--ar") {
		t.Error("Prompt should not contain --ar parameter")
	}
	if strings.Contains(p.Prompt, "Job ID") {
		t.Error("Prompt should not contain Job ID")
	}
	if !strings.HasSuffix(strings.TrimSpace(p.Prompt), "fine art.") {
		t.Errorf("Prompt should end with 'fine art.', got: ...%s", p.Prompt[max(0, len(p.Prompt)-30):])
	}
}

func TestMidjourney_NoParam(t *testing.T) {
	chunks := map[string]string{
		"Description": `a serene mountain landscape with snow --ar 16:9 --no trees, people --stylize 200 --chaos 10 --v 6.1 Job ID: aaaabbbb-cccc-dddd-eeee-ffffaaaabbbb`,
	}

	p := ParseTextChunks(chunks)
	fmt.Println("=== Midjourney --no param ===")
	fmt.Println(pretty(p))

	assertEq(t, "trees, people", p.No, "--no value")
	assertEq(t, "trees, people", p.NegativePrompt, "NegativePrompt from --no")
	assertEq(t, 200, p.Stylize, "Stylize")
	assertEq(t, 10, p.Chaos, "Chaos")
	assertEq(t, "6.1", p.MJVersion, "MJVersion")
	assertEq(t, "16:9", p.AspectRatio, "AspectRatio")
}

func TestMidjourney_Niji(t *testing.T) {
	chunks := map[string]string{
		"Description": `anime girl with cherry blossoms, studio ghibli style --ar 2:3 --niji 6 --stylize 200`,
	}

	p := ParseTextChunks(chunks)
	fmt.Println("=== Midjourney Niji ===")
	fmt.Println(pretty(p))

	assertEq(t, "Niji 6", p.Model, "Model (Niji)")
	assertEq(t, "niji-6", p.MJVersion, "MJVersion (Niji)")
	assertEq(t, 200, p.Stylize, "Stylize")
}

func TestMidjourney_Chaos_Seed(t *testing.T) {
	chunks := map[string]string{
		"Description": `vibrant fantasy forest, glowing mushrooms, ethereal mist --ar 16:9 --no text, watermark, signature --chaos 30 --seed 42 --stylize 750 --v 6.1`,
	}

	p := ParseTextChunks(chunks)
	fmt.Println("=== Midjourney --no / --chaos / --seed ===")
	fmt.Println(pretty(p))

	assertEq(t, 30, p.Chaos, "Chaos")
	assertEq(t, int64(42), p.Seed, "Seed")
	assertEq(t, 750, p.Stylize, "Stylize")
	if !strings.Contains(p.No, "text") {
		t.Errorf("No should contain 'text', got: %s", p.No)
	}
	if p.NegativePrompt == "" {
		t.Error("NegativePrompt should be populated from --no")
	}
}

func TestIsMidjourneyDescription(t *testing.T) {
	cases := []struct {
		desc     string
		expected bool
	}{
		{"a beautiful landscape --ar 3:4 --v 7", true},
		{"portrait --stylize 500", true},
		{"test Job ID: 8323d5b0-1888-4fd7-a239-9d4bb8222ce3", true},
		{"anime girl --niji 6", true},
		{"Steps: 30, Sampler: Euler, CFG scale: 7", false},
		{"", false},
		{"just a normal description without params", false},
	}
	for _, c := range cases {
		got := IsMidjourneyDescription(c.desc)
		if got != c.expected {
			short := c.desc
			if len(short) > 40 {
				short = short[:40]
			}
			t.Errorf("IsMidjourneyDescription(%q) = %v, want %v", short, got, c.expected)
		}
	}
}

// ==================== Format Detection 测试 ====================

func TestFormatDetection_Midjourney(t *testing.T) {
	chunks := map[string]string{
		"Description": "a beautiful landscape --ar 3:4 --v 7",
	}
	p := ParseTextChunks(chunks)
	if p == nil {
		t.Fatal("expected result")
	}
	assertEq(t, "Midjourney", p.SourceTool, "SourceTool")
}

func TestFormatDetection_SwarmUI(t *testing.T) {
	chunks := map[string]string{
		"parameters": `{"sui_image_params": {"prompt": "test", "seed": 1}}`,
	}
	p := ParseTextChunks(chunks)
	if p == nil {
		t.Fatal("expected result")
	}
	assertEq(t, "SwarmUI", p.SourceTool, "SourceTool")
}

func TestFormatDetection_ComfyUI(t *testing.T) {
	chunks := map[string]string{
		"prompt": `{"3": {"class_type": "KSampler", "inputs": {"seed": 1}}}`,
	}
	p := ParseTextChunks(chunks)
	if p == nil {
		t.Fatal("expected result")
	}
	assertEq(t, "ComfyUI", p.SourceTool, "SourceTool")
}

func TestFormatDetection_SDWebUI(t *testing.T) {
	chunks := map[string]string{
		"parameters": "a cat\nSteps: 30, Sampler: Euler, CFG scale: 7, Seed: 123, Size: 512x512, Model: test",
	}
	p := ParseTextChunks(chunks)
	if p == nil {
		t.Fatal("expected result")
	}
	assertEq(t, "SD WebUI", p.SourceTool, "SourceTool")
}

// ==================== 工具 ====================

func assertEq(t *testing.T, expected, actual interface{}, label string) {
	t.Helper()
	expStr := fmt.Sprintf("%v", expected)
	actStr := fmt.Sprintf("%v", actual)
	if expStr != actStr {
		t.Errorf("%s: expected %v, got %v", label, expected, actual)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
