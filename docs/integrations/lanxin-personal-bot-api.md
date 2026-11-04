
<!--
来源: https://developer.lanxin.cn/official/article?id=646ecae03d4e4adb7039c0e4&module=development-help&article_id=69fc297394d50cdd2d8922db
标题: 个人机器人接入指南（蓝信个人机器人 API 对接文档，文档落款 2026-07-20）
抓取日期: 2026-11-04。仅作离线参考，以官方在线文档为准。
-->

# 蓝信个人机器人 API 对接文档
本文档面向任何想要对接蓝信个人机器人的第三方 Agent 服务，覆盖完整的 API 接口说明、消息格式、认证流程和对接步骤。

----

## 概述
蓝信是面向企业和政府机构的即时通讯平台。蓝信支持"个人机器人"——

- 应用类型：个人机器人（Personal Bot），非组织机器人

- 消息通道：私聊（P2P）和群聊

- 连接方式：WebSocket 长连接（接收消息）+ HTTP API（发送消息/查询）

- 消息格式：支持纯文本、Markdown 富文本、图片、视频、文件、语音、多种卡片

- 特殊说明：个人机器人仅能接收其创建者的私聊消息，群聊中可被任何群成员 @ 触发

----

## 1. 获取凭证

### 操作步骤

- 打开蓝信桌面端

- 通讯录 → 智能机器人 → 个人机器人

- 创建新机器人，或点击已有机器人右侧的 ℹ️ 图标

- 查看 App ID 和 App Secret

蓝信桌面端
  └── 通讯录（左侧导航）
       └── 智能机器人 或 Bots
            └── 个人机器人 或 Personal Bots
                 └── 点击机器人 → ℹ️ 图标 → 查看 App ID / App Secret

### 注意事项

- 仅桌面端支持查看凭证，移动端不支持

- 个人机器人（Personal Bot）仅创建者自己可与机器人私聊，群聊不受此限制

- App ID 格式一般为 orgId-appId，如 13107200-4218880

- App Secret 为随机字符串，需妥善保管

----

## 2. 认证机制
蓝信 API 使用 两步认证：

### Step 1：获取 App Token
GET {apiGatewayUrl}/v1/apptoken/create?grant_type=client_credential&appid={appId}&secret={appSecret}

请求方式：GET，参数在 query string
成功响应：
{
  "errCode": 0,
  "errMsg": "",
  "data": {
    "appToken": "eyJhbGciOi...",
    "expiresIn": 7200
  }
}

- appToken：后续所有 API 调用的凭证

- expiresIn：过期时间（秒），通常为 7200 秒（2 小时）

缓存建议：建议在 expiresIn - 300 秒时提前刷新，避免临界过期。

### Step 2：使用 App Token 调用 API
所有业务 API 均需在 URL 上拼接 app_token 参数：
POST {apiGatewayUrl}/v1/bot/messages/create?app_token={appToken}

例外：获取 WebSocket 地址的接口不使用 appToken，而是直接传 appId + secret 在 request body 中。

----

## 3. API 网关地址
环境地址
 | 蓝信公有云（默认） | https://open.e.lanxin.cn/open/apigw
 | 企业私有部署 | 由企业管理员提供（如 https://apigw.lx.qianxin.com）

注意：除非确认使用的是蓝信公有云，否则需要向企业管理员索要正确的 API 网关地址。大多数企业部署使用私有网关。

----

## 4. 接收消息的方式
蓝信个人机器人使用 WebSocket 长连接回调消息事件
优点缺点
 | • 实时推送，延迟低• 无需公网 IP• 在公司内网也可运行 | • 需要维护长连接• 需要处理断线重连• 网络切换后可能成为僵尸连接

#### 4.1 获取 WebSocket 地址
POST {apiGatewayUrl}/v1/ws/endpoint/create
Content-Type: application/json

{
  "appId": "13107200-4218880",
  "secret": "your-app-secret"
}

成功响应：
{
  "errCode": 0,
  "errMsg": "",
  "data": {
    "wsEndpoint": "wss://ws.lanxin.cn/ws/v1/..."
  }
}

#### 4.2 建立 WebSocket 连接
使用标准 WebSocket 客户端连接返回的 wsEndpoint 地址。
心跳保活：

- 每 30 秒发送一次 ping 帧

- 期望 10 秒内收到 pong 响应

- 超过 70 秒未收到 pong 应视为连接已死，主动断开重连

断线重连：

