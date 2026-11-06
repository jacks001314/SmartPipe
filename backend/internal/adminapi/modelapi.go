package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/store"
)

// registerModels 挂载模型供应商、模型目录和默认模型接口。
func (a *API) registerModels(party iris.Party) {
	party.Get("/model-providers", a.listModelProviders)
	party.Post("/model-providers", a.createModelProvider)
	party.Get("/model-providers/{id}", a.getModelProvider)
	party.Patch("/model-providers/{id}", a.updateModelProvider)
	party.Post("/model-providers/{id}/enabled", a.setModelProviderEnabled)
	party.Post("/model-providers/{id}/test", a.testModelProvider)
	party.Delete("/model-providers/{id}", a.deleteModelProvider)
	party.Get("/model-providers/{id}/runtime", a.modelProviderRuntime)
	party.Post("/model-providers/{id}/models", a.addProviderModel)
	party.Post("/model-providers/{id}/models/refresh", a.refreshProviderModels)
	party.Post("/model-providers/{id}/models/{model}/enabled", a.setProviderModelEnabled)
	party.Delete("/model-providers/{id}/models/{model}", a.deleteProviderModel)
	party.Get("/model-default", a.getModelDefault)
	party.Put("/model-default", a.putModelDefault)
}

type modelProviderRequest struct {
	Name    string   `json:"name"`
	BaseURL string   `json:"base_url"`
	WireAPI string   `json:"wire_api"`
	APIKey  string   `json:"api_key"`
	Enabled *bool    `json:"enabled"`
	Models  []string `json:"models"`
}

func (a *API) listModelProviders(c iris.Context) {
	items, err := a.store.ListModelProviders(c.Request().Context())
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: items})
}

func (a *API) getModelProvider(c iris.Context) {
	p, err := a.store.GetModelProvider(c.Request().Context(), c.Params().Get("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	_ = c.JSON(envelope{Data: p})
}

func (a *API) createModelProvider(c iris.Context) {
	var in modelProviderRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	ctx := c.Request().Context()
	p, err := a.store.CreateModelProvider(ctx, &store.ModelProvider{
		Name: in.Name, BaseURL: in.BaseURL, WireAPI: in.WireAPI, Enabled: enabled,
	})
	if err != nil {
		a.badRequest(c, err.Error())
		return
	}
	if strings.TrimSpace(in.APIKey) != "" {
		cred, err := a.store.CreateCredential(ctx, nil, store.CredentialModelProvider,
			"model-provider/"+p.ID, in.APIKey, map[string]any{"base_url": p.BaseURL})
		if err != nil {
			a.fail(c, err)
			return
		}
		p.CredentialID = cred.ID
		if p, err = a.store.UpdateModelProvider(ctx, p); err != nil {
			a.fail(c, err)
			return
		}
	}
	for _, modelID := range in.Models {
		if strings.TrimSpace(modelID) == "" {
			continue
		}
		if _, err := a.store.AddProviderModel(ctx, p.ID, modelID, true); err != nil {
			a.fail(c, err)
			return
		}
	}
	p, _ = a.store.GetModelProvider(ctx, p.ID)
	a.audit(ctx, c, "model.provider.create", "model_provider", p.ID, nil, "ok",
		map[string]any{"name": p.Name, "base_url": p.BaseURL})
	c.StatusCode(http.StatusCreated)
	_ = c.JSON(envelope{Data: p})
}

func (a *API) updateModelProvider(c iris.Context) {
	id := c.Params().Get("id")
	ctx := c.Request().Context()
	p, err := a.store.GetModelProvider(ctx, id)
	if err != nil {
		a.fail(c, err)
		return
	}
	var in modelProviderRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	if in.Name != "" {
		p.Name = in.Name
	}
	if in.BaseURL != "" {
		p.BaseURL = in.BaseURL
	}
	if in.WireAPI != "" {
		p.WireAPI = in.WireAPI
	}
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}
	if strings.TrimSpace(in.APIKey) != "" {
		if p.CredentialID == "" {
			cred, err := a.store.CreateCredential(ctx, nil, store.CredentialModelProvider,
				"model-provider/"+p.ID, in.APIKey, map[string]any{"base_url": p.BaseURL})
			if err != nil {
				a.fail(c, err)
				return
			}
			p.CredentialID = cred.ID
		} else if _, err := a.store.RotateCredential(ctx, p.CredentialID, in.APIKey); err != nil {
			a.fail(c, err)
			return
		}
	}
	updated, err := a.store.UpdateModelProvider(ctx, p)
	if err != nil {
		a.badRequest(c, err.Error())
		return
	}
	a.audit(ctx, c, "model.provider.update", "model_provider", id, nil, "ok",
		map[string]any{"name": updated.Name, "key_rotated": strings.TrimSpace(in.APIKey) != ""})
	_ = c.JSON(envelope{Data: updated})
}

func (a *API) setModelProviderEnabled(c iris.Context) {
	id := c.Params().Get("id")
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	p, err := a.store.SetModelProviderEnabled(c.Request().Context(), id, in.Enabled)
	if err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "model.provider.enabled", "model_provider", id, nil, "ok",
		map[string]any{"enabled": in.Enabled})
	_ = c.JSON(envelope{Data: p})
}

