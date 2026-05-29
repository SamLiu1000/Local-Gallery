//go:build bindings

package main

import (
	"errors"
	"sync"
)

var (
	thumbGenLocks sync.Map
	thumbSemMu    sync.RWMutex
	thumbSem      = make(chan struct{}, 2)
	thumbSemSize  int32
	preGenCancel  chan struct{}
	preGenCancelMu sync.Mutex
)

func (a *App) SetThumbConcurrency(n int) map[string]interface{} {
	return map[string]interface{}{"success": true, "thumbConcurrency": n}
}

func (a *App) GetThumbConcurrency() int { return int(thumbSemSize) }

func (a *App) SetThumbKernel(kernel string) map[string]interface{} {
	return map[string]interface{}{"success": true, "thumbKernel": kernel}
}

func (a *App) GetThumbKernel() string { return "lanczos3" }

func (a *App) loadThumbSettings() {}

func (a *App) getThumbDir() string { return "" }

func (a *App) GetThumbDir() string { return "" }

func (a *App) SetThumbDir(path string) map[string]interface{} {
	return map[string]interface{}{"success": true}
}

func (a *App) serveThumbnail(imageID string) ([]byte, error) {
	return nil, errors.New("stub: not available in bindings build")
}

func (a *App) startPreGenThumbs(folder string, entries []*ImageEntry) {}

func (a *App) StartPreGenThumbs(folders []string) map[string]interface{} {
	return map[string]interface{}{"success": false, "message": "stub: not available in bindings build"}
}

func (a *App) StopPreGenThumbs() map[string]interface{} {
	return map[string]interface{}{"success": true}
}

func (a *App) PausePreGenThumbs() map[string]interface{} {
	return map[string]interface{}{"success": true}
}

func (a *App) ResumePreGenThumbs() map[string]interface{} {
	return map[string]interface{}{"success": true}
}

func (a *App) GetPreGenStatus() *PreGenStatus {
	return &PreGenStatus{}
}

func (a *App) GetThumbGeneration() int64 { return 0 }

func (a *App) ClearFolderThumbs(folderPath string) map[string]interface{} {
	return map[string]interface{}{"success": true, "cleaned": 0}
}

func (a *App) CleanOrphanedThumbs() map[string]interface{} {
	return map[string]interface{}{"success": true, "cleaned": 0}
}

func (a *App) removeThumbsByIDs(ids []string) {}

func (a *App) computeThumbCounts() map[string]int {
	return make(map[string]int)
}

func (a *App) PreloadThumbCounts() {}

func (a *App) getCachedThumbCounts() map[string]int { return nil }

func (a *App) GetUserDataDir() string { return "" }

func (a *App) SetUserDataDir(path string) map[string]interface{} {
	return map[string]interface{}{"success": true}
}

func (a *App) finishUserDataDirSwitch(resolved string) {}

func (a *App) saveUserDataDirToDefault(userDataDir string) {}

func (a *App) applySavedUserDataDir() {}

func (a *App) GetThumbCacheInfo() map[string]interface{} {
	return map[string]interface{}{"count": 0, "totalSize": int64(0), "totalSizeStr": "0 B", "dir": ""}
}

func readJSONFile(path string) map[string]interface{} {
	return make(map[string]interface{})
}

func writeJSONFile(path string, data map[string]interface{}) {}

func formatThumbSize(bytes int64) string { return "0 B" }

func GetImageDimensionsVips(filePath string) (int, int) { return 0, 0 }