- 建议指数退避策略：[2s, 5s, 10s, 30s, 60s]

- 每次重连前重新调用获取 WS 地址的接口（地址可能变化）

收到消息的格式见下文「6A. WebSocket 接收消息格式」。

----

## 5. API 接口列表
所有接口的基础响应格式统一为：
{
  "errCode": 0,
  "errMsg": "",
  "data": { ... }
}

- errCode=0 表示成功，非 0 表示失败

- errMsg 失败时包含错误描述

----

### 5.1 发送私聊消息
POST {apiGatewayUrl}/v1/bot/messages/create?app_token={appToken}
Content-Type: application/json

请求体：
{
  "userIdList": ["目标用户ID"],
  "msgType": "text | formatText | linkCard | appCard | approveCard | appArticles | i18nAppCard",
  "msgData": {
    "text": { "content": "消息内容" }
  },
  "refMsgId": "引用消息ID（可选）"
}

----

### 5.2 发送群聊消息
POST {apiGatewayUrl}/v1/messages/group/create?app_token={appToken}
Content-Type: application/json

请求体：
{
  "groupId": "群ID",
  "msgType": "text | formatText | linkCard | appCard | approveCard | appArticles | i18nAppCard",
  "msgData": {
    "text": { "content": "消息内容" }
  },
  "refMsgId": "引用消息ID（可选）"
}

### 5.3 上传媒体文件
POST {apiGatewayUrl}/v1/app/medias/create?type={image|video|audio|file}&app_token={appToken}
Content-Type: multipart/form-data

表单字段：
字段说明
 | media | 文件内容（multipart/form-data 文件字段）
 | type | 媒体类型：image、video、audio、file

视频额外参数（当 type=video 时附加在 URL query string）：

- width：视频宽度（像素）

- height：视频高度（像素）

- duration：视频时长（秒）

成功响应：
{
  "errCode": 0,
  "errMsg": "",
  "data": {
    "mediaId": "abc123..."
  }
}

----

### 5.4 下载媒体文件
GET {apiGatewayUrl}/v1/medias/{mediaId}/fetch?app_token={appToken}

成功响应：直接返回文件二进制数据，Content-Type 根据文件类型设置。

----

### 5.5 撤回消息
POST {apiGatewayUrl}/v1/messages/revoke?app_token={appToken}
Content-Type: application/json

请求体：
{
  "chatType": "bot | group",
  "messageIds": ["msgId1", "msgId2"],
  "senderId": "发送者ID（仅群聊需要）"
}

----

### 5.6 更新卡片状态
POST {apiGatewayUrl}/v1/messages/dynamic/update?app_token={appToken}
Content-Type: application/json

更新 approveCard（审批卡片）：
{
  "msgId": "消息ID",
  "msgType": "approveCard",
  "msgData": {
    "approveCardUpdateMsg": {
      "headStatus": {
        "describe": "已批准",
        "colour": "#198754",
        "statusIcon": 1
      },
      "buttons": [
        { "text": "已允许执行一次", "buttonTheme": 1, "state": 1 }
      ]
    }
  }
}

更新 appCard（动态卡片）：
{
  "msgId": "消息ID",
  "msgType": "appCard",
  "msgData": {
    "appCardUpdateMsg": {
      "headStatusInfo": {
        "description": "<div style=\"color:#198754\">已完成</div>",
        "colour": "#198754"
      },
      "links": [
        { "title": "查看详情", "url": "https://..." }
      ],
      "isLastUpdate": true
    }
  }
}

----

### 5.7 发送带 @提醒的消息
在 msgData 中添加 reminder 字段（群聊专用）：
{
  "groupId": "群ID",
  "msgType": "text",
  "msgData": {
    "text": {
      "content": "消息内容",
      "reminder": {
        "all": false,
        "userIds": ["用户ID1", "用户ID2"],
        "botIds": ["机器人ID"]
      }
    }
  }
}

----

### 5.8 引用回复消息
在请求体顶层添加 refMsgId 字段：
{
  "userIdList": ["目标用户ID"],
  "msgType": "text",
  "msgData": { "text": { "content": "引用回复内容" } },
  "refMsgId": "要引用的消息ID"
}

----

## 6. 消息类型详解

### 6A. WebSocket 接收消息格式
WebSocket 推送的消息为 JSON 文本。蓝信 WebSocket 会实时回调机器人所在群的全部消息，接入方需根据 reminder.isAtMe 等字段自行判断是否处理某条消息。

