// Package release 实现发版服务与 release-mcp 工具：
// ready_to_ship 汇总、Release Note、版本号、构建签名编排、发布验证与回滚。
// 门禁自判，不信任 Syntropy automation 的 succeeded 语义（缺口 G4）。
package release
