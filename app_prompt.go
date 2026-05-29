package main

import (
	"fmt"
	"local-gallery/internal/database"
	"time"
)

// ==================== Prompt 版本操作（SQLite 后端） ====================

// AddPromptVersion 添加提示词版本
func (a *App) AddPromptVersion(imagePath, positivePrompt, negativePrompt, source string) map[string]interface{} {
	if a.imageDB == nil {
		return map[string]interface{}{"success": false, "error": "数据库不可用"}
	}
	v := &database.PromptVersion{
		ID:             "pv_" + fmt.Sprintf("%d", time.Now().UnixNano()) + "_" + fmt.Sprintf("%06d", time.Now().Nanosecond()%1000000),
		ImagePath:      imagePath,
		PositivePrompt: positivePrompt,
		NegativePrompt: negativePrompt,
		Source:         source,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	if err := a.imageDB.AddPromptVersion(v); err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{"success": true, "version": v}
}

// GetPromptVersions 获取某张图片的提示词版本列表
func (a *App) GetPromptVersions(imagePath string) []database.PromptVersion {
	if a.imageDB == nil {
		return nil
	}
	versions, err := a.imageDB.GetPromptVersions(imagePath)
	if err != nil {
		fmt.Printf("[警告] 获取提示词版本失败: %v\n", err)
		return nil
	}
	if versions == nil {
		return []database.PromptVersion{}
	}
	return versions
}

// DeletePromptVersion 删除提示词版本
func (a *App) DeletePromptVersion(id string) map[string]interface{} {
	if a.imageDB == nil {
		return map[string]interface{}{"success": false, "error": "数据库不可用"}
	}
	if err := a.imageDB.DeletePromptVersion(id); err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{"success": true}
}

// GetAllPromptVersions 获取所有提示词版本（导出用）
func (a *App) GetAllPromptVersions() []database.PromptVersion {
	if a.imageDB == nil {
		return nil
	}
	versions, err := a.imageDB.GetAllPromptVersions()
	if err != nil {
		fmt.Printf("[警告] 获取全部提示词版本失败: %v\n", err)
		return nil
	}
	if versions == nil {
		return []database.PromptVersion{}
	}
	return versions
}

// GetAllPromptVersionCounts 获取各图片的提示词版本数量
func (a *App) GetAllPromptVersionCounts() map[string]int {
	if a.imageDB == nil {
		return nil
	}
	counts, err := a.imageDB.GetAllPromptVersionCounts()
	if err != nil {
		fmt.Printf("[警告] 获取提示词版本计数失败: %v\n", err)
		return nil
	}
	if counts == nil {
		return map[string]int{}
	}
	return counts
}

// ImportPromptVersions 批量导入提示词版本（导入用）
func (a *App) ImportPromptVersions(versions []database.PromptVersion) map[string]interface{} {
	if a.imageDB == nil {
		return map[string]interface{}{"success": false, "error": "数据库不可用"}
	}
	if err := a.imageDB.BatchAddPromptVersions(versions); err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{"success": true, "count": len(versions)}
}

// migratePromptVersions 将 user-data.json 中的旧 promptVersions 迁移到 SQLite
func (a *App) migratePromptVersions() {
	if a.imageDB == nil {
		return
	}
	data := a.readUserDataFile()
	pvRaw, ok := data["promptVersions"]
	if !ok {
		return
	}
	pvList, ok := pvRaw.([]interface{})
	if !ok || len(pvList) == 0 {
		delete(data, "promptVersions")
		a.writeUserDataFile(data)
		return
	}

	var toInsert []database.PromptVersion
	for _, item := range pvList {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		imagePath, _ := m["imagePath"].(string)
		positive, _ := m["positivePrompt"].(string)
		negative, _ := m["negativePrompt"].(string)
		source, _ := m["source"].(string)
		createdAt, _ := m["createdAt"].(string)
		if id == "" || imagePath == "" {
			continue
		}
		toInsert = append(toInsert, database.PromptVersion{
			ID:             id,
			ImagePath:      imagePath,
			PositivePrompt: positive,
			NegativePrompt: negative,
			Source:         source,
			CreatedAt:      createdAt,
		})
	}

	if len(toInsert) > 0 {
		if err := a.imageDB.BatchAddPromptVersions(toInsert); err != nil {
			fmt.Printf("[迁移] 提示词版本迁移到 SQLite 失败: %v\n", err)
			return
		}
		fmt.Printf("[迁移] 已将 %d 个提示词版本从 user-data.json 迁移到 SQLite\n", len(toInsert))
	}

	delete(data, "promptVersions")
	if err := a.writeUserDataFile(data); err != nil {
		fmt.Printf("[迁移] 清理 user-data.json 中的 promptVersions 失败: %v\n", err)
	} else {
		fmt.Printf("[迁移] 已从 user-data.json 中移除 promptVersions\n")
	}
}
