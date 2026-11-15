//go:build windows

package main

import "os/exec"

// Windows 上 exec.CommandContext 默认只结束 cmd.exe；整棵进程树需要 Job Object，
// 不在本轮改动范围内。
func setProcessGroup(cmd *exec.Cmd) {}
