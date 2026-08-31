package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// ==================== MP4 QuickTime keyed metadata 测试（ComfyUI / MiniMax 等） ====================

// 构造与 MiniMax H3 视频一致的 moov/udta/meta(keys+ilst) keyed metadata 结构

func mp4Box(btype string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b, uint32(8+len(payload)))
	copy(b[4:8], btype)
	copy(b[8:], payload)
	return b
}

func mp4KeyedMetaFile(t *testing.T, values map[int][]byte, names []string) string {
	t.Helper()

	// keys box（full box: version/flags + entry_count + entries）
	keysP := make([]byte, 8)
	binary.BigEndian.PutUint32(keysP[4:], uint32(len(names)))
	for _, name := range names {
		entry := make([]byte, 4+len(name))
		binary.BigEndian.PutUint32(entry, uint32(4+len(name)))
		copy(entry[4:], name)
		keysP = append(keysP, entry...)
	}
	keysBox := mp4Box("keys", keysP)

	// ilst box（子项 type 为 4 字节大端索引，内含 data box）
	var ilstP []byte
	for idx, val := range values {
		// data atom: version/flags(4, flags 为类型码 1=UTF-8) + locale(4) + value
		dataP := make([]byte, 8+len(val))
		binary.BigEndian.PutUint32(dataP, 1)
		copy(dataP[8:], val)
		dataBox := mp4Box("data", dataP)
		item := make([]byte, 8+len(dataBox))
		binary.BigEndian.PutUint32(item, uint32(8+len(dataBox)))
		binary.BigEndian.PutUint32(item[4:], uint32(idx))
		copy(item[8:], dataBox)
		ilstP = append(ilstP, item...)
	}
	ilstBox := mp4Box("ilst", ilstP)

	// meta（full box，前 4 字节 version/flags）
	metaP := make([]byte, 4)
	metaP = append(metaP, keysBox...)
	metaP = append(metaP, ilstBox...)
	metaBox := mp4Box("meta", metaP)

	udtaBox := mp4Box("udta", metaBox)
	moovBox := mp4Box("moov", udtaBox)

	path := filepath.Join(t.TempDir(), "test.mp4")
	content := append(mp4Box("ftyp", make([]byte, 24)), moovBox...)
	content = append(content, mp4Box("mdat", make([]byte, 16))...)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const minimaxPromptJSON = `{
  "136": {"class_type": "MiniMaxH3ReferenceToVideo", "inputs": {"prompt": ["138", 0], "clip": ["128", 0], "vae": ["119", 0]}},
  "138": {"class_type": "PrimitiveStringMultiline", "inputs": {"value": "<Subject 1> keep identity, dancing"}},
  "127": {"class_type": "UNETLoader", "inputs": {"unet_name": "minimax_h3_ref2va_pruned_int8_convrot.safetensors"}},
  "123": {"class_type": "KSamplerSelect", "inputs": {"sampler_name": "euler"}},
  "124": {"class_type": "BasicScheduler", "inputs": {"scheduler": "beta", "steps": 8}},
  "129": {"class_type": "RandomNoise", "inputs": {"noise_seed": 1029437820840887}}
}`

const minimaxWorkflowJSON = `{"nodes": [{"id": 123, "type": "KSamplerSelect", "widgets_values": ["euler"]}]}`

func TestExtractMP4KeyedMetadata(t *testing.T) {
	path := mp4KeyedMetaFile(t, map[int][]byte{
		1: []byte(minimaxPromptJSON),
		2: []byte(minimaxWorkflowJSON),
		3: []byte("Lavf62.3.100"),
	}, []string{"mdtaprompt", "mdtaworkflow", "mdtaencoder"})

	kv := extractMP4KeyedMetadata(path)
	if len(kv) != 3 {
		t.Fatalf("期望 3 个键，得到 %d: %v", len(kv), kv)
	}
	if kv["prompt"] != minimaxPromptJSON {
		t.Error("prompt 键值不匹配")
	}
	if kv["workflow"] != minimaxWorkflowJSON {
		t.Error("workflow 键值不匹配")
	}
	if kv["encoder"] != "Lavf62.3.100" {
		t.Errorf("encoder 应为 Lavf62.3.100，得到 %q", kv["encoder"])
	}
}

func TestParseMetadataFast_MiniMaxVideo(t *testing.T) {
	path := mp4KeyedMetaFile(t, map[int][]byte{
		1: []byte(minimaxPromptJSON),
		2: []byte(minimaxWorkflowJSON),
		3: []byte("Lavf62.3.100"),
	}, []string{"mdtaprompt", "mdtaworkflow", "mdtaencoder"})

	a := &App{}
	meta := a.ParseMetadataFast(path)
	if meta == nil {
		t.Fatal("期望能解析出视频元数据")
	}
	prompt, _ := meta["prompt"].(string)
	if prompt != "<Subject 1> keep identity, dancing" {
		t.Errorf("prompt 提取不正确: %q", prompt)
	}
	params, _ := meta["params"].(map[string]string)
	if params["Model"] != "minimax_h3_ref2va_pruned_int8_convrot" {
		t.Errorf("Model 提取不正确: %q", params["Model"])
	}
	if params["Sampler"] != "euler" || params["Scheduler"] != "beta" {
		t.Errorf("Sampler/Scheduler 提取不正确: %v", params)
	}
	if params["Seed"] != "1029437820840887" {
		t.Errorf("Seed 提取不正确: %v", params["Seed"])
	}
	if params["SourceTool"] != "ComfyUI" {
		t.Errorf("SourceTool 应为 ComfyUI: %q", params["SourceTool"])
	}
}

// 无 keyed metadata 的普通 MP4 返回 nil
func TestParseMetadataFast_PlainMP4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.mp4")
	content := append(mp4Box("ftyp", make([]byte, 24)), mp4Box("mdat", make([]byte, 16))...)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	a := &App{}
	if meta := a.ParseMetadataFast(path); meta != nil {
		t.Errorf("普通 MP4 不应有元数据: %v", meta)
	}
}