#### 群聊消息
{
  "events": [
    {
      "type": "bot_group_message",
      "data": {
        "msgType": "text",
        "msgData": {
          "text": { "content": "你好" }
        },
        "chatType": "group",
        "from": "发送者staffId",
        "senderName": "发送者姓名",
        "fromType": 0,
        "groupId": "群ID",
        "groupName": "群名称",
        "conversationId": "会话ID",
        "messageId": "消息ID",
        "msgId": "消息唯一ID",
        "entryId": "企业租户ID",
        "botCreator": "机器人创建者ID",
        "botId": "机器人ID",
        "magic": "消息序号",
        "reminder": {
          "isAtMe": true,
          "isAtAll": false,
          "bots": [{ "botId": "xxx", "botName": "机器人名" }],
          "staffs": [{ "staffId": "xxx", "staffName": "用户名" }]
        },
        "referenceMsg": {
          "from": "引用消息发送者ID",
          "senderName": "发送者姓名",
          "fromType": 0,
          "msgType": "text",
          "msgData": { "text": { "content": "被引用的消息内容" } },
          "referenceMsg": null
        }
      }
    }
  ]
}

#### 私聊消息
{
  "events": [
    {
      "type": "bot_message",
      "data": {
        "msgType": "text",
        "msgData": {
          "text": { "content": "你好" }
        },
        "chatType": "p2p",
        "from": "发送者staffId",
        "senderName": "发送者姓名",
        "conversationId": "会话ID",
        "messageId": "消息ID",
        "msgId": "消息唯一ID"
      }
    }
  ]
}

注意：个人机器人仅能接收其创建者的私聊消息，其他用户的私聊消息不会被推送。
关键字段说明：
字段说明
 | events[].type | 事件类型：bot_message（私聊）、bot_group_message（群聊）、approve_card_callback（审批卡片按钮回调）
 | data.chatType | "p2p"（私聊）或 "group"（群聊）
 | data.from | 发送者的 staff ID
 | data.fromType | 0=用户，1=应用（bot）
 | data.groupId | 群 ID（群聊时存在）
 | data.messageId | 消息序号
 | data.msgId | 消息唯一 ID（用于引用回复的 refMsgId）
 | data.reminder | 提醒信息，可为 null（非 @消息时）
 | data.reminder.isAtMe | 是否 @了本机器人
 | data.reminder.isAtAll | 是否 @所有人
 | data.reminder.bots | 被 @ 的机器人列表，含 botId/botName
 | data.reminder.staffs | 被 @ 的人员列表，含 staffId/staffName
 | data.referenceMsg | 被引用的原始消息（嵌套结构，可能多层）

#### box（聊天记录转发）入站示例
{
  "type": "bot_group_message",
  "data": {
    "msgType": "box",
    "msgData": {
      "box": {
        "content": "群名称的消息记录",
        "sendTime": "1783676567385101",
        "sequence": 744,
        "messageItems": [
          {
            "sendTime": "1783594786016554",
            "from": "staff-id-1",
            "msgType": "text",
            "msgData": { "text": { "content": "第一条消息" } }
          },
          {
            "sendTime": "1783594822779531",
            "from": "staff-id-2",
            "msgType": "image",
            "msgData": { "image": { "mediaIds": ["mediaId123"], "content": "图片描述" } }
          }
        ],
        "msgBoxType": 1
      }
    },
    "from": "发送者staffId",
    "senderName": "发送者姓名",
    "groupId": "群ID",
    "groupName": "群名称",
    "reminder": null
  }
}

box 字段说明：
字段说明
 | content | 聊天记录标题
 | sendTime | 转发操作的发送时间（微秒时间戳）
 | messageItems | 子消息列表，每条包含 sendTime/from/msgType/msgData
 | msgBoxType | 1=聊天记录（合并转发），2=引用的聊天记录

注意：API 只返回一层 messageItems，嵌套的聊天记录不会展开。

#### approveCard 入站示例
{
  "type": "bot_group_message",
  "data": {
    "msgType": "approveCard",
    "msgData": {
      "approveCard": {
        "title": "审批标题",
        "formatType": "markdown",
        "text": "审批内容正文",
        "sequence": 432
      }
    },
    "from": "发送者staffId",
    "senderName": "发送者姓名",
    "groupId": "群ID",
    "groupName": "群名称",
    "reminder": null
  }
}

注意：入站 approveCard 为扁平格式 { title, formatType, text, sequence }，与出站发送时的嵌套 head/body/buttons 结构不同。

