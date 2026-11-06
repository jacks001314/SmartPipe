// Package conncheck 在服务端验证外部凭据是否可用。
//
// 所有请求都在服务端发起，浏览器只看到结果码和脱敏消息；
// 任何错误信息都不允许包含凭据明文。
package conncheck

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	lanxinsdk "smartpipe/sdks/lanxin"
)

// Result 是一次连接测试的结论。
type Result struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Latency int64  `json:"latency_ms"`
}

// Checker 执行实际的连通性检查。
type Checker struct {
	HTTP    *http.Client
	Timeout time.Duration
	// Now 用于测试注入时间。
	Now func() time.Time
}

// New 返回带默认超时的 Checker。
func New() *Checker {
	return &Checker{
		HTTP:    &http.Client{Timeout: 12 * time.Second},
		Timeout: 20 * time.Second,
		Now:     time.Now,
	}
}

func (c *Checker) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 12 * time.Second}
}

func metaString(meta map[string]any, key, fallback string) string {
	if meta == nil {
		return fallback
	}
	if v, ok := meta[key].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

// Dispatch 按凭据类型分派到具体检查。
func (c *Checker) Dispatch(ctx context.Context, kind, secret string, meta map[string]any) Result {
	start := c.Now()
	var res Result
	switch kind {
	case "github_pat":
		res = c.githubPAT(ctx, secret, meta)
	case "github_app":
		res = c.githubApp(ctx, secret, meta)
	case "github_webhook_secret":
		res = Result{OK: len(secret) >= 16, Message: "webhook secret 长度校验"}
		if !res.OK {
			res.Message = "webhook secret 至少需要 16 个字符"
		}
	case "lanxin":
		res = c.lanxin(ctx, secret, meta)
	case "syntropy":
		res = c.syntropy(ctx, secret, meta)
	case "git_ssh":
		res = c.gitRemote(ctx, secret, meta)
	case "signing_key":
		res = Result{OK: len(secret) > 0, Message: "签名密钥已登记（未做联网校验）"}
	case "model_provider":
		res = c.modelProvider(ctx, secret, meta)
	default:
		res = Result{OK: false, Message: "不支持的凭据类型: " + kind}
	}
	res.Latency = c.Now().Sub(start).Milliseconds()
	return res
}

func (c *Checker) githubPAT(ctx context.Context, token string, meta map[string]any) Result {
	base := metaString(meta, "api_base", "https://api.github.com")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/user", nil)
	if err != nil {
		return Result{Message: "构造请求失败"}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.client().Do(req)
	if err != nil {
		return Result{Message: "网络不可达: " + scrub(err.Error(), token)}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusOK {
		return Result{OK: true, Message: "PAT 有效，可读取账号信息"}
	}
	return Result{Message: fmt.Sprintf("GitHub 返回 %d（检查 token 权限）", resp.StatusCode)}
}

// githubApp 用应用私钥签一个 RS256 JWT，换取 installation token。
func (c *Checker) githubApp(ctx context.Context, privateKeyPEM string, meta map[string]any) Result {
	appID := metaString(meta, "app_id", "")
	installID := metaString(meta, "installation_id", "")
	if appID == "" || installID == "" {
		return Result{Message: "缺少 app_id 或 installation_id"}
	}
	jwt, err := signAppJWT(appID, privateKeyPEM, c.Now())
	if err != nil {
		return Result{Message: "私钥解析失败: " + scrub(err.Error(), privateKeyPEM)}
	}
	base := metaString(meta, "api_base", "https://api.github.com")
	url := fmt.Sprintf("%s/app/installations/%s/access_tokens", base, installID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return Result{Message: "构造请求失败"}
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return Result{Message: "网络不可达: " + scrub(err.Error(), privateKeyPEM)}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
		return Result{OK: true, Message: "GitHub App 安装可用，已取得 installation token"}
	}
	return Result{Message: fmt.Sprintf("GitHub App 校验失败 %d: %s", resp.StatusCode, truncate(string(body), 120))}
}

func signAppJWT(appID, privateKeyPEM string, now time.Time) (string, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return "", errors.New("no PEM block")
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = k
	} else if k2, err2 := x509.ParsePKCS8PrivateKey(block.Bytes); err2 == nil {
		rk, ok := k2.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("not an RSA key")
		}
		key = rk
	} else {
		return "", errors.New("unsupported key format")
	}

	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": appID,
	}
	h, _ := json.Marshal(header)
	p, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(h) + "." + enc.EncodeToString(p)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + enc.EncodeToString(sig), nil
}

// lanxinErrHint 把蓝信返回的业务错误码翻译成可操作的排查建议。
func lanxinErrHint(code int) string {
	switch code {
	case -5:
		return "网关认为请求方法或路径不对；请确认 API Base 是该环境的网关根地址（不要带 /v1）"
	case 40013:
		return "网关没有收到 appid 参数；请确认 App ID 已保存，且 API Base 指向蓝信网关根地址"
	case 40018:
		return "App Secret 错误；请到蓝信管理后台重新复制应用密钥，填到 App Secret 后保存"
	case 10005:
		return "应用缺少该接口权限，请联系蓝信管理员开通"
	}
	return "请核对 App ID、App Secret 与 API Base"
}

