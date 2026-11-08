# GitHub 客户端选型记录

日期：2026-11-08

## 结论

采用 `github.com/google/go-github/v68` 作为 GitHub REST 访问底层，保留项目内的
`internal/github.Client` 对外接口，调用方（bridge / release / admin-api）不需要改动。

## 为什么换

原客户端是 570 行手写实现，只覆盖 Issue、评论、标签和 Release 等少量接口。继续加
PR、Review、Checks、分页检索时，手写路径会不断累积。官方库直接提供：

- 类型化的 service 与请求/响应结构；
- 分页（`ListOptions`）、限流错误、`ErrorResponse` 状态码；
- GitHub Enterprise 的 BaseURL/UploadURL 处理；
- Release 附件上传、Git refs、Issue 标签等标准路径。

## 保留的项目行为

- 422「标签不存在」时先 `EnsureLabel` 再重试一次；
- `api.github.com` → `uploads.github.com`、企业版 `api.*` → `uploads.*`；
- GitHub App RS256 JWT 与 installation token 交换；
- 对外类型 `Issue` / `Release` / `Asset` / `APIError` 与 `IsNotFound` 语义不变。

## 验证

- `internal/github`、`internal/bridge`、`internal/release` 单测通过；
- 带真实 Postgres 的全量 `go test -p 1 ./...` 通过；
- 线上镜像已重新构建并部署，healthz 正常，bot 重新连上蓝信。

后续新增 GitHub 能力优先使用 go-github 的 service，不再手写 REST 路径。