### 6B. 审批卡片按钮回调
用户点击 approveCard 的按钮后，WebSocket 会推送：
{
  "type": "approve_card_callback",
  "data": {
    "eventData": "callbackInfo原值",
    "staffId": "点击者staffId"
  }
}

eventData 是发送卡片时按钮 callbackInfo 字段的原样回传，接入方可自行定义其格式。例如发送时设置 "callbackInfo": "once:req-001"，回调中 eventData 就是 "once:req-001"。

----

### 6C. 出站消息类型

#### text（纯文本）
{
  "msgType": "text",
  "msgData": {
    "text": {
      "content": "你好，这是纯文本消息"
    }
  }
}

#### formatText（Markdown 富文本）
{
  "msgType": "formatText",
  "msgData": {
    "formatText": {
      "formatType": 1,
      "text": "**粗体** _斜体_ `代码`"
    }
  }
}

- 支持标准 Markdown 语法

- formatType 固定为 1

#### image / video / file / voice（媒体消息）
媒体消息需要先上传获取 mediaId，然后在 text 消息中引用：
{
  "msgType": "text",
  "msgData": {
    "text": {
      "content": "这是图片说明",
      "mediaType": 2,
      "mediaIds": ["mediaId123"]
    }
  }
}

mediaType 枚举：
值类型
 | 1 | 视频
 | 2 | 图片
 | 3 | 文件
 | 4 | 语音

视频消息特殊要求：mediaIds 需包含两个 ID [videoMediaId, coverImageMediaId]（视频 + 封面图）。

----

### 6D. 入站消息类型汇总
从 WebSocket 接收的消息 msgType 可能的值：
msgType说明msgData 路径
 | text | 纯文本 | msgData.text.content
 | format | Markdown 富文本 | msgData.format.text
 | image | 图片 | msgData.image.mediaIds[]，可带 msgData.image.content（图片描述）
 | video | 视频 | msgData.video.mediaIds[]，可带 msgData.video.content（视频描述）
 | file | 文件 | msgData.file.mediaIds[]，可带 msgData.file.content（文件描述）
 | voice | 语音 | msgData.voice.mediaIds[]，可带 msgData.voice.content（语音描述）
 | position | 位置 | msgData.position.{name,address,latitude,longitude}
 | card | 名片 | msgData.card.staffId
 | sticker | 贴纸 | msgData.sticker.stickerId
 | approveCard | 审批卡片 | msgData.approveCard.{title,formatType,text}（扁平格式）
 | box | 聊天记录转发 | msgData.box.{content,messageItems[],msgBoxType}
 | i18nAppCard | 国际化应用卡片 | msgData.i18nAppCard.i18nBodyTitle
 | systemAction | 系统消息 | msgData.systemAction.content

----

## 7. 卡片类型

### 7.1 linkCard（链接卡片）
{
  "msgType": "linkCard",
  "msgData": {
    "linkCard": {
      "title": "卡片标题",
      "link": "https://example.com",
      "description": "卡片描述",
      "iconLink": "https://example.com/icon.png",
      "pcLink": "https://example.com/pc",
      "fromName": "来源名称",
      "fromIconLink": "https://example.com/source-icon.png"
    }
  }
}

### 7.2 appCard（应用卡片）
支持 div-style 格式化（color、font-size、text-align、text-indent）：
{
  "msgType": "appCard",
  "msgData": {
    "appCard": {
      "headTitle": "卡片头部标题",
      "headIconUrl": "https://...",
      "bodyTitle": "<div style=\"font-size:16pt;color:#333\">正文标题</div>",
      "bodySubTitle": "副标题",
      "bodyContent": "<div style=\"font-size:14pt;color:#666;text-indent:0em\">正文内容</div>",
      "signature": "签名行",
      "staffId": "员工ID（用于显示头像）",
      "isDynamic": false,
      "fields": [
        { "key": "键", "value": "值" }
      ],
      "links": [
        { "title": "链接标题", "url": "https://..." }
      ],
      "cardLink": "https://...",
      "pcCardLink": "https://...",
      "padCardLink": "https://..."
    }
  }
}

CSS 格式限制：

- font-size：必须使用 pt 单位（12pt–36pt），不能使用 px

- text-indent：必须带单位（如 0em），裸写 0 会导致发送失败

- 支持的属性：color、font-size、text-align、text-indent

动态卡片（isDynamic: true）：

