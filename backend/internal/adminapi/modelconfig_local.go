package adminapi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kataras/iris/v12"
)

// localCodexConfigCandidates 返回服务端可能存放 Codex 配置的位置：
// 显式环境变量优先，其次 CODEX_HOME，最后是运行用户的 ~/.codex/config.toml。
func localCodexConfigCandidates() []string {
	var out []string
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
			}
		}
		for _, existing := range out {
			if existing == path {
				return
			}
		}
		out = append(out, path)
	}
	if raw := strings.TrimSpace(os.Getenv("SMARTPIPE_CODEX_CONFIG")); raw != "" {
		for _, item := range strings.Split(raw, string(os.PathListSeparator)) {
			add(item)
		}
	}
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		add(filepath.Join(home, "config.toml"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".codex", "config.toml"))
	}
	return out
}

// readLocalCodexConfig 依次尝试候选路径，返回第一份可读的配置。
func readLocalCodexConfig() (string, string, []string, error) {
	candidates := localCodexConfigCandidates()
	for _, path := range candidates {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		if info.Size() > maxCodexConfigBytes {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := strings.TrimPrefix(string(raw), "\ufeff")
		if strings.TrimSpace(content) == "" {
			continue
		}
		return path, content, candidates, nil
	}
	return "", "", candidates, fmt.Errorf("服务端没有找到可读取的 config.toml")
}

type localModelConfigPreview struct {
	Found      bool                      `json:"found"`
	Source     string                    `json:"source"`
	Candidates []string                  `json:"candidates"`
	Preview    *modelConfigImportPreview `json:"preview,omitempty"`
	Error      string                    `json:"error,omitempty"`
}

func (a *API) previewLocalModelConfig(c iris.Context) {
	path, content, candidates, err := readLocalCodexConfig()
	if err != nil {
		_ = c.JSON(envelope{Data: localModelConfigPreview{Found: false, Candidates: candidates, Error: err.Error()}})
		return
	}
	parsed, err := parseCodexModelConfig(content)
	if err != nil {
		_ = c.JSON(envelope{Data: localModelConfigPreview{
			Found: false, Source: path, Candidates: candidates, Error: err.Error(),
		}})
		return
	}
	ctx := c.Request().Context()
	providers, err := a.store.ListModelProviders(ctx)
	if err != nil {
		a.fail(c, err)
		return
	}
	profiles, err := a.store.ListModelProfiles(ctx)
	if err != nil {
		a.fail(c, err)
		return
	}
	preview := buildModelConfigImportPreview(parsed, providers, profiles)
	_ = c.JSON(envelope{Data: localModelConfigPreview{
		Found: true, Source: path, Candidates: candidates, Preview: &preview,
	}})
}

func (a *API) importLocalModelConfig(c iris.Context) {
	var in modelConfigImportRequest
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	if len(in.ProfileKeys) == 0 && len(in.ProviderKeys) == 0 {
		a.badRequest(c, "请至少选择一个 Profile 或 Provider")
		return
	}
	path, content, _, err := readLocalCodexConfig()
	if err != nil {
		a.badRequest(c, err.Error())
		return
	}
	parsed, err := parseCodexModelConfig(content)
	if err != nil {
		a.badRequest(c, err.Error())
		return
	}
	ctx := c.Request().Context()
	result, err := a.applyCodexModelConfig(ctx, parsed, in.ProfileKeys, in.ProviderKeys)
	if err != nil {
		if _, ok := err.(*modelImportSelectionError); ok {
			a.badRequest(c, err.Error())
		} else {
			a.fail(c, err)
		}
		return
	}
	result.Source = path
	a.audit(ctx, c, "model.config.import", "model_provider", path, nil, "ok", map[string]any{
		"source": path, "imported_profiles": result.ImportedProfiles,
		"updated_profiles": result.UpdatedProfiles, "imported_providers": result.ImportedProviders,
		"updated_providers": result.UpdatedProviders,
	})
	_ = c.JSON(envelope{Data: result})
}
