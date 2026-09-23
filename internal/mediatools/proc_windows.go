//go:build windows

package mediatools

import (
	"os/exec"
	"syscall"
)

// hideWindow 防止 ffmpeg/ffprobe 子进程在 GUI 程序里弹出 cmd 窗口。
// 必须同时设置 HideWindow 与 CREATE_NO_WINDOW，否则部分构建仍会闪窗。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