- 必须设置 headStatusInfo 字段：

- 可通过「5.6 更新卡片状态」接口更新

### 7.3 approveCard（审批卡片）
{
  "msgType": "approveCard",
  "msgData": {
    "approveCard": {
      "head": {
        "title": "⚠️ 命令审批",
        "headStatus": {
          "describe": "待审批",
          "statusIcon": 1,
          "colour": "#FFB116"
        }
      },
      "body": {
        "title": "命令审批请求",
        "content": {
          "formatType": 1,
          "text": "请审批以下命令：\n```\nrm -rf /\n```"
        },
        "fields": [
          { "key": "会话", "value": "agent:main:lansenger:..." }
        ]
      },
      "buttons": [
        {
          "text": "执行一次",
          "buttonTheme": 1,
          "callbackInfo": "once:req-001",
          "permissionScope": {
            "permittedStaffs": ["owner-staff-id"]
          }
        },
        {
          "text": "本会话有效",
          "buttonTheme": 2,
          "callbackInfo": "session:req-001"
        },
        {
          "text": "拒绝执行",
          "buttonTheme": 4,
          "callbackInfo": "deny:req-001"
        }
      ],
      "expireTime": 300,
      "reminder": {
        "userIds": ["被提醒用户ID"]
      }
    }
  }
}

按钮主题（buttonTheme）：
值样式
 | 1 | 主色（蓝色）
 | 2 | 次要色（浅蓝）
 | 3 | 次要色（黑色）
 | 4 | 警告色（红色）

按钮状态（state）：
值状态
 | 0 | 可用
 | 1 | 禁用
 | 2 | 隐藏

权限范围（permissionScope）：
{
  "permittedStaffs": ["允许点击的用户ID"],
  "prohibitedStaffs": ["禁止点击的用户ID"]
}

### 7.4 appArticles（图文卡片）
{
  "msgType": "appArticles",
  "msgData": {
    "appArticles": [
      {
        "title": "文章标题",
        "imgUrl": "https://example.com/cover.jpg",
        "url": "https://example.com/article",
        "pcUrl": "https://example.com/pc",
        "padUrl": "https://example.com/pad",
        "summary": "文章摘要"
      }
    ]
  }
}

### 7.5 i18nAppCard（国际化应用卡片）
{
  "msgType": "i18nAppCard",
  "msgData": {
    "i18nAppCard": {
      "i18nHeadTitle": {
        "zhHans": "标题简体",
        "zhHant": "標題繁體",
        "zhHantHK": "標題繁體HK",
        "en": "Title EN",
        "fr": "Titre FR"
      },
      "headIconId": "图标mediaId",
      "i18nBodyTitle": { "zhHans": "...", "en": "..." },
      "i18nBodySubTitle": { "zhHans": "...", "en": "..." },
      "i18nBodyContent": { "zhHans": "...", "en": "..." },
      "i18nSignature": { "zhHans": "...", "en": "..." },
      "i18nFields": [
        {
          "i18nKey": { "zhHans": "名称", "en": "Name" },
          "i18nValue": { "zhHans": "张三", "en": "Zhang San" },
          "timestamp": 1234567890
        }
      ],
      "cardLink": "https://...",
      "pcCardLink": "https://..."
    }
  }
}

----

## 8. 机器人斜杠命令
蓝信支持注册机器人斜杠命令，用户在输入框中输入 / 即可看到命令列表。

### 8.1 注册命令
POST {apiGatewayUrl}/v1/bot/commands/create?app_token={appToken}
Content-Type: application/json

{
  "scopeType": 5,
  "commands": [
    {
      "command": "hello",
      "description": "打招呼",
      "i18nDescription": {
        "zhHans": "打招呼",
        "zhHant": "打招呼",
        "en": "Say hello"
      }
    }
  ]
}

### 8.2 scopeType 枚举
值范围说明
 | 1 | 单个指定群的单个成员 | 需要同时传 chatId、chatType="group"、staffId
 | 2 | 指定单个群的管理员 | 需要同时传 chatId、chatType="group"
 | 3 | 单个指定聊天 | 需要同时传 chatId、chatType
 | 4 | 所有群管理员 | 无需额外参数
 | 5 | 所有群 | 无需额外参数
 | 6 | 所有私聊 | 无需额外参数
 | 7 | 默认全局 | 无需额外参数

### 8.3 查询命令
POST {apiGatewayUrl}/v1/bot/commands/fetch?app_token={appToken}
Content-Type: application/json

