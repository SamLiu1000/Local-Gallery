package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
)

// ==================== 头部极速取尺寸 ====================
// 只读文件头部（≤1MB 缓冲）解析宽高与 EXIF 方向，不解码图像、不读整个文件。
// 单张成本 <1ms（顺序小读），对比旧路径（DecodeConfig + goexif 全量解析 + vips 兜底，
// 慢盘实测 ~920ms/张）。解析失败自动回退旧路径，行为不变。

const fastDimReadCap = 1 << 20 // 最多读 1MB 头部（覆盖 Photoshop APP13 等大段在 SOF 之前的场景）

// fastImageDimensions 从文件头部解析宽高（含 EXIF 方向交换）。
// 返回 (0,0) 表示无法用快路径确定，调用方应回退慢路径。
func fastImageDimensions(filePath string) (int, int) {
	f, err := os.Open(filePath)
	if err != nil {
		return 0, 0
	}
	defer f.Close()

	// 一次性读头部：64KB 起步，SOF 不在头部时按需扩到上限
	buf := make([]byte, 64*1024)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return 0, 0
	}
	if n < 16 {
		return 0, 0
	}
	if (n == len(buf) || err == nil) && len(buf) < fastDimReadCap {
		// 头部读满且未到上限 → JPEG 可能需要更多内容（大 APP 段），扩读
		extra := make([]byte, fastDimReadCap-len(buf))
		m, _ := io.ReadFull(f, extra)
		buf = append(buf[:n], extra[:m]...)
		n = len(buf)
	}
	data := buf[:n]

	w, h := fastDimsFromHead(data)
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	// EXIF 方向 5-8（旋转 90°/270°）→ 交换宽高
	if orient := fastEXIFFromJPEGHead(data); orient >= 5 && orient <= 8 {
		w, h = h, w
	}
	return w, h
}

