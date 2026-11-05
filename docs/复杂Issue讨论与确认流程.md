# 复杂 Issue 的 GitHub 讨论与确认流程

日期：2026-11-05  
适用范围：由蓝信反馈进入 SmartPipe 后，被判断为复杂任务的 Issue

## 1. 目标

复杂 Issue 不能由 lead agent 直接进入开发。必须先建立 GitHub Issue，在评论区完成方案讨论，并由有权限的维护者明确确认某个版本的方案后，才能派生 dev 子 Agent。

这条门禁要同时满足三件事：

- 讨论过程可追溯：问题、方案、反对意见、修改记录都在 GitHub Issue 评论里。
- 确认是机器可判定的：不是“看起来同意了”，而是某个有权限的人对某个明确的方案版本执行了确认。
- 确认不可绕过：即使 lead agent 判断错误，工具层也必须拒绝未批准的开发请求。

## 2. 什么算复杂 Issue

lead agent 先做复杂度判断。满足任意一条即进入 GitHub 讨论流程：

- 涉及多个模块、服务或仓库。
- 需要改数据库结构、接口协议、权限模型或配置格式。
- 涉及安全、认证、加密、发布流程等敏感路径。
- 需要跨团队协作，或依赖外部系统改造。
- 预计实现超过 1 个工作日。
- 验收条件不清晰，或存在多个可选方案。
- lead 自己无法判断是否复杂。

判断不确定时默认按复杂处理。反之，单文件、可复现、验收明确的小 Bug 可以直接进入开发，不创建 GitHub Issue，避免讨论噪音。

复杂度判断结果和理由要写入内部 Issue，便于复盘；lead 不能只凭一句“复杂”就跳过实现。

## 3. GitHub Issue 的建立与关联

复杂 Issue 由 lead 通过 GitHub 工具创建，或在已存在时关联：

- 标题使用内部 Issue 的结构化标题。
- 正文包含：问题描述、影响范围、复现信息、已知约束、待确认问题。
- 正文底部写入内部关联标记，例如 `<!-- smartpipe-issue: ISS-202611-0007 -->`。
- 创建后把 `github_repo`、`github_issue_number`、`github_issue_url` 写回内部 Issue。
- 内部 Issue 状态从 `triaged` 进入 `discussing`。
- 给 GitHub Issue 打上 `smartpipe:discussing` 标签。

如果用户直接在 GitHub 提了 Issue，则由 ingress 反向建立内部 Issue 并关联，避免两套编号各自演化。

## 4. 评论区讨论协议

### 4.1 方案版本

lead 在评论区发布方案时必须带版本号，例如：

```markdown
## 方案 v3

- 改动范围：...
- 实现步骤：...
- 风险与回滚：...
- 验收标准：...
- 待确认问题：...

<!-- smartpipe-plan:v3 hash:8f3c... -->
```

版本号单调递增。方案内容变化后必须发布新版本，不能覆盖旧版本后继续沿用旧确认。

### 4.2 评论命令

维护者使用明确命令表达决策：

| 命令 | 含义 |
|---|---|
| `/plan` | 请 lead 补充或刷新方案 |
| `/approve v3` | 确认 v3 方案，可以进入开发 |
| `/changes <说明>` | 要求修改方案，暂不开发 |
| `/hold` | 暂停讨论和开发 |
| `/reject <说明>` | 拒绝该 Issue |

普通评论只作为讨论内容，不产生开发许可。表情回应、单纯的“可以”“OK”、机器人转述都不算确认。

### 4.3 谁可以确认

确认人必须同时满足：

- 是 GitHub 仓库的 `OWNER`、`MEMBER`、`COLLABORATOR`，或拥有该仓库写权限。
- 如果仓库配置了 `CODEOWNERS`，还应覆盖本次改动涉及的路径；跨模块改动需要对应模块的负责人确认。
- 不能是 SmartPipe 机器人自己，也不能是 lead 代发的蓝信用户评论。

服务端在写入 `approved` 状态前重新查询 GitHub 权限，不能只相信评论里的身份文本。

## 5. 状态机

在原有 Issue 状态机中增加讨论和确认状态：

```
triaged
  ├─ 简单任务 ──────────────────────────► in_dev
  └─ 复杂任务 ──► discussing ──► awaiting_approval
                       ▲                 │
                       │   /changes      │ /approve vN
                       └─────────────────┤
                                         ▼
                                      approved ──► in_dev
                                         │
                                         └─ /hold 或 /reject ──► postponed / rejected
```

规则：

- `discussing`：lead 已发方案，正在收集意见。
- `awaiting_approval`：方案版本已固定，等待有权限的人确认。
- `approved`：确认的是某个具体方案版本，不是整个 Issue 的永久通行证。
- `approved` 后方案若发生变化，状态回到 `discussing`，旧确认自动失效。
- `in_dev` 只能由服务端在确认校验通过后写入；lead 不能直接把状态改成 `in_dev`。

