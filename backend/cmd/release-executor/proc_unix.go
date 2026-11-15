//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// setProcessGroup 把构建命令放进独立进程组，并在 context 取消时杀掉整组。
// 只杀 /bin/sh 会留下 pnpm/electron-forge 等子进程继续构建，
// 取消 run 之后执行器虽然空出来了，机器却还在跑上一轮的活。
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
