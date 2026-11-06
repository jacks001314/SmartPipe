package main

import (
	"testing"

	"smartpipe/backend/internal/bot"
)

// 配置台首次可用（启动时 admin-api 还没起）或轮换密钥时，
// 轮询必须发现差异并重连；旧实现拿「已合并的 target」和 rc 比较，永远相等。
func TestSameLanxinDetectsRemoteChange(t *testing.T) {
	envCfg := bot.Config{AppID: "placeholder", AppSecret: "placeholder", APIBase: "https://open.e.lanxin.cn/open/apigw", RequireAtMe: true}
	rc := &bot.RemoteLanxinConfig{AppID: "2285568-15426304", AppSecret: "s1", APIBase: "https://apigw.lx.qianxin.com", RequireAtMe: true}

	target := envCfg.WithRemote(rc)
	if sameLanxin(envCfg, target) {
		t.Fatal("expected change from env fallback to remote config")
	}
	if !sameLanxin(target, envCfg.WithRemote(rc)) {
		t.Fatal("expected identical remote configs to compare equal")
	}

	rotated := &bot.RemoteLanxinConfig{AppID: rc.AppID, AppSecret: "s2", APIBase: rc.APIBase, RequireAtMe: true}
	if sameLanxin(target, envCfg.WithRemote(rotated)) {
		t.Fatal("expected secret rotation to trigger reconnect")
	}
}

func TestSameLanxinDetectsRequireAtMeChange(t *testing.T) {
	envCfg := bot.Config{AppID: "a", AppSecret: "s", APIBase: "b", RequireAtMe: true}
	target := envCfg.WithRemote(&bot.RemoteLanxinConfig{AppID: "a", AppSecret: "s", APIBase: "b", RequireAtMe: false})
	if sameLanxin(envCfg, target) {
		t.Fatal("expected require_at_me change to trigger reconnect")
	}
}
