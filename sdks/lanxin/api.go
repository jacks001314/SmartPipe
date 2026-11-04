package lanxin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
)

// ---- 接收通道 ----

type wsEndpointData struct {
	WSEndpoint string `json:"wsEndpoint"`
}

// WSEndpoint 获取 WebSocket 长连接地址。该接口不用 appToken，
// 直接以 appId+secret 为请求体（地址可能变化，每次重连前都应重新获取）。
func (c *Client) WSEndpoint(ctx context.Context) (string, error) {
	var d wsEndpointData
	err := c.callRaw(ctx, http.MethodPost, "/v1/ws/endpoint/create", nil,
		map[string]string{"appId": c.appID, "secret": c.secret}, &d)
	if err != nil {
		return "", err
	}
	return d.WSEndpoint, nil
}

// ---- 发送消息 ----

// SendP2PMessage 发送私聊消息（/v1/bot/messages/create）。
// 个人机器人仅能接收创建者的私聊，userIdList 通常只有一个元素。
func (c *Client) SendP2PMessage(ctx context.Context, userID string, msg *Message) (json.RawMessage, error) {
	body := struct {
		UserIDList []string       `json:"userIdList"`
		MsgType    MsgType        `json:"msgType"`
		MsgData    map[string]any `json:"msgData"`
		RefMsgID   string         `json:"refMsgId,omitempty"`
	}{UserIDList: []string{userID}, MsgType: msg.MsgType, MsgData: msg.MsgData, RefMsgID: msg.RefMsgID}

	var out json.RawMessage
	if err := c.call(ctx, http.MethodPost, "/v1/bot/messages/create", nil, body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SendGroupMessage 发送群聊消息（/v1/messages/group/create）。
func (c *Client) SendGroupMessage(ctx context.Context, groupID string, msg *Message) (json.RawMessage, error) {
	body := struct {
		GroupID  string         `json:"groupId"`
		MsgType  MsgType        `json:"msgType"`
		MsgData  map[string]any `json:"msgData"`
		RefMsgID string         `json:"refMsgId,omitempty"`
	}{GroupID: groupID, MsgType: msg.MsgType, MsgData: msg.MsgData, RefMsgID: msg.RefMsgID}

	var out json.RawMessage
	if err := c.call(ctx, http.MethodPost, "/v1/messages/group/create", nil, body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- 媒体 ----

// UploadMedia 上传媒体文件（multipart 字段名固定为 media）。
// type 为 image|video|audio|file；视频需额外传 width/height/duration（放入 extra）。
func (c *Client) UploadMedia(ctx context.Context, mt MediaType, filename string, r io.Reader, extra url.Values) (string, error) {
	q := url.Values{}
	q.Set("type", map[MediaType]string{MediaVideo: "video", MediaImage: "image", MediaFile: "file", MediaVoice: "audio"}[mt])
	for k, vs := range extra {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	tok, err := c.tok.get(ctx)
	if err != nil {
		return "", err
	}
	q.Set("app_token", tok)

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		defer pw.Close()
		part, err := mw.CreateFormFile("media", filename)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(part, r); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.CloseWithError(mw.Close())
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/v1/app/medias/create", q.Encode()), pr)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("lanxin: medias/create: %w", err)
	}
	defer resp.Body.Close()

	var env Envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return "", fmt.Errorf("lanxin: medias/create: decode (http %d): %w", resp.StatusCode, err)
	}
	if env.ErrCode != 0 {
		return "", &APIError{Code: env.ErrCode, Msg: env.ErrMsg, Path: "/v1/app/medias/create"}
	}
	var d struct {
		MediaID string `json:"mediaId"`
	}
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return "", err
	}
	return d.MediaID, nil
}

// DownloadMedia 下载媒体文件（返回原始字节）。
func (c *Client) DownloadMedia(ctx context.Context, mediaID string) ([]byte, error) {
	tok, err := c.tok.get(ctx)
	if err != nil {
		return nil, err
	}
	q := url.Values{"app_token": {tok}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/v1/medias/"+mediaID+"/fetch", q.Encode()), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lanxin: media fetch: http %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// ---- 消息管理 ----

// RevokeMessages 撤回消息。chatType 为 "bot"（私聊）或 "group"；
// 群聊撤回他人消息时需传 senderID。
func (c *Client) RevokeMessages(ctx context.Context, chatType string, messageIDs []string, senderID string) error {
	body := struct {
		ChatType   string   `json:"chatType"`
		MessageIDs []string `json:"messageIds"`
		SenderID   string   `json:"senderId,omitempty"`
	}{ChatType: chatType, MessageIDs: messageIDs, SenderID: senderID}
	return c.call(ctx, http.MethodPost, "/v1/messages/revoke", nil, body, nil)
}

// UpdateCard 更新卡片状态（approveCard / appCard），msgData 为官方文档
// 5.6 节对应的 { approveCardUpdateMsg | appCardUpdateMsg: {...} } 结构。
func (c *Client) UpdateCard(ctx context.Context, msgID string, msgType MsgType, msgData any) error {
	return c.call(ctx, http.MethodPost, "/v1/messages/dynamic/update", nil, map[string]any{
		"msgId":   msgID,
		"msgType": msgType,
		"msgData": msgData,
	}, nil)
}

// ---- 斜杠命令 ----

type Command struct {
	Command         string            `json:"command"`
	Description     string            `json:"description"`
	I18nDescription map[string]string `json:"i18nDescription,omitempty"`
}

// CreateCommands 注册斜杠命令（scopeType 见官方文档 8.2；名称仅 [a-zA-Z0-9_]）。
func (c *Client) CreateCommands(ctx context.Context, scopeType int, cmds []Command) error {
	body := struct {
		ScopeType int       `json:"scopeType"`
		Commands  []Command `json:"commands"`
	}{ScopeType: scopeType, Commands: cmds}
	return c.call(ctx, http.MethodPost, "/v1/bot/commands/create", nil, body, nil)
}

// FetchCommands 查询已注册命令。
func (c *Client) FetchCommands(ctx context.Context, scopeType int) (json.RawMessage, error) {
	var out json.RawMessage
	body := struct {
		ScopeType int `json:"scopeType"`
	}{ScopeType: scopeType}
	err := c.call(ctx, http.MethodPost, "/v1/bot/commands/fetch", nil, body, &out)
	return out, err
}

// DeleteCommands 删除当前 scopeType 下的全部命令。
func (c *Client) DeleteCommands(ctx context.Context, scopeType int) error {
	body := struct {
		ScopeType int `json:"scopeType"`
	}{ScopeType: scopeType}
	return c.call(ctx, http.MethodPost, "/v1/bot/commands/delete", nil, body, nil)
}

// ---- 群组管理（v2，企业部署可能需要管理员开通权限）----

type GroupRef struct {
	TotalGroupIDs int      `json:"totalGroupIds"`
	GroupIDs      []string `json:"groupIds"`
}

func (c *Client) ListGroups(ctx context.Context, offset, limit int) (*GroupRef, error) {
	q := url.Values{}
	q.Set("page_offset", fmt.Sprint(offset))
	q.Set("page_size", fmt.Sprint(limit))
	var d GroupRef
	if err := c.call(ctx, http.MethodGet, "/v2/groups/fetch", q, nil, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

type Staff struct {
	StaffID string `json:"staffId"`
	Name    string `json:"name"`
}

type GroupInfo struct {
	Name         string `json:"name"`
	AvatarURL    string `json:"avatarUrl"`
	Description  string `json:"description"`
	Owner        Staff  `json:"owner"`
	Creator      Staff  `json:"creator"`
	MaxMembers   int    `json:"maxMembers"`
	TotalMembers int    `json:"totalMembers"`
	SendMsgStatus bool  `json:"sendMsgStatus"`
}

func (c *Client) GroupInfo(ctx context.Context, groupID string) (*GroupInfo, error) {
	var d GroupInfo
	if err := c.call(ctx, http.MethodGet, "/v2/groups/"+groupID+"/info/fetch", nil, nil, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

type GroupMember struct {
	StaffID   string `json:"staffId"`
	BotID     string `json:"botId"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatarUrl"`
	OrgName   string `json:"orgName"`
	Role      int    `json:"role"`
	Status    int    `json:"status"`
}

func (c *Client) GroupMembers(ctx context.Context, groupID string, offset, limit int) ([]GroupMember, error) {
	q := url.Values{}
	q.Set("page_offset", fmt.Sprint(offset))
	q.Set("page_size", fmt.Sprint(limit))
	var d struct {
		TotalMembers int           `json:"totalMembers"`
		Members      []GroupMember `json:"members"`
	}
	if err := c.call(ctx, http.MethodGet, "/v2/groups/"+groupID+"/members/fetch", q, nil, &d); err != nil {
		return nil, err
	}
	return d.Members, nil
}

func (c *Client) IsInGroup(ctx context.Context, groupID, staffID string) (bool, error) {
	q := url.Values{}
	if staffID != "" {
		q.Set("staff_id", staffID)
	}
	var d struct {
		IsInGroup bool `json:"isInGroup"`
	}
	if err := c.call(ctx, http.MethodGet, "/v2/groups/"+groupID+"/members/is_in_group", q, nil, &d); err != nil {
		return false, err
	}
	return d.IsInGroup, nil
}

// callRaw 不附加 appToken 的原始调用（ws/endpoint/create 专用）。
func (c *Client) callRaw(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = io.NopCloser(bytesReader(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path, queryEncode(query)), rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("lanxin: %s: %w", path, err)
	}
	defer resp.Body.Close()
	var env Envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("lanxin: %s: decode (http %d): %w", path, resp.StatusCode, err)
	}
	if env.ErrCode != 0 {
		return &APIError{Code: env.ErrCode, Msg: env.ErrMsg, Path: path}
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return err
		}
	}
	return nil
}