{ "scopeType": 5 }

### 8.4 删除命令
POST {apiGatewayUrl}/v1/bot/commands/delete?app_token={appToken}
Content-Type: application/json

{ "scopeType": 5 }

（删除当前 scopeType 下的所有命令）

### 8.5 命令名限制

- 只允许 [a-zA-Z0-9_]

- 不能带 / 前缀

### 8.6 内置 /new 命令（新建对话）
蓝信个人机器人对话窗口右上角提供"新建对话"按钮。用户点击后，蓝信客户端会自动向机器人发送 /new 斜杠命令（以普通文本消息形式送达）。接入方必须支持此命令。
处理要求：

- 收到 content 为 /new 的文本消息后，接入方应重置该用户/会话的 Session，不再继承之前对话的上下文记忆

- 向用户回复确认信息，明确告知：从此开始的对话是新的 Session，可能已无上一 Session 的记忆

重要说明：

- 蓝信界面上的历史消息不会消失——用户仍可向上滚动查看之前的对话记录，但这只是客户端展示层的行为

- 接入方的 Session 上下文已切换，不应再用历史消息作为上下文继续推理

- /new 是蓝信客户端内置命令，与通过「8.1 注册命令」注册的自定义命令不同，接入方直接在消息处理逻辑中拦截 /new 文本即可，无需也无法通过 API 注册

----

## 9. 群组管理 API

### 9.1 查询机器人所在群列表
GET {apiGatewayUrl}/v2/groups/fetch?app_token={appToken}&page_offset=0&page_size=100

成功响应：
{
  "errCode": 0,
  "data": {
    "totalGroupIds": 5,
    "groupIds": ["group-id-1", "group-id-2", ...]
  }
}

注意：企业部署环境中此接口可能返回 errCode=10005（权限不足），此时需要用户手动提供群 ID。

### 9.2 获取群信息
GET {apiGatewayUrl}/v2/groups/{groupId}/info/fetch?app_token={appToken}

成功响应：
{
  "errCode": 0,
  "data": {
    "name": "群名称",
    "avatarId": "头像ID",
    "avatarUrl": "https://...",
    "description": "群描述",
    "owner": { "staffId": "xxx", "name": "群主名" },
    "creator": { "staffId": "xxx", "name": "创建者名" },
    "state": 0,
    "isPublic": false,
    "maxMembers": 2000,
    "totalMembers": 150,
    "remindAll": false,
    "sendMsgStatus": true
  }
}

### 9.3 获取群成员列表
GET {apiGatewayUrl}/v2/groups/{groupId}/members/fetch?app_token={appToken}&page_offset=0&page_size=100

成功响应：
{
  "errCode": 0,
  "data": {
    "totalMembers": 150,
    "members": [
      {
        "status": 1,
        "staffId": "staff-id",
        "botId": "",
        "name": "成员名称",
        "avatarUrl": "https://...",
        "avatarId": "avatar-id",
        "orgName": "部门名称",
        "role": 0
      },
      {
        "status": 1,
        "staffId": "",
        "botId": "bot-id",
        "name": "机器人名称",
        "avatarUrl": "",
        "avatarId": "",
        "orgName": "组织名称",
        "role": 0
      }
    ]
  }
}

成员字段说明：
字段说明
 | staffId | 自然人群成员的 staffId，机器人成员为空字符串
 | botId | 机器人群成员的 botId，自然人成员为空字符串
 | name | 成员名称
 | avatarUrl | 头像 URL
 | avatarId | 头像 open ID
 | orgName | 组织名称
 | role | 角色类型
 | status | 成员状态

成员角色（role）：
值角色
 | 0 | 成员
 | 1 | 副群主
 | 2 | 群主

成员状态（status）：
值状态
 | 0 | 未激活
 | 1 | 正常
 | 2 | 冻结
 | 3 | 已删除

### 9.4 检查成员是否在群内
GET {apiGatewayUrl}/v2/groups/{groupId}/members/is_in_group?app_token={appToken}&staff_id={staffId}

- staff_id 可选，省略时检查机器人自身是否在群中

成功响应：
{
  "errCode": 0,
  "data": {
    "isInGroup": true
  }
}

----

## 10. 完整对接流程
以下为一个典型的 Agent 服务对接蓝信个人机器人的完整步骤：

### Step 1：准备凭证
从蓝信桌面端获取 App ID 和 App Secret（参见「1. 获取凭证」）。

