package releasemcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"smartpipe/backend/internal/releasev2"
)

// executorDispatch 实现构建机遥控工具。命令只会发到本次运行绑定的构建机，
// Agent 不能凭字符串直接指定任意机器。
func (s *Server) executorDispatch(ctx context.Context, p *principal, name string, args map[string]any) (*toolResult, bool, error) {
	if name == "executor_command_get" {
		if !releasev2.HasScope(p.Token.Scopes, "executor.read") {
			return blockedResult("scope_denied", name+" requires executor.read"), true, nil
		}
		cmd, err := s.Store.GetExecutorCommand(ctx, int64Arg(args, "command_id"))
		if err != nil {
			return nil, true, err
		}
		if cmd.RunID != p.Run.ID {
			return blockedResult("scope_denied", "command belongs to a different run"), true, nil
		}
		return textResult(cmd), true, nil
	}
	switch name {
	case "executor_probe", "executor_exec", "executor_logs", "executor_files":
	default:
		return nil, false, nil
	}
	if name == "executor_exec" {
		if !releasev2.HasScope(p.Token.Scopes, "build.write") {
			return blockedResult("scope_denied", "executor_exec requires build.write"), true, nil
		}
	} else if !releasev2.HasScope(p.Token.Scopes, "executor.read") {
		return blockedResult("scope_denied", name+" requires executor.read"), true, nil
	}
	executorID, err := s.resolveRunExecutor(ctx, p.Run, stringArg(args, "executor"))
	if err != nil {
		return nil, true, err
	}
	kind := releasev2.ExecutorCommandProbe
	payload := map[string]any{}
	switch name {
	case "executor_exec":
		kind = releasev2.ExecutorCommandExec
		if timeout := intArg(args, "timeout_seconds", 0); timeout > 0 {
			payload["timeout_seconds"] = timeout
		}
	case "executor_logs":
		kind = releasev2.ExecutorCommandLogs
		payload["path"] = stringArg(args, "path")
		if lines := intArg(args, "lines", 0); lines > 0 {
			payload["lines"] = lines
		}
	case "executor_files":
		kind = releasev2.ExecutorCommandFiles
		payload["path"] = stringArg(args, "path")
	}
	expires := time.Now().Add(10 * time.Minute)
	cmd := releasev2.ExecutorCommand{
		ExecutorID: executorID, RunID: p.Run.ID, TargetID: stringArg(args, "target"),
		Kind: kind, Command: stringArg(args, "command"), Cwd: stringArg(args, "cwd"),
		Payload: payload, CreatedBy: "agent:" + p.Token.ID, ExpiresAt: &expires,
	}
	if err := cmd.Normalize(); err != nil {
		return nil, true, err
	}
	created, err := s.Store.EnqueueExecutorCommand(ctx, cmd)
	if err != nil {
		return nil, true, err
	}
	timeout := 90 * time.Second
	switch kind {
	case releasev2.ExecutorCommandExec:
		// 命令本身可以跑 12 分钟，但工具调用只等 110 秒：更久的安装/构建
		// 直接返回 command_id，让 Agent 主动轮询，避免 MCP 客户端超时把
		// 仍在进行的修复误判成失败。
		timeout = 110 * time.Second
	case releasev2.ExecutorCommandProbe:
		timeout = 2 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		current, err := s.Store.GetExecutorCommand(ctx, created.ID)
		if err != nil {
			return nil, true, err
		}
		if current.Terminal() {
			return textResult(current), true, nil
		}
		if time.Now().After(deadline) {
			return textResult(map[string]any{
				"command_id": created.ID, "executor_id": executorID, "status": current.Status,
				"note": "command is still running; call executor_command_get with this id to read the result",
			}), true, nil
		}
		select {
		case <-ctx.Done():
			return nil, true, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// resolveRunExecutor 只允许使用本次运行绑定的构建机。
func (s *Server) resolveRunExecutor(ctx context.Context, run *releasev2.Run, requested string) (string, error) {
	if run == nil {
		return "", fmt.Errorf("run is required")
	}
	bound := append([]string(nil), run.ExecutorIDs...)
	if len(bound) == 0 {
		if targets, err := s.Store.ListTargets(ctx, run.ID); err == nil {
			for _, target := range targets {
				if id := strings.TrimSpace(target.ExecutorID); id != "" {
					bound = append(bound, id)
				}
			}
		}
	}
	seen := map[string]bool{}
	unique := make([]string, 0, len(bound))
	for _, id := range bound {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	requested = strings.TrimSpace(requested)
	if requested != "" {
		if !seen[requested] {
			return "", fmt.Errorf("executor %s is not bound to run %s (bound: %v)", requested, run.ID, unique)
		}
		return requested, nil
	}
	if len(unique) == 1 {
		return unique[0], nil
	}
	return "", fmt.Errorf("executor is required: run is bound to %v", unique)
}
