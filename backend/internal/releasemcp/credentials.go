package releasemcp

import (
	"context"
	"fmt"
	"strings"

	"smartpipe/backend/internal/releasepublish"
	"smartpipe/backend/internal/store"
)

// StoreProvider 先按名字从数据库凭据里解析（界面上录入、加密保存），
// 找不到再回落到环境变量，便于从 env 平滑迁移到界面配置。
type StoreProvider struct {
	Store    *store.Store
	Fallback releasepublish.Provider
}

func (p StoreProvider) Resolve(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("%w: credential reference is required", releasepublish.ErrInvalid)
	}
	if p.Store != nil {
		if _, secret, _, err := p.Store.ResolveCredentialByName(ctx, ref); err == nil && strings.TrimSpace(secret) != "" {
			return secret, nil
		}
	}
	if p.Fallback != nil {
		return p.Fallback.Resolve(ctx, ref)
	}
	return "", fmt.Errorf("%w: credential %q is not configured", releasepublish.ErrCredentialMissing, ref)
}