### Step 2：获取 App Token
curl "https://open.e.lanxin.cn/open/apigw/v1/apptoken/create?grant_type=client_credential&appid=YOUR_APP_ID&secret=YOUR_APP_SECRET"

缓存返回的 appToken，在过期前（expiresIn - 300s）刷新。

### Step 3：连接 WebSocket（推荐）
# 1. 获取 WS 地址
curl -X POST "https://open.e.lanxin.cn/open/apigw/v1/ws/endpoint/create" \
  -H "Content-Type: application/json" \
  -d '{"appId":"YOUR_APP_ID","secret":"YOUR_APP_SECRET"}'

# 2. 连接返回的 wss:// 地址
# 使用你的 WebSocket 客户端连接

### Step 4：接收并处理消息
# 伪代码示例
import json
import websocket

def on_message(ws, raw_message):
    msg = json.loads(raw_message)

    # 处理审批卡片按钮回调
    if msg.get("type") == "approve_card_callback":
        handle_callback(msg["data"])
        return

    # 处理普通消息事件
    for event in msg.get("events", []):
        data = event["data"]
        chat_type = data["chatType"]  # "p2p" or "group"
        msg_type = data["msgType"]    # "text", "formatText", "image", etc.
        sender = data["from"]
        content = extract_content(msg_type, data["msgData"])

        # 决定是否响应：
        # - 私聊：检查是否为创建者
        # - 群聊：检查是否 @了机器人
        if should_respond(event):
            reply = call_your_agent(content)
            send_message(sender, chat_type, reply)

### Step 5：发送回复
def send_message(target_id, chat_type, text, ref_msg_id=None):
    """发送 Markdown 格式消息"""
    if chat_type == "group":
        url = f"{API_GATEWAY}/v1/messages/group/create?app_token={token}"
        body = {
            "groupId": target_id,
            "msgType": "formatText",
            "msgData": {"formatText": {"formatType": 1, "text": text}}
        }
    else:
        url = f"{API_GATEWAY}/v1/bot/messages/create?app_token={token}"
        body = {
            "userIdList": [target_id],
            "msgType": "formatText",
            "msgData": {"formatText": {"formatType": 1, "text": text}}
        }

    if ref_msg_id:
        body["refMsgId"] = ref_msg_id

    response = requests.post(url, json=body)
    return response.json()

### Step 6：发送媒体文件
def send_file(target_id, chat_type, file_path, caption=""):
    """上传并发送文件"""
    # 1. 上传
    with open(file_path, "rb") as f:
        resp = requests.post(
            f"{API_GATEWAY}/v1/app/medias/create?type=file&app_token={token}",
            files={"media": (os.path.basename(file_path), f)}
        )
    media_id = resp.json()["data"]["mediaId"]

    # 2. 发送
    if chat_type == "group":
        url = f"{API_GATEWAY}/v1/messages/group/create?app_token={token}"
        body = {
            "groupId": target_id,
            "msgType": "text",
            "msgData": {"text": {"content": caption, "mediaType": 3, "mediaIds": [media_id]}}
        }
    else:
        url = f"{API_GATEWAY}/v1/bot/messages/create?app_token={token}"
        body = {
            "userIdList": [target_id],
            "msgType": "text",
            "msgData": {"text": {"content": caption, "mediaType": 3, "mediaIds": [media_id]}}
        }

    return requests.post(url, json=body).json()

### Step 7：注册斜杠命令（可选）
def register_commands():
    commands = [
        {"command": "help", "description": "显示帮助信息", "i18nDescription": {"en": "Show help"}},
        {"command": "status", "description": "查看状态", "i18nDescription": {"en": "Check status"}},
    ]
    for scope in [5, 6]:  # 群聊 + 私聊
        requests.post(
            f"{API_GATEWAY}/v1/bot/commands/create?app_token={token}",
            json={"scopeType": scope, "commands": commands}
        )

