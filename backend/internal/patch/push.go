package patch

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
)

// PushRequest 把已提交的分支推送到远端；不执行 force push。
type PushRequest struct {
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	Remote    string `json:"remote"`
	RemoteURL string `json:"remote_url"`
}

// PushResult 是推送结果与两端 ref 证据。
type PushResult struct {
	OK     bool   `json:"ok"`
	Branch string `json:"branch"`
	Remote string `json:"remote"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Commit string `json:"commit,omitempty"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Push 推送分支并核对远端 ref；远端拒绝（非快进）时返回 ok=false。
func (v *Verifier) Push(ctx context.Context, req PushRequest) (*PushResult, error) {
	res := &PushResult{Branch: strings.TrimSpace(req.Branch), Remote: strings.TrimSpace(req.Remote)}
	if strings.TrimSpace(req.Repo) == "" {
		return res, fmt.Errorf("patch: repo is required")
	}
	if res.Branch == "" {
		res.Branch = "agent/patch"
	}
	if res.Remote == "" {
		res.Remote = "origin"
	}
	repo := strings.TrimSpace(req.Repo)
	local, err := v.git(ctx, repo, "rev-parse", "refs/heads/"+res.Branch)
	if err != nil {
		res.Error = "branch not found: " + err.Error()
		return res, nil
	}
	res.Commit = strings.TrimSpace(local)

	remoteRef := "refs/heads/" + res.Branch
	remoteURL := strings.TrimSpace(req.RemoteURL)
	lsRemote := func() (string, error) {
		args := []string{"ls-remote"}
		if remoteURL != "" {
			args = append(args, remoteURL)
		} else {
			args = append(args, res.Remote)
		}
		out, err := v.git(ctx, repo, append(args, remoteRef)...)
		if err != nil {
			return "", err
		}
		return parseRefSHA(out), nil
	}
	if before, err := lsRemote(); err == nil {
		res.Before = before
	} else {
		res.Error = "cannot reach remote: " + err.Error()
		return res, nil
	}

	args := []string{"push"}
	if remoteURL != "" {
		args = append(args, remoteURL)
	} else {
		args = append(args, res.Remote)
	}
	args = append(args, res.Branch+":"+res.Branch)
	out, err := v.git(ctx, repo, args...)
	res.Output = strings.TrimSpace(out)
	if err != nil {
		res.Error = "push rejected: " + err.Error()
		return res, nil
	}
	after, err := lsRemote()
	if err != nil {
		res.Error = "cannot verify remote ref: " + err.Error()
		return res, nil
	}
	res.After = after
	if after != res.Commit {
		res.Error = fmt.Sprintf("remote ref mismatch: local %s remote %s", res.Commit, after)
		return res, nil
	}
	res.OK = true
	return res, nil
}

// parseRefSHA 从 ls-remote 输出里取出 ref 的 SHA。
// git 可能把 "warning: redirecting to ..." 之类的提示混进输出，
// 直接取第一个字段会拿到 "warning:"。
func parseRefSHA(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		sha := fields[0]
		if len(sha) != 40 {
			continue
		}
		if _, err := hex.DecodeString(sha); err != nil {
			continue
		}
		return sha
	}
	return ""
}
