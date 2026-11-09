// Package frptunnel 在进程内运行一个 frpc 客户端，把本机端口的 TCP 代理
// 暴露到公网 frps 上，供 GitHub Webhook 之类的公网回调进入。
//
// 这里直接依赖 github.com/fatedier/frp 的 client 包：frpc 本身就是这个包
// 的一个命令行外壳，用内存配置源即可省掉独立进程和配置文件。
package frptunnel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fatedier/frp/client"
	"github.com/fatedier/frp/pkg/config/source"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/util/log"
)

// Config 描述一条 TCP 隧道。
type Config struct {
	// ServerAddr / ServerPort 指向 frps 的 bindPort。
	ServerAddr string
	ServerPort int
	// Token 必须与 frps 的 auth.token 一致。
	Token string
	// LocalIP / LocalPort 是本进程可访问的后端地址。
	LocalIP   string
	LocalPort int
	// RemotePort 是 frps 侧监听的端口（受 frps 的 allowPorts 限制）。
	RemotePort int
	// ProxyName 是隧道名，frps 侧按这个名字去重。
	ProxyName string
	// LogLevel 传给 frp 自己的日志器，默认 info。
	LogLevel string
}

func (c Config) withDefaults() Config {
	if c.ServerPort <= 0 {
		c.ServerPort = 7000
	}
	if c.LocalIP == "" {
		c.LocalIP = "127.0.0.1"
	}
	if c.ProxyName == "" {
		c.ProxyName = "smartpipe-webhook"
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	return c
}

func (c Config) validate() error {
	if strings.TrimSpace(c.ServerAddr) == "" {
		return errors.New("frptunnel: ServerAddr 不能为空")
	}
	if c.LocalPort <= 0 {
		return errors.New("frptunnel: LocalPort 必须大于 0")
	}
	if c.RemotePort <= 0 {
		return errors.New("frptunnel: RemotePort 必须大于 0")
	}
	return nil
}

// Run 建立隧道并阻塞到 ctx 结束。frpc 内部自带断线重连，这里不需要额外重试。
func Run(ctx context.Context, cfg Config) error {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return err
	}

	common := &v1.ClientCommonConfig{
		ServerAddr: cfg.ServerAddr,
		ServerPort: cfg.ServerPort,
		Auth: v1.AuthClientConfig{
			Method: v1.AuthMethodToken,
			Token:  cfg.Token,
		},
		Log: v1.LogConfig{
			To:      "console",
			Level:   cfg.LogLevel,
			MaxDays: 3,
		},
	}
	if err := common.Complete(); err != nil {
		return fmt.Errorf("frptunnel: 组装客户端配置失败: %w", err)
	}

	proxy := &v1.TCPProxyConfig{
		ProxyBaseConfig: v1.ProxyBaseConfig{
			Name: cfg.ProxyName,
			Type: "tcp",
			ProxyBackend: v1.ProxyBackend{
				LocalIP:   cfg.LocalIP,
				LocalPort: cfg.LocalPort,
			},
		},
		RemotePort: cfg.RemotePort,
	}

	configSource := source.NewConfigSource()
	if err := configSource.ReplaceAll([]v1.ProxyConfigurer{proxy}, nil); err != nil {
		return fmt.Errorf("frptunnel: 注册代理失败: %w", err)
	}

	svr, err := client.NewService(client.ServiceOptions{
		Common:                 common,
		ConfigSourceAggregator: source.NewAggregator(configSource),
	})
	if err != nil {
		return fmt.Errorf("frptunnel: 创建 frpc 服务失败: %w", err)
	}
	defer svr.Close()

	log.InitLogger(common.Log.To, common.Log.Level, int(common.Log.MaxDays), common.Log.DisablePrintColor)

	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		svr.Close()
	}()

	err = svr.Run(ctx)
	<-done
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("frptunnel: frpc 退出: %w", err)
	}
	return nil
}

// DefaultRetryBackoff 供调用方在隧道异常退出后重新拉起时使用。
func DefaultRetryBackoff() time.Duration { return 5 * time.Second }
