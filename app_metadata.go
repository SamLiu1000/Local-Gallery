package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"github.com/rwcarlsen/goexif/exif"

	"local-gallery/internal/metadata"
)

// extractCameraEXIF 用 goexif 从已读取的 JPEG 字节中提取标准相机 EXIF 标签
func extractCameraEXIF(data []byte) map[string]string {
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
		// 注意：v.Rat(0) 在 goexif 库内部可能 panic，需保护
		var r *big.Rat
		func() {
			defer func() {
				recover() // 忽略 panic
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
			result["测光模式"] = meteringModeName(n)
		}
	}
	if v, err := x.Get(exif.ExposureProgram); err == nil {
		if n, err := v.Int(0); err == nil {
			result["曝光程序"] = exposureProgramName(n)
		}
	}
	if v, err := x.Get(exif.WhiteBalance); err == nil {
		if n, err := v.Int(0); err == nil {
			result["白平衡"] = whiteBalanceName(n)
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

func meteringModeName(n int) string {
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

func exposureProgramName(n int) string {
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

func whiteBalanceName(n int) string {
	switch n {
	case 0:
		return "自动"
	case 1:
		return "手动"
	default:
		return fmt.Sprintf("未知(%d)", n)
	}
}

// ParseMetadataFast 快速解析 PNG 元数据（逐块流式读取）
// 委托给 metadata.ParseTextChunks，返回旧版 map 格式以保持向后兼容
func (a *App) ParseMetadataFast(filePath string) map[string]interface{} {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".png":
		return a.parsePNGToLegacy(filePath)
	case ".jpg", ".jpeg":
		return a.parseJPEGToLegacy(filePath)
	case ".webp":
		return a.parseWebPToLegacy(filePath)
	default:
		return nil
	}
}

// ParseMetadata 旧版入口，保持向后兼容
func (a *App) ParseMetadata(filePath string) map[string]interface{} {
	return a.ParseMetadataFast(filePath)
}

// ==================== PNG 解析（流式读取 text chunks） ====================

func (a *App) parsePNGToLegacy(filePath string) map[string]interface{} {
	f, err := os.Open(filePath)
	if err != nil {
		return nil
	}
	defer f.Close()

	sig := make([]byte, 8)
	if _, err := io.ReadFull(f, sig); err != nil {
		return nil
	}
	if len(sig) < 4 || string(sig[1:4]) != "PNG" {
		return nil
	}

	textChunks := make(map[string]string)

	for {
		var length uint32
		if err := binary.Read(f, binary.BigEndian, &length); err != nil {
			break
		}

		chunkType := make([]byte, 4)
		if _, err := io.ReadFull(f, chunkType); err != nil {
			break
		}

		chunkName := string(chunkType)

		// 跳过 IDAT 后的数据块
		if chunkName == "IDAT" || chunkName == "IEND" {
			break
		}

		if chunkName == "tEXt" || chunkName == "iTXt" || chunkName == "zTXt" {
			data := make([]byte, length)
			if _, err := io.ReadFull(f, data); err != nil {
				break
			}

			switch chunkName {
			case "zTXt":
				key, _, compressed := splitTextChunk(data)
				if key != "" && compressed != nil {
					decompressed := decompressZlib(compressed)
					if decompressed != nil {
						textChunks[key] = string(decompressed)
					} else {
						textChunks[key] = "[zlib compressed]"
					}
				}

			case "iTXt":
				key, compressionFlag, rest := splitITextChunk(data)
				if key != "" && rest != nil {
					if compressionFlag == 1 {
						decompressed := decompressZlib(rest)
						if decompressed != nil {
							textChunks[key] = string(decompressed)
						} else {
							textChunks[key] = "[zlib compressed]"
						}
					} else {
						textChunks[key] = string(rest)
					}
				}

			default: // tEXt
				nullIdx := -1
				for i, b := range data {
					if b == 0 {
						nullIdx = i
						break
					}
				}
				if nullIdx > 0 && nullIdx+1 < len(data) {
					key := string(data[:nullIdx])
					value := string(data[nullIdx+1:])
					textChunks[key] = value
				}
			}

			_, _ = f.Seek(4, io.SeekCurrent) // skip CRC
		} else {
			_, _ = f.Seek(int64(length)+4, io.SeekCurrent)
		}
	}

	if len(textChunks) == 0 {
		return nil
	}

	parsed := metadata.ParseTextChunks(textChunks)
	if parsed == nil {
		return nil
	}
	return toLegacyWithRaw(parsed, textChunks)
}

// ==================== JPEG 解析 ====================

func (a *App) parseJPEGToLegacy(filePath string) map[string]interface{} {
	data, err := os.ReadFile(filePath)
	if err != nil || len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil
	}

	textChunks := make(map[string]string)
	offset := 2

	for offset+1 < len(data) {
		if data[offset] != 0xFF {
			break
		}
		marker := binary.BigEndian.Uint16(data[offset : offset+2])

		if marker == 0xFFE1 { // EXIF APP1
			if offset+4 < len(data) {
				length := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
				if offset+2+length <= len(data) {
					exifData := data[offset+4 : offset+2+length]
					if text := extractEXIFUserComment(exifData); text != "" {
						textChunks["parameters"] = text
					}
				}
				offset += 2 + length
			} else {
				break
			}
		} else if marker == 0xFFFE { // COM
			if offset+4 < len(data) {
				length := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
				if offset+2+length <= len(data) {
					textChunks["Comment"] = string(data[offset+4 : offset+2+length])
				}
				offset += 2 + length
			} else {
				break
			}
		} else if marker == 0xFFDA { // SOS
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

	// 提取标准相机 EXIF（goexif），合并到 textChunks 供前端展示
	hasEXIF := false
	if exifTags := extractCameraEXIF(data); exifTags != nil {
		hasEXIF = true
		for k, v := range exifTags {
			textChunks[k] = v
		}
	}

	if len(textChunks) == 0 {
		return nil
	}

	parsed := metadata.ParseTextChunks(textChunks)
	if parsed == nil && !hasEXIF {
		return nil
	}
	if parsed == nil {
		parsed = &metadata.ParsedParams{}
	}
	return toLegacyWithRaw(parsed, textChunks)
}

// ==================== WebP 解析 ====================

func (a *App) parseWebPToLegacy(filePath string) map[string]interface{} {
	data, err := os.ReadFile(filePath)
	if err != nil || len(data) < 12 {
		return nil
	}
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return nil
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
			chunkData := data[offset+8 : offset+8+chunkSize]
			if chunkType == "XMP " {
				textChunks["xmp"] = string(chunkData)
			} else {
				textChunks["parameters"] = string(chunkData)
			}
		}

		offset += 8 + chunkSize
		if chunkSize%2 != 0 {
			offset++
		}
	}

	if len(textChunks) == 0 {
		return nil
	}

	parsed := metadata.ParseTextChunks(textChunks)
	if parsed == nil {
		return nil
	}
	return toLegacyWithRaw(parsed, textChunks)
}

// ==================== EXIF / PNG chunk 辅助 ====================

// extractEXIFUserComment 从 EXIF APP1 中提取 UserComment (tag 0x9286)
func extractEXIFUserComment(exifData []byte) string {
	if len(exifData) < 14 {
		return ""
	}

	// JPEG EXIF APP1 格式: "Exif\0\0" + TIFF header
	tiffStart := 0
	if string(exifData[:6]) == "Exif\x00\x00" {
		tiffStart = 6
	}

	tiffHeader := exifData[tiffStart:]

	var byteOrder binary.ByteOrder = binary.LittleEndian
	if tiffHeader[0] == 'M' && tiffHeader[1] == 'M' {
		byteOrder = binary.BigEndian
	} else if tiffHeader[0] != 'I' || tiffHeader[1] != 'I' {
		return ""
	}
	_ = byteOrder.Uint16(tiffHeader[2:4]) // magic 0x002A

	// IFD0 offset (relative to TIFF header start)
	ifd0Offset := int(byteOrder.Uint32(tiffHeader[4:8]))
	if ifd0Offset < 0 || tiffStart+ifd0Offset+2 > len(exifData) {
		return ""
	}

	// 扫描 IFD0 和 ExifIFD 子 IFD 中的 UserComment
	if result := scanIFDForUserComment(exifData, tiffStart+ifd0Offset, byteOrder, tiffStart); result != "" {
		return result
	}

	// 如果 IFD0 有 ExifIFD 指针 (tag 0x8769)，扫描子 IFD
	// 注意: findExifIFDOffset 返回的是相对于 TIFF 开始位置的偏移
	exifIFDOffset := findExifIFDOffset(exifData, tiffStart+ifd0Offset, byteOrder)
	if exifIFDOffset >= 0 && tiffStart+exifIFDOffset < len(exifData) {
		if result := scanIFDForUserComment(exifData, tiffStart+exifIFDOffset, byteOrder, tiffStart); result != "" {
			return result
		}
	}

	// 回退：在 EXIF 中寻找纯文本参数
	str := string(exifData)
	if idx := strings.Index(str, "parameters"); idx >= 0 {
		after := str[idx+10:]
		after = strings.ReplaceAll(after, "\x00", " ")
		after = strings.TrimSpace(after)
		if after != "" {
			return after
		}
	}

	return ""
}

// scanIFDForUserComment 在指定 IFD 中扫描 UserComment tag (0x9286)
func scanIFDForUserComment(exifData []byte, ifdOffset int, byteOrder binary.ByteOrder, tiffStart int) string {
	if ifdOffset+2 > len(exifData) {
		return ""
	}
	pos := ifdOffset
	numEntries := int(byteOrder.Uint16(exifData[pos : pos+2]))
	pos += 2

	for i := 0; i < numEntries; i++ {
		if pos+12 > len(exifData) {
			break
		}
		tag := byteOrder.Uint16(exifData[pos : pos+2])
		count := byteOrder.Uint32(exifData[pos+4 : pos+8])

		if tag == 0x9286 { // UserComment
			if count > 4 {
				dataOff := int(byteOrder.Uint32(exifData[pos+8 : pos+12]))
				commentPos := tiffStart + dataOff
				if commentPos+8 < len(exifData) {
					comment := exifData[commentPos:]
					charCode := string(comment[:8])
					text := comment[8:]
					if int(count-8) < len(text) {
						text = text[:count-8]
					}
					if strings.HasPrefix(charCode, "UNICODE") {
						var cleaned []byte
						for j := 0; j+1 < len(text); j += 2 {
							hi, lo := text[j], text[j+1]
							if byteOrder == binary.LittleEndian {
								hi, lo = lo, hi
							}
							if hi == 0 && lo >= 0x20 && lo < 0x7f {
								cleaned = append(cleaned, lo)
							}
						}
						result := strings.TrimRight(string(cleaned), "\x00")
						if result != "" {
							return result
						}
					} else if strings.HasPrefix(charCode, "ASCII") {
						result := strings.TrimRight(string(text), "\x00")
						if result != "" {
							return result
						}
					}
				}
			}
		}
		pos += 12
	}
	return ""
}

// findExifIFDOffset 在指定 IFD 中查找 ExifIFD 指针 (tag 0x8769)，返回子 IFD 的绝对偏移
func findExifIFDOffset(exifData []byte, ifdOffset int, byteOrder binary.ByteOrder) int {
	if ifdOffset+2 > len(exifData) {
		return -1
	}
	pos := ifdOffset
	numEntries := int(byteOrder.Uint16(exifData[pos : pos+2]))
	pos += 2

	for i := 0; i < numEntries; i++ {
		if pos+12 > len(exifData) {
			break
		}
		tag := byteOrder.Uint16(exifData[pos : pos+2])
		if tag == 0x8769 {
			subIFDOffset := int(byteOrder.Uint32(exifData[pos+8 : pos+12]))
			return subIFDOffset
		}
		pos += 12
	}
	return -1
}

// splitTextChunk 解析 tEXt/zTXt chunk data，返回 key 和 value 字节
func splitTextChunk(data []byte) (key string, value []byte, compressed []byte) {
	nullIdx := -1
	for i, b := range data {
		if b == 0 {
			nullIdx = i
			break
		}
	}
	if nullIdx > 0 && nullIdx+1 < len(data) {
		key = string(data[:nullIdx])
		rest := data[nullIdx+1:]
		// zTXt: 第一个字节是 compression method
		if len(rest) > 1 {
			return key, nil, rest[1:] // skip compression method byte
		}
	}
	return "", nil, nil
}

// splitITextChunk 解析 iTXt chunk data，返回 key, compressionFlag, textBytes
func splitITextChunk(data []byte) (key string, compressionFlag byte, textBytes []byte) {
	nullIdx := -1
	for i, b := range data {
		if b == 0 {
			nullIdx = i
			break
		}
	}
	if nullIdx <= 0 || nullIdx+3 >= len(data) {
		return "", 0, nil
	}
	key = string(data[:nullIdx])
	compressionFlag = data[nullIdx+1]
	rest := data[nullIdx+2:]

	// 跳过 language tag
	langEnd := -1
	for i, b := range rest {
		if b == 0 {
			langEnd = i
			break
		}
	}
	if langEnd < 0 || langEnd+1 >= len(rest) {
		return "", 0, nil
	}
	rest = rest[langEnd+1:]

	// 跳过 translated keyword
	transEnd := -1
	for i, b := range rest {
		if b == 0 {
			transEnd = i
			break
		}
	}
	if transEnd < 0 || transEnd+1 >= len(rest) {
		return "", 0, nil
	}
	rest = rest[transEnd+1:]

	// 跳过前导 null 字节
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}

	if len(rest) == 0 {
		return "", 0, nil
	}
	return key, compressionFlag, rest
}

// decompressZlib zlib 解压
func decompressZlib(data []byte) []byte {
	if len(data) < 2 {
		return nil
	}
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		// 尝试去掉 zlib header (2 bytes)
		if len(data) > 2 {
			r, err = zlib.NewReader(bytes.NewReader(data[2:]))
		}
		if err != nil {
			return nil
		}
	}
	defer r.Close()
	result, _ := io.ReadAll(r)
	return result
}

// ==================== 旧版兼容函数（从 app_metadata.go 保留，供其他调用方使用） ====================

// ParseMetadataText 公开 metadata.ParseTextChunks 的单文本字符串入口
func ParseMetadataText(raw string) *metadata.ParsedParams {
	return metadata.ParseTextChunks(map[string]string{"parameters": raw})
}

// ParseXMPMetadata 公开 XMP 解析入口
func ParseXMPMetadata(xmp string) *metadata.ParsedParams {
	return metadata.ParseTextChunks(map[string]string{"XML:com.adobe.xmp": xmp})
}

// ParseMidjourneyChunks 公开 MJ 解析入口
func ParseMidjourneyChunks(chunks metadata.MidjourneyChunks) *metadata.ParsedParams {
	return metadata.ParseTextChunks(map[string]string{
		"Description":   chunks.Description,
		"Author":        chunks.Author,
		"Creation Time": chunks.CreationTime,
	})
}

// IsMidjourneyDescription 公开 MJ 检测入口
func IsMidjourneyDescription(desc string) bool {
	return metadata.IsMidjourneyDescription(desc)
}

// toLegacyWithRaw 将 ParsedParams 转为旧版 map 并保留原始 text chunks
func toLegacyWithRaw(parsed *metadata.ParsedParams, textChunks map[string]string) map[string]interface{} {
	result := parsed.ToLegacy()
	raw := make(map[string]interface{}, len(textChunks))
	for k, v := range textChunks {
		raw[k] = v
	}
	result["raw"] = raw
	return result
}
