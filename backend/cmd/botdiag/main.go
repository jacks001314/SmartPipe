// botdiag 蓝信机器人连通性自检（只读，不发送任何消息）：
//  1. 获取 AppToken（验证凭证与网关）
//  2. 获取 WebSocket 地址（验证 WS 通道）
//  3. 与 WS 网关完成握手（验证网络连通）
//  4. 查询机器人所在群列表（验证业务权限；企业部署可能 10005）
//
// 用法：设置 LANXIN_APP_ID / LANXIN_APP_SECRET / LANXIN_API_BASE 后运行。
package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"time"

	"github.com/gorilla/websocket"

	"smartpipe/sdks/lanxin"
)

func main() {
	appID := os.Getenv("LANXIN_APP_ID")
	secret := os.Getenv("LANXIN_APP_SECRET")
	base := os.Getenv("LANXIN_API_BASE")
	if base == "" {
		base = lanxin.DefaultAPIBase
	}
	if appID == "" || secret == "" {
		log.Fatal("LANXIN_APP_ID / LANXIN_APP_SECRET 未设置")
	}

	client := lanxin.New(appID, secret, lanxin.WithAPIBase(base))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Println("gateway:", base)

	// 1. AppToken
	tok, err := client.AppToken(ctx)
	if err != nil {
		log.Fatalf("[1/4] AppToken 失败: %v", err)
	}
	fmt.Println("[1/4] AppToken OK:", mask(tok))

	// 2. WS 地址
	ep, err := client.WSEndpoint(ctx)
	if err != nil {
		log.Fatalf("[2/4] WS endpoint 失败: %v", err)
	}
	fmt.Println("[2/4] WS endpoint OK:", maskURL(ep))

	// 3. WS 握手（连上即断，不读消息）
	dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
	defer dcancel()
	conn, resp, err := websocket.DefaultDialer.DialContext(dctx, ep, nil)
	if err != nil {
		log.Fatalf("[3/4] WS 握手失败: %v", err)
	}
	conn.Close()
	fmt.Println("[3/4] WS 握手 OK (http", resp.StatusCode, ")")

	// 4. 群列表（只读）
	ref, err := client.ListGroups(ctx, 0, 100)
	if err != nil {
		fmt.Printf("[4/4] 群列表失败（企业部署常见 10005=无权限）: %v\n", err)
		return
	}
	fmt.Printf("[4/4] 群列表 OK: %d 个群 %v\n", ref.TotalGroupIDs, ref.GroupIDs)
}

func mask(s string) string {
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "…" + s[len(s)-4:]
}

func maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparsable)"
	}
	return u.Scheme + "://" + u.Host + u.Path[:min(len(u.Path), 12)] + "…"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