// lanxin 用 GET + query 调 /v1/apptoken/create（官方协议），
// 失败时把上游 errMsg 与排查建议一起返回给管理界面。
func (c *Checker) lanxin(ctx context.Context, appSecret string, meta map[string]any) Result {
	appID := metaString(meta, "app_id", "")
	base := lanxinsdk.NormalizeAPIBase(metaString(meta, "api_base", lanxinsdk.DefaultAPIBase))
	if appID == "" {
		return Result{Message: "缺少 App ID：请填写并保存应用配置"}
	}
	if strings.TrimSpace(appSecret) == "" {
		return Result{Message: "缺少 App Secret：请重新填写并保存"}
	}
	q := url.Values{}
	q.Set("grant_type", "client_credential")
	q.Set("appid", appID)
	q.Set("secret", appSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/apptoken/create?"+q.Encode(), nil)
	if err != nil {
		return Result{Message: "构造请求失败：请检查 API Base 是否为合法 URL"}
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return Result{Message: "无法连接蓝信网关 " + base + "：" + scrub(err.Error(), appSecret) + "；请检查网关地址、网络与代理"}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var out struct {
		ErrCode int    `json:"errCode"`
		ErrMsg  string `json:"errMsg"`
		Data    struct {
			AppToken  string `json:"appToken"`
			ExpiresIn int    `json:"expiresIn"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		snippet := strings.TrimSpace(scrub(string(body), appSecret))
		if snippet == "" {
			return Result{Message: fmt.Sprintf("蓝信网关返回 HTTP %d 且响应为空；请确认 API Base 指向蓝信 API 网关", resp.StatusCode)}
		}
		return Result{Message: fmt.Sprintf("蓝信网关返回 HTTP %d，响应不是 JSON：%s", resp.StatusCode, truncate(snippet, 160))}
	}
	if out.ErrCode != 0 {
		detail := "蓝信返回 errCode=" + strconv.Itoa(out.ErrCode)
		if msg := strings.TrimSpace(out.ErrMsg); msg != "" {
			detail += "（" + msg + "）"
		}
		if hint := lanxinErrHint(out.ErrCode); hint != "" {
			detail += "。" + hint
		}
		return Result{Message: scrub(detail, appSecret)}
	}
	if resp.StatusCode != http.StatusOK {
		return Result{Message: fmt.Sprintf("蓝信网关返回 HTTP %d；请确认 API Base 与网络可达", resp.StatusCode)}
	}
	if out.Data.AppToken == "" {
		return Result{Message: "蓝信返回成功但缺少 appToken；请确认 API Base 与 App ID 属于同一蓝信环境"}
	}
	ttl := out.Data.ExpiresIn
	if ttl <= 0 {
		ttl = 7200
	}
	return Result{OK: true, Message: fmt.Sprintf("蓝信 app token 获取成功（有效期 %d 秒）", ttl)}
}

func (c *Checker) syntropy(ctx context.Context, token string, meta map[string]any) Result {
	base := metaString(meta, "base_url", "http://127.0.0.1:8765")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/healthz", nil)
	if err != nil {
		return Result{Message: "构造请求失败"}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return Result{Message: "App Server 不可达: " + scrub(err.Error(), token)}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
	if resp.StatusCode == http.StatusOK {
		return Result{OK: true, Message: "App Server 健康检查通过"}
	}
	return Result{Message: fmt.Sprintf("App Server 返回 %d", resp.StatusCode)}
}

// modelProvider 用 /models 探测模型网关，确认 Base URL 与 Key 可用。
func (c *Checker) modelProvider(ctx context.Context, apiKey string, meta map[string]any) Result {
	base := strings.TrimRight(metaString(meta, "base_url", "https://api.openai.com/v1"), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return Result{Message: "构造请求失败"}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := c.client().Do(req)
	if err != nil {
		return Result{Message: "模型网关不可达: " + scrub(err.Error(), apiKey)}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
	if resp.StatusCode == http.StatusOK {
		var out struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		_ = json.Unmarshal(body, &out)
		if len(out.Data) > 0 {
			return Result{OK: true, Message: fmt.Sprintf("模型网关可用，返回 %d 个模型", len(out.Data))}
		}
		return Result{OK: true, Message: "模型网关可用"}
	}
	return Result{Message: fmt.Sprintf("模型网关返回 %d: %s", resp.StatusCode, truncate(scrub(string(body), apiKey), 160))}
}

func (c *Checker) gitRemote(ctx context.Context, key string, meta map[string]any) Result {
	repo := metaString(meta, "repo_url", "")
	if repo == "" {
		return Result{Message: "缺少 repo_url"}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--heads", repo)
	if key != "" {
		cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return Result{Message: "git 读取失败: " + truncate(scrub(string(out), key), 160)}
	}
	n := strings.Count(strings.TrimSpace(string(out)), "\n") + 1
	if strings.TrimSpace(string(out)) == "" {
		return Result{OK: true, Message: "仓库可读，但没有分支"}
	}
	return Result{OK: true, Message: fmt.Sprintf("仓库可读，发现 %d 个分支", n)}
}

func scrub(msg, secret string) string {
	if secret == "" {
		return msg
	}
	return strings.ReplaceAll(msg, secret, "****")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