// fastDimsFromHead 按格式魔数分发。不认识或截断 → (0,0)。
func fastDimsFromHead(d []byte) (int, int) {
	switch {
	case bytes.HasPrefix(d, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		// PNG：IHDR 固定在 16..24，大端
		if len(d) < 24 {
			return 0, 0
		}
		return int(binary.BigEndian.Uint32(d[16:20])), int(binary.BigEndian.Uint32(d[20:24]))

	case bytes.HasPrefix(d, []byte("GIF8")):
		// GIF：逻辑屏幕描述符 6..10，小端
		if len(d) < 10 {
			return 0, 0
		}
		return int(binary.LittleEndian.Uint16(d[6:8])), int(binary.LittleEndian.Uint16(d[8:10]))

	case bytes.HasPrefix(d, []byte("BM")):
		// BMP：DIB 头 18..26，小端（可能是负值表示上下翻转，取绝对值）
		if len(d) < 26 {
			return 0, 0
		}
		return abs32(binary.LittleEndian.Uint32(d[18:22])), abs32(binary.LittleEndian.Uint32(d[22:26]))

	case bytes.HasPrefix(d, []byte("RIFF")) && len(d) >= 30 && bytes.Equal(d[8:12], []byte("WEBP")):
		return fastWebPDims(d)

	case d[0] == 0xFF && d[1] == 0xD8:
		return fastJPEGDims(d)
	}
	return 0, 0
}

// fastWebPDims 解析 VP8X / VP8 / VP8L 三种子格式的画布尺寸
func fastWebPDims(d []byte) (int, int) {
	if len(d) < 30 {
		return 0, 0
	}
	switch {
	case bytes.Equal(d[12:16], []byte("VP8X")):
		// 24..26 宽-1，27..29 高-1（24 位小端，各 +1）
		w := 1 + (int(d[24]) | int(d[25])<<8 | int(d[26])<<16)
		h := 1 + (int(d[27]) | int(d[28])<<8 | int(d[29])<<16)
		return w, h
	case bytes.Equal(d[12:16], []byte("VP8 ")):
		// 有损：20..23 帧头，23..26 起始码 9D 01 2A，26..30 宽高（14 位小端）
		if len(d) < 30 || d[23] != 0x9d || d[24] != 0x01 || d[25] != 0x2a {
			return 0, 0
		}
		w := int(binary.LittleEndian.Uint16(d[26:28])) & 0x3fff
		h := int(binary.LittleEndian.Uint16(d[28:30])) & 0x3fff
		return w, h
	case bytes.Equal(d[12:16], []byte("VP8L")):
		// 无损：21 起 0x2F 标志，14 位宽-1 与 14 位高-1 打包在 20..24 位流
		if len(d) < 25 || d[20] != 0x2f {
			return 0, 0
		}
		bits := uint32(d[21]) | uint32(d[22])<<8 | uint32(d[23])<<16 | uint32(d[24])<<24
		w := int(bits&0x3FFF) + 1
		h := int((bits>>14)&0x3FFF) + 1
		return w, h
	}
	return 0, 0
}

// jpegSOF 标记集合：0xC0-0xCF 除 DHT(0xC4)/JPG(0xC8)/DAC(0xCC)
func isSOFC5(m byte) bool {
	return m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC
}

// fastJPEGDims 扫描段标记找 SOF（含渐进式 SOF2）。缓冲耗尽/进入熵编码 → (0,0) 回退。
func fastJPEGDims(d []byte) (int, int) {
	pos := 2
	for pos+4 <= len(d) {
		if d[pos] != 0xFF {
			// 段结构破坏（某些编码器有 padding 垃圾），跳过一个字节继续找
			pos++
			continue
		}
		m := d[pos+1]
		if m == 0xFF { // 填充字节
			pos++
			continue
		}
		if m == 0xD8 || m == 0x01 || (m >= 0xD0 && m <= 0xD7) { // 无长度段
			pos += 2
			continue
		}
		if pos+4 > len(d) {
			return 0, 0
		}
		segLen := int(binary.BigEndian.Uint16(d[pos+2 : pos+4]))
		if segLen < 2 {
			return 0, 0
		}
		if isSOFC5(m) {
			if pos+9 > len(d) {
				return 0, 0
			}
			h := int(binary.BigEndian.Uint16(d[pos+5 : pos+7]))
			w := int(binary.BigEndian.Uint16(d[pos+7 : pos+9]))
			return w, h
		}
		if m == 0xDA { // SOS：进入熵编码数据，SOF 不会再出现
			return 0, 0
		}
		pos += 2 + segLen
	}
	return 0, 0
}

// fastEXIFFromJPEGHead 从 JPEG 头部 APP1(Exif) 段解析 Orientation（0 = 无）。
// 极简 TIFF IFD0 遍历，只读 tag 0x0112；结构异常即放弃。
func fastEXIFFromJPEGHead(d []byte) int {
	pos := 2
	for pos+4 <= len(d) {
		if d[pos] != 0xFF {
			pos++
			continue
		}
		m := d[pos+1]
		if m == 0xFF {
			pos++
			continue
		}
		if m == 0xD8 || m == 0x01 || (m >= 0xD0 && m <= 0xD7) {
			pos += 2
			continue
		}
		if pos+4 > len(d) {
			return 0
		}
		segLen := int(binary.BigEndian.Uint16(d[pos+2 : pos+4]))
		if segLen < 2 {
			return 0
		}
		if m == 0xE1 && pos+10 <= len(d) && bytes.HasPrefix(d[pos+4:pos+10], []byte("Exif\x00\x00")) {
			return fastTIFFOrientation(d[pos+4 : minInt(pos+2+segLen, len(d))])
		}
		if m == 0xDA || isSOFC5(m) {
			// EXIF 一定在 SOF 前（APP 段区）；到了还没找到就没有了
			return 0
		}
		pos += 2 + segLen
	}
	return 0
}

// fastTIFFOrientation 极简 TIFF 头 + IFD0 遍历找 tag 0x0112。
func fastTIFFOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 0
	}
	var bo binary.ByteOrder = binary.LittleEndian
	switch {
	case tiff[0] == 'I' && tiff[1] == 'I':
		bo = binary.LittleEndian
	case tiff[0] == 'M' && tiff[1] == 'M':
		bo = binary.BigEndian
	default:
		return 0
	}
	if bo.Uint16(tiff[2:4]) != 42 {
		return 0
	}
	ifdOff := int(bo.Uint32(tiff[4:8]))
	if ifdOff+2 > len(tiff) {
		return 0
	}
	count := int(bo.Uint16(tiff[ifdOff : ifdOff+2]))
	for i := 0; i < count; i++ {
		e := ifdOff + 2 + i*12
		if e+12 > len(tiff) {
			return 0
		}
		if bo.Uint16(tiff[e:e+2]) == 0x0112 { // Orientation
			return int(bo.Uint16(tiff[e+8 : e+10]))
		}
	}
	return 0
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func abs32(v uint32) int {
	x := int(v)
	if x < 0 {
		return -x
	}
	return x
}
