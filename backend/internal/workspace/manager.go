// Package workspace 按 Issue 准备独立、可回收的 git 工作区。
// 每个工作区从指定 base_commit 创建 agent/<issue> 分支，供 dev 子 Agent 修改；
// 源仓库只读，工作区由 lead 负责回收。
package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ErrNotFound 表示工作区不存在。
var ErrNotFound = errors.New("workspace: not found")

// ErrInvalid 表示请求参数不安全或不完整。
var ErrInvalid = errors.New("workspace: invalid")

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var commitPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// Info 是一个已准备的工作区。
type Info struct {
	ID         string    `json:"id"`
	Path       string    `json:"path"`
	Branch     string    `json:"branch"`
	BaseCommit string    `json:"base_commit"`
	HeadCommit string    `json:"head_commit"`
	Source     string    `json:"source"`
	CreatedAt  time.Time `json:"created_at"`
}

// Manager 管理工作区根目录。
type Manager struct {
	Root   string
	GitBin string
}

func (m *Manager) gitBin() string {
	if strings.TrimSpace(m.GitBin) != "" {
		return m.GitBin
	}
	return "git"
}

func (m *Manager) repoDir(id string) string  { return filepath.Join(m.Root, id, "repo") }
func (m *Manager) metaPath(id string) string { return filepath.Join(m.Root, id, "workspace.json") }

func (m *Manager) ensureRoot() error {
	if strings.TrimSpace(m.Root) == "" {
		return fmt.Errorf("%w: workspace root is empty", ErrInvalid)
	}
	return os.MkdirAll(m.Root, 0o755)
}

// Prepare 创建工作区；同 ID 已存在时返回现有信息（要求 base 一致）。
func (m *Manager) Prepare(ctx context.Context, id, repoSource, baseCommit, branch string) (*Info, error) {
	id = strings.TrimSpace(id)
	if !idPattern.MatchString(id) || strings.Contains(id, "..") {
		return nil, fmt.Errorf("%w: unsafe workspace id %q", ErrInvalid, id)
	}
	if !commitPattern.MatchString(strings.TrimSpace(baseCommit)) {
		return nil, fmt.Errorf("%w: base_commit must be a git commit", ErrInvalid)
	}
	if strings.TrimSpace(repoSource) == "" {
		return nil, fmt.Errorf("%w: repo is required", ErrInvalid)
	}
	if strings.TrimSpace(branch) == "" {
		branch = "agent/" + id
	}
	if existing, err := m.Get(id); err == nil {
		if existing.BaseCommit != strings.TrimSpace(baseCommit) {
			return nil, fmt.Errorf("%w: workspace %s already exists at %s", ErrInvalid, id, existing.BaseCommit)
		}
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if err := m.ensureRoot(); err != nil {
		return nil, err
	}
	dir := m.repoDir(id)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return nil, err
	}
	if err := m.runGit(ctx, "", "clone", "--no-hardlinks", "--quiet", repoSource, dir); err != nil {
		_ = os.RemoveAll(filepath.Dir(dir))
		return nil, fmt.Errorf("workspace: clone: %w", err)
	}
	if err := m.runGit(ctx, dir, "checkout", "-B", branch, strings.TrimSpace(baseCommit)); err != nil {
		_ = os.RemoveAll(filepath.Dir(dir))
		return nil, fmt.Errorf("workspace: checkout: %w", err)
	}
	head, err := m.gitOutput(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	info := &Info{
		ID: id, Path: dir, Branch: branch, BaseCommit: strings.TrimSpace(baseCommit),
		HeadCommit: strings.TrimSpace(head), Source: repoSource, CreatedAt: time.Now().UTC(),
	}
	if err := m.writeMeta(info); err != nil {
		_ = os.RemoveAll(filepath.Dir(dir))
		return nil, err
	}
	return info, nil
}

// Get 读取工作区元数据。
func (m *Manager) Get(id string) (*Info, error) {
	if !idPattern.MatchString(strings.TrimSpace(id)) {
		return nil, ErrNotFound
	}
	raw, err := os.ReadFile(m.metaPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var info Info
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, fmt.Errorf("workspace: decode meta: %w", err)
	}
	return &info, nil
}

// List 返回全部工作区，按创建时间倒序。
func (m *Manager) List() ([]Info, error) {
	entries, err := os.ReadDir(m.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return []Info{}, nil
		}
		return nil, err
	}
	out := make([]Info, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := m.Get(entry.Name())
		if err != nil {
			continue
		}
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// Cleanup 删除工作区目录；只允许删除 Root 下的路径。
func (m *Manager) Cleanup(id string) error {
	info, err := m.Get(id)
	if err != nil {
		return err
	}
	rootAbs, err := filepath.Abs(m.Root)
	if err != nil {
		return err
	}
	dirAbs, err := filepath.Abs(filepath.Join(m.Root, id))
	if err != nil {
		return err
	}
	if !strings.HasPrefix(dirAbs+string(os.PathSeparator), rootAbs+string(os.PathSeparator)) {
		return fmt.Errorf("%w: workspace path escapes root", ErrInvalid)
	}
	if err := os.RemoveAll(dirAbs); err != nil {
		return err
	}
	_ = info
	return nil
}

func (m *Manager) writeMeta(info *Info) error {
	raw, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.metaPath(info.ID), raw, 0o644)
}

func (m *Manager) runGit(ctx context.Context, dir string, args ...string) error {
	_, err := m.gitOutput(ctx, dir, args...)
	return err
}

func (m *Manager) gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-c", "core.autocrlf=false"}, args...)
	cmd := exec.CommandContext(ctx, m.gitBin(), full...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// ShortID 从 Issue 标识生成稳定、安全的短 ID。
func ShortID(issueRef string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(strings.ToLower(issueRef))))
	return "ws-" + hex.EncodeToString(sum[:6])
}
