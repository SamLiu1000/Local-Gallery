package main

import (
	"testing"

	"local-gallery/internal/database"
)

// 一次性工具：对真实数据库执行全量元数据回填（后台运行）
func TestRunFullBackfill(t *testing.T) {
	idb, err := database.New(`L:\111-Local Gallery\user\images.db`)
	if err != nil {
		t.Fatal(err)
	}
	defer idb.Close()
	a := &App{imageDB: idb, userDataDir: `L:\111-Local Gallery\user`}
	a.backfillImageMetadata()
}
