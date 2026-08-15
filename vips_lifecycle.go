//go:build !bindings

package main

import "github.com/davidbyttow/govips/v2/vips"

func initVips() {
	vips.Startup(nil)
}

// initVipsWithConcurrency 以指定 libvips 线程池并发启动（worker 子进程用）。
// 与主进程设置同步：并发翻倍时 worker 的 vips 才能真正用上更多线程。
func initVipsWithConcurrency(n int) {
	if n <= 0 {
		initVips()
		return
	}
	vips.Startup(&vips.Config{ConcurrencyLevel: n})
}

func shutdownVips() {
	vips.Shutdown()
}