func (a *API) deleteModelProvider(c iris.Context) {
	id := c.Params().Get("id")
	ctx := c.Request().Context()
	if err := a.store.DeleteModelProvider(ctx, id); err != nil {
		a.fail(c, err)
		return
	}
	a.audit(ctx, c, "model.provider.delete", "model_provider", id, nil, "ok", nil)
	c.StatusCode(http.StatusNoContent)
}

func (a *API) addProviderModel(c iris.Context) {
	id := c.Params().Get("id")
	var in struct {
		ModelID string `json:"model_id"`
		Enabled *bool  `json:"enabled"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	if _, err := a.store.GetModelProvider(c.Request().Context(), id); err != nil {
		a.fail(c, err)
		return
	}
	m, err := a.store.AddProviderModel(c.Request().Context(), id, in.ModelID, enabled)
	if err != nil {
		a.badRequest(c, err.Error())
		return
	}
	a.audit(c.Request().Context(), c, "model.catalog.add", "model_provider", id, nil, "ok",
		map[string]any{"model": m.ModelID, "enabled": m.Enabled})
	c.StatusCode(http.StatusCreated)
	_ = c.JSON(envelope{Data: m})
}

func (a *API) setProviderModelEnabled(c iris.Context) {
	id := c.Params().Get("id")
	modelID := c.Params().Get("model")
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	m, err := a.store.SetProviderModelEnabled(c.Request().Context(), id, modelID, in.Enabled)
	if err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "model.catalog.enabled", "model_provider", id, nil, "ok",
		map[string]any{"model": modelID, "enabled": in.Enabled})
	_ = c.JSON(envelope{Data: m})
}

func (a *API) deleteProviderModel(c iris.Context) {
	id := c.Params().Get("id")
	modelID := c.Params().Get("model")
	if err := a.store.DeleteProviderModel(c.Request().Context(), id, modelID); err != nil {
		a.fail(c, err)
		return
	}
	a.audit(c.Request().Context(), c, "model.catalog.delete", "model_provider", id, nil, "ok",
		map[string]any{"model": modelID})
	c.StatusCode(http.StatusNoContent)
}

// refreshProviderModels 通过 /models 拉取目录；已有模型保持启停状态，新模型默认停用。
func (a *API) refreshProviderModels(c iris.Context) {
	id := c.Params().Get("id")
	ctx := c.Request().Context()
	p, err := a.store.GetModelProvider(ctx, id)
	if err != nil {
		a.fail(c, err)
		return
	}
	apiKey, err := a.providerKey(ctx, p)
	if err != nil {
		a.badRequest(c, "读取供应商凭据失败，请先填写 API Key")
		return
	}
	models, err := listGatewayModels(ctx, p.BaseURL, apiKey)
	if err != nil {
		_ = a.store.RecordModelProviderTest(ctx, id, false, err.Error())
		a.badRequest(c, err.Error())
		return
	}
	for _, modelID := range models {
		if err := a.store.UpsertCatalogModel(ctx, id, modelID); err != nil {
			a.fail(c, err)
			return
		}
	}
	updated, _ := a.store.GetModelProvider(ctx, id)
	a.audit(ctx, c, "model.catalog.refresh", "model_provider", id, nil, "ok", map[string]any{"count": len(models)})
	_ = c.JSON(envelope{Data: iris.Map{"provider": updated, "count": len(models)}})
}

// testModelProvider 用一次最小对话验证供应商与模型可用。
func (a *API) testModelProvider(c iris.Context) {
	id := c.Params().Get("id")
	ctx := c.Request().Context()
	p, err := a.store.GetModelProvider(ctx, id)
	if err != nil {
		a.fail(c, err)
		return
	}
	var in struct {
		Model string `json:"model"`
	}
	_ = decodeBody(c, &in)
	model := strings.TrimSpace(in.Model)
	if model == "" {
		for _, m := range p.Models {
			if m.Enabled {
				model = m.ModelID
				break
			}
		}
	}
	if model == "" && len(p.Models) > 0 {
		model = p.Models[0].ModelID
	}
	if model == "" {
		a.badRequest(c, "先在模型列表里添加一个模型")
		return
	}
	apiKey, _ := a.providerKey(ctx, p)
	ok, message, latency := probeModelWire(ctx, p.BaseURL, apiKey, model, p.WireAPI)
	_ = a.store.RecordModelProviderTest(ctx, id, ok, message)
	result := "ok"
	if !ok {
		result = "failed"
	}
	a.audit(ctx, c, "model.provider.test", "model_provider", id, nil, result,
		map[string]any{"model": model, "ok": ok})
	updated, _ := a.store.GetModelProvider(ctx, id)
	_ = c.JSON(envelope{Data: iris.Map{
		"ok":         ok,
		"message":    message,
		"latency_ms": latency,
		"model":      model,
		"provider":   updated,
	}})
}

// modelProviderRuntime 供流水线进程读取连接信息；返回明文 key，仅限管理员令牌调用。
func (a *API) modelProviderRuntime(c iris.Context) {
	id := c.Params().Get("id")
	ctx := c.Request().Context()
	p, err := a.store.GetModelProvider(ctx, id)
	if err != nil {
		a.fail(c, err)
		return
	}
	if !p.Enabled {
		a.badRequest(c, "模型供应商已停用")
		return
	}
	apiKey, _ := a.providerKey(ctx, p)
	a.audit(ctx, c, "model.runtime.read", "model_provider", id, nil, "ok", nil)
	_ = c.JSON(envelope{Data: iris.Map{
		"id":       p.ID,
		"name":     p.Name,
		"base_url": p.BaseURL,
		"wire_api": p.WireAPI,
		"api_key":  apiKey,
	}})
}

func (a *API) getModelDefault(c iris.Context) {
	ctx := c.Request().Context()
	providerID, _ := a.store.GetSetting(ctx, "codex_default_provider")
	model, _ := a.store.GetSetting(ctx, "codex_default_model")
	effort, _ := a.store.GetSetting(ctx, "codex_default_effort")
	_ = c.JSON(envelope{Data: iris.Map{
		"provider_id": providerID, "model": model, "reasoning_effort": effort,
	}})
}

func (a *API) putModelDefault(c iris.Context) {
	var in struct {
		ProviderID string `json:"provider_id"`
		Model      string `json:"model"`
		Effort     string `json:"reasoning_effort"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	ctx := c.Request().Context()
	if strings.TrimSpace(in.ProviderID) != "" {
		p, err := a.store.GetModelProvider(ctx, in.ProviderID)
		if err != nil {
			a.fail(c, err)
			return
		}
		found := false
		for _, m := range p.Models {
			if m.ModelID == in.Model {
				found = true
				break
			}
		}
		if !found {
			a.badRequest(c, "模型不在该供应商的目录里")
			return
		}
	}
	for key, value := range map[string]string{
		"codex_default_provider": in.ProviderID,
		"codex_default_model":    in.Model,
		"codex_default_effort":   in.Effort,
	} {
		if err := a.store.SetSetting(ctx, key, value); err != nil {
			a.fail(c, err)
			return
		}
	}
	a.audit(ctx, c, "model.default.update", "model_default", in.ProviderID, nil, "ok",
		map[string]any{"model": in.Model, "effort": in.Effort})
	_ = c.JSON(envelope{Data: iris.Map{
		"provider_id": in.ProviderID, "model": in.Model, "reasoning_effort": in.Effort,
	}})
}

func (a *API) providerKey(ctx context.Context, p *store.ModelProvider) (string, error) {
	if p.CredentialID == "" {
		return "", fmt.Errorf("no credential")
	}
	_, key, _, err := a.store.GetSecret(ctx, p.CredentialID)
	return key, err
}

func listGatewayModels(ctx context.Context, base, apiKey string) ([]string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil, fmt.Errorf("缺少 Base URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("目录不可达: %s", scrubSecret(err.Error(), apiKey))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("目录返回 %d: %s", resp.StatusCode, truncateModelText(scrubSecret(string(body), apiKey), 160))
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("目录格式不是 OpenAI 兼容格式")
	}
	models := make([]string, 0, len(out.Data))
	for _, item := range out.Data {
		if strings.TrimSpace(item.ID) != "" {
			models = append(models, strings.TrimSpace(item.ID))
		}
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("目录里没有模型")
	}
	return models, nil
}

func probeModelWire(ctx context.Context, base, apiKey, model, wire string) (bool, string, int64) {
	if strings.EqualFold(strings.TrimSpace(wire), "responses") {
		return probeResponsesModel(ctx, base, apiKey, model)
	}
	return probeModel(ctx, base, apiKey, model)
}

func probeResponsesModel(ctx context.Context, base, apiKey, model string) (bool, string, int64) {
	start := time.Now()
	payload := map[string]any{"model": model, "input": "只回复 OK", "max_output_tokens": 16}
	raw, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/responses", bytes.NewReader(raw))
	if err != nil {
		return false, "构造请求失败", time.Since(start).Milliseconds()
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 45 * time.Second}).Do(req)
	if err != nil {
		return false, "模型网关不可达: " + scrubSecret(err.Error(), apiKey), time.Since(start).Milliseconds()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32768))
	latency := time.Since(start).Milliseconds()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, "模型响应正常（responses）", latency
	}
	return false, fmt.Sprintf("上游返回 %d: %s", resp.StatusCode, truncateModelText(scrubSecret(string(body), apiKey), 240)), latency
}

