//go:build !bindings

package main

import "github.com/davidbyttow/govips/v2/vips"

func initVips() {
	vips.Startup(nil)
}

func shutdownVips() {
	vips.Shutdown()
}
