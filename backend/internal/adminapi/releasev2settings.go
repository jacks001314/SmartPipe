package adminapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/kataras/iris/v12"
)

// releaseSettings 是发版系统在运行时读取的外部服务参数。密钥本身不在这里，
// 只存凭据引用（credentials 表里的 name），避免明文进数据库。
// releaseUpdateService 是一台可发布的升级服务：同一份制品要灌到每一台，
// 每台有自己的管理员令牌，所以凭据也按服务分别保存。
type releaseUpdateService struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	Credential string `json:"credential"`
	Enabled    bool   `json:"enabled"`
}

type releaseSettings struct {
	// UpdateServices 是界面上维护的升级服务列表；UpdateURL/UpdateCredential
	// 只作为单服务时代的兼容字段，保存时镜像第一个启用的服务。
	UpdateServices   []releaseUpdateService `json:"update_services"`
	UpdateURL        string                 `json:"update_url"`
	UpdateCredential string                 `json:"update_credential"`
	GitHubAPIBase    string                 `json:"github_api_base"`
	GitHubCredential string                 `json:"github_credential"`
	S3Endpoint       string                 `json:"s3_endpoint"`
	S3Bucket         string                 `json:"s3_bucket"`
	S3Region         string                 `json:"s3_region"`
	S3Prefix         string                 `json:"s3_prefix"`
	S3Credential     string                 `json:"s3_credential"`
}

var releaseSettingFields = []struct {
	Key   string
	Value func(*releaseSettings) *string
}{
	{"release_update_url", func(s *releaseSettings) *string { return &s.UpdateURL }},
	{"release_update_credential", func(s *releaseSettings) *string { return &s.UpdateCredential }},
	{"release_github_api_base", func(s *releaseSettings) *string { return &s.GitHubAPIBase }},
	{"release_github_credential", func(s *releaseSettings) *string { return &s.GitHubCredential }},
	{"release_s3_endpoint", func(s *releaseSettings) *string { return &s.S3Endpoint }},
	{"release_s3_bucket", func(s *releaseSettings) *string { return &s.S3Bucket }},
	{"release_s3_region", func(s *releaseSettings) *string { return &s.S3Region }},
	{"release_s3_prefix", func(s *releaseSettings) *string { return &s.S3Prefix }},
	{"release_s3_credential", func(s *releaseSettings) *string { return &s.S3Credential }},
}

// getReleaseV2Settings 返回界面上可编辑的参数，密钥不在其中。
func (a *API) getReleaseV2Settings(c iris.Context) {
	ctx := c.Request().Context()
	var out releaseSettings
	for _, f := range releaseSettingFields {
		v, err := a.store.GetSetting(ctx, f.Key)
		if err != nil {
			a.fail(c, err)
			return
		}
		*f.Value(&out) = v
	}
	if raw, err := a.store.GetSetting(ctx, "release_update_services"); err == nil && strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &out.UpdateServices)
	}
	if len(out.UpdateServices) == 0 && strings.TrimSpace(out.UpdateURL) != "" {
		out.UpdateServices = []releaseUpdateService{{
			Name: "默认", URL: out.UpdateURL, Credential: out.UpdateCredential, Enabled: true,
		}}
	}
	_ = c.JSON(envelope{Data: out})
}

// putReleaseV2Settings 保存界面参数；空字符串表示清空，回落到服务端环境变量。
func (a *API) putReleaseV2Settings(c iris.Context) {
	var in releaseSettings
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	normalize := func(s string) string { return strings.TrimSpace(s) }
	in.UpdateURL = normalize(in.UpdateURL)
	in.UpdateCredential = normalize(in.UpdateCredential)
	in.GitHubAPIBase = normalize(in.GitHubAPIBase)
	in.GitHubCredential = normalize(in.GitHubCredential)
	in.S3Endpoint = normalize(in.S3Endpoint)
	in.S3Bucket = normalize(in.S3Bucket)
	in.S3Region = normalize(in.S3Region)
	in.S3Prefix = normalize(in.S3Prefix)
	in.S3Credential = normalize(in.S3Credential)

	for i := range in.UpdateServices {
		service := &in.UpdateServices[i]
		service.Name = strings.TrimSpace(service.Name)
		service.URL = strings.TrimRight(strings.TrimSpace(service.URL), "/")
		service.Credential = strings.TrimSpace(service.Credential)
	}
	cleaned := in.UpdateServices[:0]
	seen := map[string]bool{}
	for i, service := range in.UpdateServices {
		if service.URL == "" {
			continue
		}
		if service.Name == "" {
			service.Name = fmt.Sprintf("升级服务 %d", i+1)
		}
		if seen[service.Name] {
			a.badRequest(c, "升级服务名称重复: "+service.Name)
			return
		}
		seen[service.Name] = true
		cleaned = append(cleaned, service)
	}
	in.UpdateServices = cleaned
	// 兼容字段镜像第一个启用的服务，旧版 release-mcp / 脚本仍能读到一个地址。
	in.UpdateURL, in.UpdateCredential = "", ""
	for _, service := range in.UpdateServices {
		if service.Enabled {
			in.UpdateURL, in.UpdateCredential = service.URL, service.Credential
			break
		}
	}

	for _, service := range in.UpdateServices {
		u := service.URL
		if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			a.badRequest(c, "升级服务地址必须以 http:// 或 https:// 开头: "+u)
			return
		}
	}
	for _, u := range []string{in.GitHubAPIBase, in.S3Endpoint} {
		if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			a.badRequest(c, "地址必须以 http:// 或 https:// 开头: "+u)
			return
		}
	}

	encoded, err := json.Marshal(in.UpdateServices)
	if err != nil {
		a.badRequest(c, "升级服务列表无法序列化")
		return
	}
	ctx := c.Request().Context()
	if err := a.store.SetSetting(ctx, "release_update_services", string(encoded)); err != nil {
		a.fail(c, err)
		return
	}
	for _, f := range releaseSettingFields {
		if err := a.store.SetSetting(ctx, f.Key, *f.Value(&in)); err != nil {
			a.fail(c, err)
			return
		}
	}
	a.audit(ctx, c, "release.v2.settings", "release_settings", "default", nil, "ok", map[string]any{
		"update_url": in.UpdateURL, "github_api_base": in.GitHubAPIBase,
		"s3_endpoint": in.S3Endpoint, "s3_bucket": in.S3Bucket,
	})
	c.StatusCode(http.StatusOK)
	_ = c.JSON(envelope{Data: in})
}
