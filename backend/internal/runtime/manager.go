// Package runtime 管理 SmartPipe 依赖的 Syntropy App Server 进程。
//
// 关键事实（见 docs/Syntropy运行时管理设计.md）：Go SDK 只是 App Server 的客户端，
// App Server 必须由这里启动并守护。Master/Worker 与 Agent 的编排走 HTTP 客户端。
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"smartpipe/backend/internal/syntropyclient"
)

// 常见错误。
var (
	ErrNotFound   = errors.New("runtime: instance not found")
	ErrDuplicate  = errors.New("runtime: instance already exists")
	ErrNotRunning = errors.New("runtime: instance is not running")
)

// Dirs 是 App Server 的目录参数。
type Dirs struct {
	NodeDir            string
	NodeArtifactsDir   string
	MasterDataDir      string
	CredentialDir      string
	MasterRunRoot      string
	MasterRegistryFile string
	LogDir             string
}

// Spec 描述一个 App Server 实例。
type Spec struct {
	ID     string
	Name   string
	Binary string
	Addr   string
	Token  string
	Dirs   Dirs
	Args   []string
	Env    []string
}

// Instance 是对外暴露的实例视图。
type Instance struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Addr      string    `json:"addr"`
	Binary    string    `json:"binary"`
	PID       int       `json:"pid"`
	Status    string    `json:"status"`
	Health    string    `json:"health"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
	ExitCode  *int      `json:"exit_code"`
	LastError string    `json:"last_error"`
	LogPath   string    `json:"log_path"`
	DataDir   string    `json:"data_dir"`
}

type process struct {
	spec    Spec
	cmd     *exec.Cmd
	logPath string
	started time.Time
	status  string
	health  string
	version string
	exit    *int
	lastErr string
	done    chan struct{}
}

// Manager 维护本机上的 App Server 进程，并把实例配置持久化到 statePath，
// 以便 admin-api 重启后仍能看到历史实例（状态标记为 stopped，需手动启动）。
type Manager struct {
	mu        sync.RWMutex
	procs     map[string]*process
	statePath string
}

// NewManager 创建空管理器。
func NewManager() *Manager {
	return &Manager{procs: make(map[string]*process)}
}

// NewManagerWithState 创建带持久化的管理器；statePath 为空时等价于 NewManager。
func NewManagerWithState(statePath string) *Manager {
	m := &Manager{procs: make(map[string]*process), statePath: statePath}
	m.loadState()
	return m
}

func (m *Manager) loadState() {
	if m.statePath == "" {
		return
	}
	data, err := os.ReadFile(m.statePath)
	if err != nil {
		return
	}
	specs := map[string]Spec{}
	if err := json.Unmarshal(data, &specs); err != nil {
		return
	}
	closed := make(chan struct{})
	close(closed)
	for id, spec := range specs {
		m.procs[id] = &process{
			spec:    spec,
			status:  "stopped",
			health:  "down",
			logPath: filepath.Join(spec.Dirs.LogDir, sanitize(spec.Name)+".log"),
			done:    closed,
		}
	}
}

func (m *Manager) persistLocked() {
	if m.statePath == "" {
		return
	}
	specs := make(map[string]Spec, len(m.procs))
	for id, p := range m.procs {
		specs[id] = p.spec
	}
	data, err := json.MarshalIndent(specs, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(m.statePath), 0o755)
	tmp := m.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, m.statePath)
}

func (m *Manager) snapshot(p *process) Instance {
	inst := Instance{
		ID:        p.spec.ID,
		Name:      p.spec.Name,
		Addr:      p.spec.Addr,
		Binary:    p.spec.Binary,
		Status:    p.status,
		Health:    p.health,
		Version:   p.version,
		StartedAt: p.started,
		ExitCode:  p.exit,
		LastError: p.lastErr,
		LogPath:   p.logPath,
		DataDir:   p.spec.Dirs.CredentialDir,
	}
	if p.cmd != nil && p.cmd.Process != nil && p.status == "running" {
		inst.PID = p.cmd.Process.Pid
	}
	return inst
}

// Start 启动一个 App Server 进程并等待其端口可用。
func (m *Manager) Start(ctx context.Context, spec Spec) (Instance, error) {
	if strings.TrimSpace(spec.Binary) == "" {
		return Instance{}, errors.New("runtime: binary path is required")
	}
	if strings.TrimSpace(spec.Addr) == "" {
		return Instance{}, errors.New("runtime: listen address is required")
	}
	info, err := os.Stat(spec.Binary)
	if err != nil || info.IsDir() {
		return Instance{}, fmt.Errorf("runtime: binary not found: %s", spec.Binary)
	}
	if spec.ID == "" {
		spec.ID = "appsrv-" + strconv.FormatInt(time.Now().UnixNano()%100000000, 36)
	}
	if spec.Name == "" {
		spec.Name = spec.ID
	}

	m.mu.Lock()
	if existing, exists := m.procs[spec.ID]; exists && existing.status == "running" {
		m.mu.Unlock()
		return Instance{}, ErrDuplicate
	}
	delete(m.procs, spec.ID)
	m.mu.Unlock()

	if !portFree(spec.Addr) {
		return Instance{}, fmt.Errorf("runtime: %s is already in use", spec.Addr)
	}

	logDir := spec.Dirs.LogDir
	if logDir == "" {
		logDir = filepath.Dir(spec.Binary)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return Instance{}, fmt.Errorf("runtime: create log dir: %w", err)
	}
	logPath := filepath.Join(logDir, sanitize(spec.Name)+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return Instance{}, fmt.Errorf("runtime: open log: %w", err)
	}

	args := buildArgs(spec)
	cmd := exec.Command(spec.Binary, args...)
	cmd.Env = append(os.Environ(), spec.Env...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Dir = filepath.Dir(spec.Binary)

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return Instance{}, fmt.Errorf("runtime: start: %w", err)
	}
	_ = logFile.Close()

	p := &process{
		spec:    spec,
		cmd:     cmd,
		logPath: logPath,
		started: time.Now().UTC(),
		status:  "running",
		health:  "unknown",
		done:    make(chan struct{}),
	}

	m.mu.Lock()
	m.procs[spec.ID] = p
	m.persistLocked()
	m.mu.Unlock()

	go func() {
		err := cmd.Wait()
		m.mu.Lock()
		defer m.mu.Unlock()
		code := 0
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		p.exit = &code
		if p.status == "running" {
			p.status = "exited"
			if code != 0 {
				p.status = "failed"
			}
		}
		if err != nil && p.status == "failed" {
			p.lastErr = err.Error()
		}
		p.health = "down"
		close(p.done)
	}()

	// 等待端口可用（最多 20 秒），再取一次健康状态。
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if !portFree(spec.Addr) {
			break
		}
		select {
		case <-p.done:
			m.mu.RLock()
			inst := m.snapshot(p)
			m.mu.RUnlock()
			return inst, fmt.Errorf("runtime: process exited during startup (see %s)", logPath)
		case <-time.After(200 * time.Millisecond):
		}
	}
	select {
	case <-ctx.Done():
		return Instance{}, ctx.Err()
	default:
	}

	inst, _ := m.Health(ctx, spec.ID)
	return inst, nil
}

// Health 探测实例的 /api/health。
func (m *Manager) Health(ctx context.Context, id string) (Instance, error) {
	m.mu.RLock()
	p, ok := m.procs[id]
	m.mu.RUnlock()
	if !ok {
		return Instance{}, ErrNotFound
	}

	health, err := probe(ctx, p.spec.Addr, p.spec.Token)
	m.mu.Lock()
	p.health = health.status
	// 进程可能是上一个管理进程留下的（重启后 PID 丢失）；只要 /api/health 可达就视为运行中。
	if health.status == "ok" || health.status == "degraded" {
		p.status = "running"
	} else if p.cmd == nil {
		p.status = "stopped"
	}
	if health.version != "" {
		p.version = health.version
	}
	if err != nil {
		p.lastErr = err.Error()
	}
	inst := m.snapshot(p)
	m.mu.Unlock()
	return inst, nil
}

type healthResult struct {
	status  string
	version string
}

func probe(ctx context.Context, addr, token string) (healthResult, error) {
	client := syntropyclient.New("http://"+addr, token)
	body, status, err := client.Health(ctx)
	if err != nil {
		return healthResult{status: "down"}, err
	}
	res := healthResult{status: "degraded"}
	if v, ok := body["version"].(string); ok {
		res.version = v
	}
	if status == 200 {
		res.status = "ok"
	}
	if s, ok := body["status"].(string); ok && s == "degraded" {
		res.status = "degraded"
	}
	return res, nil
}

// Stop 停止实例：先尝试优雅中断，超时后强杀。
func (m *Manager) Stop(id string, timeout time.Duration) error {
	m.mu.RLock()
	p, ok := m.procs[id]
	m.mu.RUnlock()
	if !ok {
		return ErrNotFound
	}
	if p.status != "running" {
		return ErrNotRunning
	}
	if timeout <= 0 {
		timeout = 8 * time.Second
	}

	if err := p.cmd.Process.Signal(os.Interrupt); err != nil {
		// Windows 不支持向非控制台进程发 SIGINT，直接进入强杀分支。
		_ = p.cmd.Process.Kill()
	}
	select {
	case <-p.done:
		m.mu.Lock()
		p.status = "stopped"
		p.health = "down"
		m.persistLocked()
		m.mu.Unlock()
		return nil
	case <-time.After(timeout):
	}

	if err := p.cmd.Process.Kill(); err != nil {
		return fmt.Errorf("runtime: kill: %w", err)
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		return errors.New("runtime: process did not exit after kill")
	}
	m.mu.Lock()
	p.status = "stopped"
	p.health = "down"
	m.persistLocked()
	m.mu.Unlock()
	return nil
}

// Restart 重启实例，保持原有参数。
func (m *Manager) Restart(ctx context.Context, id string) (Instance, error) {
	m.mu.RLock()
	p, ok := m.procs[id]
	var spec Spec
	if ok {
		spec = p.spec
	}
	m.mu.RUnlock()
	if !ok {
		return Instance{}, ErrNotFound
	}
	if p.status == "running" && p.cmd != nil {
		if err := m.Stop(id, 8*time.Second); err != nil {
			return Instance{}, err
		}
	}
	m.mu.Lock()
	delete(m.procs, id)
	m.mu.Unlock()
	return m.Start(ctx, spec)
}

// Remove 删除实例记录；仅在实例未运行时允许。
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.procs[id]
	if !ok {
		return ErrNotFound
	}
	if p.status == "running" {
		return errors.New("runtime: stop the instance before removing it")
	}
	delete(m.procs, id)
	m.persistLocked()
	return nil
}

// List 返回所有实例快照。
func (m *Manager) List() []Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Instance, 0, len(m.procs))
	for _, p := range m.procs {
		out = append(out, m.snapshot(p))
	}
	return out
}

// Get 返回单个实例快照。
func (m *Manager) Get(id string) (Instance, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.procs[id]
	if !ok {
		return Instance{}, false
	}
	return m.snapshot(p), true
}

// Client 返回指向该实例的 HTTP 客户端。
func (m *Manager) Client(id string) (*syntropyclient.Client, error) {
	m.mu.RLock()
	p, ok := m.procs[id]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	return syntropyclient.New("http://"+p.spec.Addr, p.spec.Token), nil
}

// LogTail 返回日志最后 n 行。
func (m *Manager) LogTail(id string, n int) (string, error) {
	m.mu.RLock()
	p, ok := m.procs[id]
	m.mu.RUnlock()
	if !ok {
		return "", ErrNotFound
	}
	if n <= 0 || n > 2000 {
		n = 200
	}
	data, err := os.ReadFile(p.logPath)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

func buildArgs(spec Spec) []string {
	args := []string{"--server-addr", spec.Addr}
	if v := spec.Dirs.NodeDir; v != "" {
		args = append(args, "--node-dir", v)
	}
	if v := spec.Dirs.NodeArtifactsDir; v != "" {
		args = append(args, "--node-artifacts-dir", v)
	}
	if v := spec.Dirs.MasterDataDir; v != "" {
		args = append(args, "--master-data-dir", v)
	}
	if v := spec.Dirs.CredentialDir; v != "" {
		args = append(args, "--credential-dir", v)
	}
	if v := spec.Dirs.MasterRunRoot; v != "" {
		args = append(args, "--master-run-root", v)
	}
	if v := spec.Dirs.MasterRegistryFile; v != "" {
		args = append(args, "--master-registry-file", v)
	}
	if v := spec.Dirs.LogDir; v != "" {
		args = append(args, "--log-dir", v)
	}
	if spec.Token != "" {
		args = append(args, "--desktop-token", spec.Token)
	}
	return append(args, spec.Args...)
}

func portFree(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return true
	}
	_ = conn.Close()
	return false
}

func sanitize(name string) string {
	replacer := strings.NewReplacer(" ", "-", "/", "-", "\\", "-", ":", "-")
	return replacer.Replace(name)
}