## 6. GitHub 事件接入

使用 GitHub App + Webhook，不轮询：

- 订阅 `issue_comment` 的 created / edited / deleted。
- 订阅 `issues` 的 labeled / unlabeled / closed / reopened。
- 校验 `X-Hub-Signature-256`。
- 用 `X-GitHub-Delivery` 做幂等，重复投递只处理一次。
- 通过 `repo + issue_number` 找到内部 Issue 和对应的 lead 任务。
- 机器人自己的评论、由机器人代发的评论直接忽略，避免自循环。
- 评论事件追加到讨论日志，并把新评论投递给 lead；如果 lead 任务已结束，创建一个轻量的“讨论跟进”任务，带上 Issue 摘要和最近评论。

lead 不需要一直保持运行等待评论。讨论期间任务可以休眠，Webhook 到达后再唤醒或新建跟进任务。

## 7. 确认校验与防绕过

开发入口必须放在服务端工具里，而不是只写在提示词中：

```
lead 请求派生 dev
  → smartpipe-mcp 检查内部 Issue
      ├─ status 不是 approved → 拒绝，返回 approval_required
      ├─ plan_version / plan_hash 与批准记录不一致 → 拒绝
      ├─ 批准人权限已失效 → 拒绝并回到 discussing
      └─ 全部通过 → 才允许创建 dev 子 Agent
```

批准记录至少保存：

- 内部 Issue ID、GitHub Issue 编号。
- 方案版本和内容哈希。
- 批准评论 ID、评论人、GitHub 用户 ID。
- 批准时间、权限校验结果。
- 方案之后是否被修改。

这样 `/approve v3` 不能顺带批准之后的 v4；也不会因为有人改了方案而沿用旧许可。

## 8. 蓝信侧如何参与

复杂 Issue 的讨论主战场是 GitHub，蓝信用户不一定有 GitHub 账号：

- lead 在蓝信回复：“问题较复杂，已在 GitHub #123 发起方案讨论，当前等待确认”，附内部可访问的链接。
- 有新评论或方案变化时，lead 给蓝信用户发简要摘要，只同步决策、待确认问题和影响，不逐条搬运评论。
- 蓝信用户回复的内容，由 lead 以“via 蓝信用户 XXX”的形式转贴到 GitHub 评论，并明确标注这是转述，不能作为批准。
- 维护者在 GitHub 确认后，lead 再通知蓝信用户进入开发及预计发版时间。
- 需要蓝信用户本人确认产品取舍时，仍由有 GitHub 权限的维护者在 GitHub 上完成最终 `/approve`，避免身份映射缺失导致越权。

## 9. 数据字段

内部 Issue 增加：

```text
github_repo
github_issue_number
github_issue_url
complexity            simple | complex
discussion_status     none | discussing | awaiting_approval | approved | changes_requested | rejected
plan_version
plan_hash
approved_by
approved_at
approval_comment_id
approval_plan_version
last_github_event_at
```

GitHub 标签由服务端维护：

- `smartpipe:discussing`
- `smartpipe:awaiting-approval`
- `smartpipe:approved`
- `smartpipe:in-dev`
- `smartpipe:blocked`
- `smartpipe:released`

标签只是可视化状态，审批事实以内部数据库中的批准记录为准。

## 10. 验收用例

1. 复杂 Issue 自动创建或关联 GitHub Issue，状态为 `discussing`，不会派生 dev。
2. 普通评论只进入讨论日志，不会触发开发。
3. 无权限用户的 `/approve v1` 被拒绝并留下审计记录。
4. 有权限维护者的 `/approve v1` 通过后，状态变为 `approved`，才允许派生 dev。
5. 批准后方案改为 v2，旧批准失效，dev 被阻断，直到 `/approve v2`。
6. `/changes` 后 lead 更新方案并重新等待确认。
7. 重复的 GitHub Webhook 不会重复创建任务或重复评论。
8. GitHub 暂时不可用时，事件进入重试队列，不自动批准、不自动开发。
9. 蓝信用户收到讨论摘要和最终确认结果。
10. 机器人自己的评论不会触发新的 Agent 回合。

## 11. 失败处理

- GitHub API 限流或超时：退避重试，状态保持 `discussing`，不降级为自动批准。
- 评论人权限查询失败：按未批准处理。
- 方案版本或哈希缺失：按未批准处理。
- lead 试图绕过状态直接派生 dev：工具拒绝并记录审计事件。
- 讨论超过 SLA：通知维护者和蓝信用户，状态保持等待，不自动推进。