func probeModel(ctx context.Context, base, apiKey, model string) (bool, string, int64) {
	start := time.Now()
	payload := map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "只回复 OK"}},
		"max_tokens": 8,
		"stream":     false,
	}
	raw, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return false, "构造请求失败", time.Since(start).Milliseconds()
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 45 * time.Second}).Do(req)
	if err != nil {
		return false, "模型网关不可达: " + scrubSecret(err.Error(), apiKey), time.Since(start).Milliseconds()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32768))
	latency := time.Since(start).Milliseconds()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var out struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		_ = json.Unmarshal(body, &out)
		if len(out.Choices) == 0 {
			return false, "网关返回 2xx 但没有 choices", latency
		}
		reply := strings.TrimSpace(out.Choices[0].Message.Content)
		if len(reply) > 40 {
			reply = reply[:40] + "…"
		}
		if reply == "" {
			reply = "（空回复）"
		}
		return true, "模型响应正常：" + reply, latency
	}
	return false, fmt.Sprintf("上游返回 %d: %s", resp.StatusCode, truncateModelText(scrubSecret(string(body), apiKey), 240)), latency
}

func scrubSecret(msg, secret string) string {
	if secret == "" {
		return msg
	}
	return strings.ReplaceAll(msg, secret, "****")
}

func truncateModelText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
