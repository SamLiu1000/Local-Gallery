package main

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
