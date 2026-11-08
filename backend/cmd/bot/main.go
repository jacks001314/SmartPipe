// 蓝信机器人服务（M1）：
//   - 从配置台加载多个机器人并各自维护 WebSocket 长连接
//   - 归一化 + msgId 幂等 + @过滤 + /new 会话重置
//   - 经 Dispatcher 交给 bridge/编排层，并上报连接状态
//   - 配置台增删改机器人后 30 秒内自动启动、重连或停止对应连接
package main

import (
	"context"
	"log"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"smartpipe/backend/internal/bot"
	"smartpipe/sdks/lanxin"
)

type wsRunner struct {
	cfg    bot.Config
	cancel context.CancelFunc
	done   chan struct{}
}

type runnerManager struct {
	base    bot.Config
	runtime *bot.BotRuntime
	mu      sync.Mutex
	running map[string]*wsRunner
}

func newRunnerManager(base bot.Config, rt *bot.BotRuntime) *runnerManager {
	return &runnerManager{base: base, runtime: rt, running: map[string]*wsRunner{}}
}

func (m *runnerManager) start(parent context.Context, cfg bot.Config) *wsRunner {
	client := lanxin.New(cfg.AppID, cfg.AppSecret, lanxin.WithAPIBase(cfg.APIBase))
	handler := bot.NewHandler(cfg, client, bot.NewDispatcher(cfg, client))
	if cfg.BridgeURL != "" {
		handler = handler.WithInbox(bot.BridgeInbox{BaseURL: cfg.BridgeURL, Token: cfg.BridgeToken})
	}
	if cfg.CrashIngestURL != "" {
		handler = handler.WithCrash(&bot.CrashIntake{
			IngestURL: cfg.CrashIngestURL, Token: cfg.CrashToken, ProjectID: cfg.CrashProject,
			MaxBytes: cfg.CrashMaxBytes, Media: client,
		})
	}
	botID := cfg.BotID
	if botID == "" {
		botID = "default"
	}
	m.runtime.Add(botID, handler)
	status := func(_ context.Context, state, message string) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := bot.ReportRemoteStatus(ctx, cfg.AdminURL, cfg.InternalToken, cfg.BotID, state, message); err != nil {
				log.Printf("[bot:%s] report status %s: %v", cfg.BotName, state, err)
			}
		}()
	}
	ws := lanxin.NewWSClient(client, handler.HandleEvent, lanxin.WithStatusHandler(status))
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := ws.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("[bot:%s] ws loop exit: %v", cfg.BotName, err)
			status(context.Background(), "error", err.Error())
		}
	}()
	log.Printf("[bot:%s] started app_id=%s api_base=%s", cfg.BotName, cfg.AppID, cfg.APIBase)
	return &wsRunner{cfg: cfg, cancel: cancel, done: done}
}

func (m *runnerManager) stop(botID string) {
	m.mu.Lock()
	runner := m.running[botID]
	delete(m.running, botID)
	m.mu.Unlock()
	if runner == nil {
		return
	}
	runner.cancel()
	select {
	case <-runner.done:
	case <-time.After(8 * time.Second):
	}
	if runner.cfg.BotID != "" {
		m.runtime.Remove(runner.cfg.BotID)
	} else {
		m.runtime.Remove("default")
	}
	log.Printf("[bot:%s] stopped", runner.cfg.BotName)
}

func (m *runnerManager) apply(parent context.Context, configs []bot.RemoteLanxinConfig) {
	desired := map[string]bot.Config{}
	for _, rc := range configs {
		cfg := m.base.WithRemote(&rc)
		if strings.TrimSpace(cfg.BotID) == "" {
			cfg.BotID = "default"
		}
		if strings.TrimSpace(cfg.BotName) == "" {
			cfg.BotName = cfg.AppID
		}
		if err := cfg.Validate(); err != nil {
			log.Printf("[bot:%s] invalid remote config: %v", cfg.BotName, err)
			continue
		}
		desired[cfg.BotID] = cfg
	}

	m.mu.Lock()
	toStop := make([]string, 0)
	for id, runner := range m.running {
		target, ok := desired[id]
		if !ok || !sameLanxin(runner.cfg, target) {
			toStop = append(toStop, id)
		}
	}
	m.mu.Unlock()
	for _, id := range toStop {
		m.stop(id)
	}

	for id, target := range desired {
		m.mu.Lock()
		runner := m.running[id]
		m.mu.Unlock()
		if runner != nil && sameLanxin(runner.cfg, target) {
			continue
		}
		m.stop(id) // 清理可能残留的旧连接
		runner = m.start(parent, target)
		m.mu.Lock()
		m.running[id] = runner
		m.mu.Unlock()
	}
}

func (m *runnerManager) stopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.running))
	for id := range m.running {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.stop(id)
	}
}

// sameLanxin 比较影响连接和消息归属的字段。
func sameLanxin(a, b bot.Config) bool {
	return a.BotID == b.BotID && a.BotName == b.BotName &&
		a.AppID == b.AppID && a.AppSecret == b.AppSecret &&
		a.APIBase == b.APIBase && a.RequireAtMe == b.RequireAtMe
}

func main() {
	base, err := bot.LoadFromEnv()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runtime := bot.NewRuntime()
	manager := newRunnerManager(base, runtime)
	if base.AdminURL == "" {
		if err := base.Validate(); err != nil {
			log.Printf("[bot] no remote console and no environment bot: %v", err)
		} else {
			manager.apply(ctx, []bot.RemoteLanxinConfig{{
				BotID: "env", BotName: "环境变量机器人", AppID: base.AppID,
				AppSecret: base.AppSecret, APIBase: base.APIBase, RequireAtMe: base.RequireAtMe,
			}})
		}
	} else if configs, err := bot.FetchRemoteConfigs(ctx, base.AdminURL, base.InternalToken); err != nil {
		log.Printf("[bot] 读取配置台机器人列表失败，暂用环境变量: %v", err)
		if base.AppID != "" && base.AppSecret != "" {
			manager.apply(ctx, []bot.RemoteLanxinConfig{{
				BotID: "env", BotName: "环境变量机器人", AppID: base.AppID,
				AppSecret: base.AppSecret, APIBase: base.APIBase, RequireAtMe: base.RequireAtMe,
			}})
		}
	} else {
		log.Printf("[bot] 配置台加载 %d 个机器人", len(configs))
		manager.apply(ctx, configs)
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			var lastErr string
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					configs, err := bot.FetchRemoteConfigs(ctx, base.AdminURL, base.InternalToken)
					if err != nil {
						if err.Error() != lastErr {
							lastErr = err.Error()
							log.Printf("[bot] 轮询配置台失败: %v", err)
						}
						continue
					}
					lastErr = ""
					manager.apply(ctx, configs)
				}
			}
		}()
	}

	app := bot.NewRuntimeServer(base, runtime)
	go func() {
		<-ctx.Done()
		manager.stopAll()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = app.Shutdown(shutdownCtx)
	}()

	log.Printf("[bot] http listening on %s (api=%s)", base.HTTPAddr, base.APIBase)
	if err := app.Listen(base.HTTPAddr); err != nil {
		log.Fatalf("http server: %v", err)
	}
}
