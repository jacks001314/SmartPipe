package releasesvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"smartpipe/backend/internal/releasev2"
)

// settingsReader / secretReader 由 *store.Store 满足；用接口断言是为了让
// HTTPHandler 的单元测试仍能只实现 ControlStore。
type settingsReader interface {
	GetSetting(ctx context.Context, key string) (string, error)
}

type secretReader interface {
	ResolveCredentialByName(ctx context.Context, name string) (kind, plaintext string, metadata map[string]any, err error)
}

type cachedStore struct {
	sig   string
	store releasev2.ObjectStore
}

var dynamicStoreCache sync.Map // *HTTPHandler -> cachedStore

// objectsFor 优先使用界面里保存的对象存储参数（release_s3_*），没配就回落到
// 启动时的存储。参数变化时重建一次 S3 客户端并缓存。
func (h *HTTPHandler) objectsFor(ctx context.Context) releasev2.ObjectStore {
	sr, ok := h.Store.(settingsReader)
	if !ok {
		return h.Objects
	}
	endpoint, _ := sr.GetSetting(ctx, "release_s3_endpoint")
	bucket, _ := sr.GetSetting(ctx, "release_s3_bucket")
	endpoint = strings.TrimSpace(endpoint)
	bucket = strings.TrimSpace(bucket)
	if endpoint == "" || bucket == "" {
		return h.Objects
	}
	region, _ := sr.GetSetting(ctx, "release_s3_region")
	prefix, _ := sr.GetSetting(ctx, "release_s3_prefix")
	credRef, _ := sr.GetSetting(ctx, "release_s3_credential")

	var accessKey, secretKey string
	if strings.TrimSpace(credRef) != "" {
		if pr, ok := h.Store.(secretReader); ok {
			if _, plain, meta, err := pr.ResolveCredentialByName(ctx, strings.TrimSpace(credRef)); err == nil {
				secretKey = plain
				if v, ok := meta["access_key"].(string); ok {
					accessKey = v
				}
			}
		}
	}

	sum := sha256.Sum256([]byte(strings.Join([]string{endpoint, region, bucket, prefix, accessKey, secretKey}, "\x00")))
	sig := hex.EncodeToString(sum[:])
	if v, ok := dynamicStoreCache.Load(h); ok {
		if cached, ok := v.(cachedStore); ok && cached.sig == sig {
			return cached.store
		}
	}
	store, err := releasev2.NewS3Store(endpoint, region, bucket, accessKey, secretKey, "", prefix)
	if err != nil {
		// 配置不完整时保留启动时的存储，避免一次错误配置让上传直接 500。
		return h.Objects
	}
	dynamicStoreCache.Store(h, cachedStore{sig: sig, store: store})
	return store
}