### 完整架构示意
┌──────────────────────────────────────────────────┐
│                   蓝信客户端                       │
│  (用户发送消息 / 点击审批按钮)                      │
└──────────────────┬───────────────────────────────┘
                   │
    ┌──────────────▼──────────────┐
    │     蓝信 API 网关            │
    │  open.e.lanxin.cn/open/apigw │
    └──────┬──────────────┬───────┘
           │              │
    ┌──────▼──────┐  ┌───▼──────────┐
    │  WebSocket   │  │  HTTP API    │
    │  (接收消息)   │  │  (发消息/查询) │
    └──────┬──────┘  └───▲──────────┘
           │              │
    ┌──────▼──────────────┴───────┐
    │       你的 Agent 服务         │
    │  ┌─────────────────────────┐ │
    │  │  消息路由 & 策略引擎      │ │
    │  │  • DM 白名单检查          │ │
    │  │  • 群聊 @提及 检查        │ │
    │  │  • 命令权限检查           │ │
    │  └───────────┬─────────────┘ │
    │  ┌───────────▼─────────────┐ │
    │  │  AI Agent 核心           │ │
    │  │  • LLM 推理              │ │
    │  │  • 工具调用              │ │
    │  │  • 审批流程              │ │
    │  └───────────┬─────────────┘ │
    │  ┌───────────▼─────────────┐ │
    │  │  消息发送                │ │
    │  │  • 文本/Markdown          │ │
    │  │  • 媒体文件               │ │
    │  │  • 卡片                   │ │
    │  └─────────────────────────┘ │
    └──────────────────────────────┘

----

## 11. 常见问题与陷阱

### 11.1 WebSocket 僵尸连接
机器休眠/网络切换后，WebSocket 的 TCP 连接可能处于半开状态（readyState 仍为 OPEN 但实际已断开）。建议：

- 定期（30s）发送 ping，期望 10s 内收到 pong

- 超过 70s 无 pong 则主动断开重连

### 11.2 App Token 过期
token 默认有效期 7200 秒（2 小时），建议提前 300 秒刷新。过期后所有 API 返回错误，需重新获取。

### 11.3 appCard CSS 单位陷阱

- font-size 必须用 pt（12pt–36pt），不能用 px

- text-indent 必须带单位（如 0em），裸写 0 会导致 API 静默失败

### 11.4 个人机器人 DM 限制
个人机器人只能接收创建者的私聊消息。其他用户的私聊消息不会被推送到机器人。

### 11.5 群聊 /v2 API 权限
企业部署中 /v2/groups/fetch 等 v2 接口可能返回 errCode=10005（API 服务无权限）。需管理员开通权限。

### 11.6 视频消息要求封面图
发送视频消息时，需要同时上传封面图，mediaIds: [videoMediaId, coverImageMediaId]，且需提供 width、height、duration 参数。

### 11.7 ID 大小写
所有蓝信 ID（群 ID、用户 staff ID 等）大小写敏感，需严格保留原始格式。

### 11.8 命令名限制
注册斜杠命令时名称只能包含 [a-zA-Z0-9_]，不支持 / 前缀。

### 11.9 后退策略
发送 formatText 失败时（如 reminder 参数不兼容），可退回使用 text 类型重试。

### 11.10 入站 approveCard 格式与出站不同
出站发送 approveCard 使用嵌套的 head/body/buttons 结构，但入站收到的 approveCard 是扁平格式 { title, formatType, text, sequence }。接入方需兼容两种格式。

### 11.11 box 消息 reminder 可为 null
聊天记录转发（box）消息的 reminder 字段为 null，不会包含 isAtMe/isAtAll。如果群开启了 requireMention 策略，box 消息将被拦截。

### 11.12  时间戳格式
蓝信事件中的时间戳为微秒级（如 1783594786016554），不是毫秒。解析时需除以 1000 转换为毫秒，或直接除以 1000000 转换为秒。

----

## 附录：完整接口速查表
方法路径用途
 | GET | /v1/apptoken/create | 获取 App Token
 | POST | /v1/ws/endpoint/create | 获取 WebSocket 地址
 | POST | /v1/bot/messages/create | 发送私聊消息
 | POST | /v1/messages/group/create | 发送群聊消息
 | POST | /v1/app/medias/create | 上传媒体文件
 | GET | /v1/medias/{mediaId}/fetch | 下载媒体文件
 | POST | /v1/messages/revoke | 撤回消息
 | POST | /v1/messages/dynamic/update | 更新卡片状态
 | POST | /v1/bot/commands/create | 注册斜杠命令
 | POST | /v1/bot/commands/fetch | 查询斜杠命令
 | POST | /v1/bot/commands/delete | 删除斜杠命令
 | GET | /v2/groups/fetch | 查询群列表
 | GET | /v2/groups/{groupId}/info/fetch | 查询群信息
 | GET | /v2/groups/{groupId}/members/fetch | 查询群成员
 | GET | /v2/groups/{groupId}/members/is_in_group | 检查成员

----
本文档最后更新：2026-07-20

