// Package lanxin 是蓝信个人机器人 API 的 Go SDK。
//
// 协议来源：蓝信开放平台《个人机器人接入指南》
// (https://developer.lanxin.cn/ 官方文档，2026-07-20 版本；离线副本见
// docs/integrations/lanxin-personal-bot-api.md)。
//
// 要点：
//   - 认证：GET /v1/apptoken/create 换 appToken（7200s，提前 300s 刷新），
//     业务 API 在 URL 上拼 app_token 参数。
//   - 接收：WebSocket 长连接（/v1/ws/endpoint/create 换地址），
//     ping 30s / pong 10s / 70s 无响应判死，指数退避重连 [2s,5s,10s,30s,60s]。
//   - 发送：私聊 /v1/bot/messages/create，群聊 /v1/messages/group/create。
//   - 网关：公有云 https://open.e.lanxin.cn/open/apigw，企业私有部署自定义。
//
// 本 SDK 只依赖标准库与 gorilla/websocket。
package lanxin
