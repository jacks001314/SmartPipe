package adminapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"smartpipe/backend/internal/releasepublish"
	"smartpipe/backend/internal/releasev2"
)

// releaseVersionWarning 是预览阶段给出的非阻断提示。它不关闭「创建发布」，
// 因为同一版本配合完全一致的字节重传是幂等的；但操作者需要在按下按钮前
// 知道这次很可能在 release_import 处被拦下来。
type releaseVersionWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// publishedVersionProbe 记录「这个版本在哪些升级服务上已经存在」。
type publishedVersionProbe struct {
	Product  string   `json:"product"`
	Channel  string   `json:"channel"`
	FoundOn  []string `json:"found_on"`
	Checked  []string `json:"checked"`
	Failures []string `json:"failures,omitempty"`
}

type versionProbeCacheEntry struct {
	probe   *publishedVersionProbe
	expires time.Time
}

// versionProbeTTL 决定预检结果的缓存时长。预览会在每次选择项目时刷新，
// 而升级服务是远程调用；缓存一分钟既能让页面保持顺手，又不会把刚发布完
// 的状态藏太久。
const versionProbeTTL = time.Minute

func (a *API) cachedPublishedVersion(ctx context.Context, cfg *releasev2.ProjectReleaseConfig, version string) *publishedVersionProbe {
	version = strings.TrimSpace(version)
	if cfg == nil || version == "" {
		return nil
	}
	product, channel, _ := updateServicePublishing(cfg)
	key := strings.Join([]string{cfg.ProjectID, product, channel, version}, "|")
	a.versionProbeMu.Lock()
	entry, ok := a.versionProbes[key]
	a.versionProbeMu.Unlock()
	if ok && time.Now().Before(entry.expires) {
		return entry.probe
	}
	probe := a.probePublishedVersion(ctx, cfg, version)
	a.versionProbeMu.Lock()
	if a.versionProbes == nil {
		a.versionProbes = map[string]versionProbeCacheEntry{}
	}
	a.versionProbes[key] = versionProbeCacheEntry{probe: probe, expires: time.Now().Add(versionProbeTTL)}
	a.versionProbeMu.Unlock()
	return probe
}

// updateServicePublishing 从项目发版配置里取出产品组件名、渠道与凭据引用。
// 缺省值必须与 release-mcp 的 bundle layout 保持一致，否则预检会查错组件。
func updateServicePublishing(cfg *releasev2.ProjectReleaseConfig) (product, channel, credential string) {
	product = "product"
	if cfg == nil {
		return product, "", ""
	}
	if section := cfg.Publishing["update_service"]; section != nil {
		if v := strings.TrimSpace(asStringValue(section["product"])); v != "" {
			product = v
		}
		channel = strings.TrimSpace(asStringValue(section["channel"]))
		credential = strings.TrimSpace(asStringValue(section["credential"]))
	}
	if channel == "" && len(cfg.ChannelOrder) > 0 {
		channel = strings.TrimSpace(cfg.ChannelOrder[0])
	}
	return product, channel, credential
}

func asStringValue(v any) string {
	s, _ := v.(string)
	return s
}

// probePublishedVersion 在预览阶段逐个升级服务查「(组件, 渠道, 版本) 是否已存在」。
// 全部并行、单个服务 6 秒超时：预览是交互路径，任何一个服务卡住都不能拖住页面。
func (a *API) probePublishedVersion(ctx context.Context, cfg *releasev2.ProjectReleaseConfig, version string) *publishedVersionProbe {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil
	}
	product, channel, fallbackCredential := updateServicePublishing(cfg)
	services := a.updateServices(ctx)
	if len(services) == 0 {
		return nil
	}
	probe := &publishedVersionProbe{Product: product, Channel: channel}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, service := range services {
		if !service.Enabled {
			continue
		}
		wg.Add(1)
		go func(service releaseUpdateService) {
			defer wg.Done()
			credential := service.Credential
			if strings.TrimSpace(credential) == "" {
				credential = fallbackCredential
			}
			client := &releasepublish.UpdateClient{
				BaseURL: service.URL,
				Creds:   settingsCredentialProvider{store: a.store},
				HTTP:    &http.Client{Timeout: 6 * time.Second},
			}
			_, err := client.FindRelease(ctx, credential, product, channel, version)
			mu.Lock()
			defer mu.Unlock()
			probe.Checked = append(probe.Checked, service.Name)
			switch {
			case err == nil:
				probe.FoundOn = append(probe.FoundOn, service.Name)
			case errors.Is(err, releasepublish.ErrNotFoundRemote):
			default:
				probe.Failures = append(probe.Failures, service.Name+"："+err.Error())
			}
		}(service)
	}
	wg.Wait()
	sort.Strings(probe.Checked)
	sort.Strings(probe.FoundOn)
	return probe
}

// versionWarnings 把预检结果翻译成操作者能直接行动的一句话。
func versionWarnings(probe *publishedVersionProbe, version string) []releaseVersionWarning {
	if probe == nil || len(probe.FoundOn) == 0 {
		return nil
	}
	return []releaseVersionWarning{{
		Code: "version_already_published",
		Message: fmt.Sprintf(
			"版本 %s 已在升级服务 %s 上发布（组件 %s，渠道 %s）。构建不可复现，同版本重发的字节通常不同，release_import 会以 version_already_published 拦下来；要发新内容请先提升版本号再打 tag。",
			version, strings.Join(probe.FoundOn, "、"), probe.Product, probe.Channel,
		),
	}}
}
