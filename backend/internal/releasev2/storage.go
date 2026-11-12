package releasev2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var ErrObjectMissing = errors.New("releasev2: object not found")

var objectKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,511}$`)

type StoredObject struct {
	Key         string `json:"key"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type,omitempty"`
	// ModTime 由 List 填充，制品清理用它判断宽限期。
	ModTime time.Time `json:"-"`
}

// ObjectStore is the streaming artifact storage contract. Implementations must
// avoid buffering whole artifacts in memory.
type ObjectStore interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (StoredObject, error)
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Stat(ctx context.Context, key string) (StoredObject, error)
}

// ObjectLister 枚举前缀下的对象，供制品清理使用。
type ObjectLister interface {
	List(ctx context.Context, prefix string) ([]StoredObject, error)
}

// ObjectDeleter 删除单个对象；不存在视为成功。
type ObjectDeleter interface {
	Delete(ctx context.Context, key string) error
}

// Presigner issues short lived download URLs for the upgrade service and
// GitHub adapters.
type Presigner interface {
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// PutPresigner issues pre-signed upload URLs. Object stores without upload
// signing (local disk) fall back to the relay endpoint on release-service.
type PutPresigner interface {
	PresignPut(ctx context.Context, key string, size int64, contentType string, ttl time.Duration) (string, map[string]string, error)
}

// LocalStore is a filesystem backed ObjectStore used for development, tests
// and single node deployments. The S3 adapter implements the same interface.
type LocalStore struct {
	Root      string
	PublicURL string
}

func NewLocalStore(root string) (*LocalStore, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("releasev2: resolve storage root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("releasev2: create storage root: %w", err)
	}
	return &LocalStore{Root: abs}, nil
}

func (l *LocalStore) path(key string) (string, error) {
	key = strings.TrimSpace(key)
	if strings.HasPrefix(key, "/") || !objectKeyPattern.MatchString(key) || strings.Contains(key, "..") {
		return "", fmt.Errorf("%w: unsafe object key %q", ErrInvalid, key)
	}
	return filepath.Join(l.Root, filepath.FromSlash(key)), nil
}

func (l *LocalStore) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (StoredObject, error) {
	dst, err := l.path(key)
	if err != nil {
		return StoredObject{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return StoredObject{}, fmt.Errorf("releasev2: create object dir: %w", err)
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return StoredObject{}, fmt.Errorf("releasev2: create object: %w", err)
	}
	h := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(f, h), &contextReader{ctx: ctx, r: r})
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return StoredObject{}, fmt.Errorf("releasev2: write object: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return StoredObject{}, fmt.Errorf("releasev2: close object: %w", closeErr)
	}
	if size > 0 && written != size {
		_ = os.Remove(tmp)
		return StoredObject{}, fmt.Errorf("%w: object size mismatch: wrote %d want %d", ErrInvalid, written, size)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return StoredObject{}, fmt.Errorf("releasev2: publish object: %w", err)
	}
	return StoredObject{Key: key, Size: written, SHA256: "sha256:" + hex.EncodeToString(h.Sum(nil)), ContentType: contentType}, nil
}

func (l *LocalStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	dst, err := l.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(dst)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrObjectMissing, key)
	}
	return f, err
}

func (l *LocalStore) Stat(ctx context.Context, key string) (StoredObject, error) {
	f, err := l.Open(ctx, key)
	if err != nil {
		return StoredObject{}, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, &contextReader{ctx: ctx, r: f})
	if err != nil {
		return StoredObject{}, fmt.Errorf("releasev2: hash object: %w", err)
	}
	return StoredObject{Key: key, Size: size, SHA256: "sha256:" + hex.EncodeToString(h.Sum(nil))}, nil
}

// List 返回前缀下的所有对象（含子目录），Key 为逻辑路径。
func (l *LocalStore) List(_ context.Context, prefix string) ([]StoredObject, error) {
	base, err := l.path(strings.TrimSuffix(prefix, "/"))
	if err != nil {
		// 允许只用一段前缀（如 releases），path 校验对纯目录同样适用。
		return nil, err
	}
	out := []StoredObject{}
	info, err := os.Stat(base)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []StoredObject{{Key: strings.TrimSuffix(prefix, "/"), Size: info.Size(), ModTime: info.ModTime()}}, nil
	}
	err = filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(l.Root, path)
		if relErr != nil {
			return relErr
		}
		item, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}
		out = append(out, StoredObject{Key: filepath.ToSlash(rel), Size: item.Size(), ModTime: item.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Delete 删除对象；对象不存在按成功处理，便于清理重复执行。
func (l *LocalStore) Delete(_ context.Context, key string) error {
	dst, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// PresignGet serves objects directly when a public base URL is configured.
func (l *LocalStore) PresignGet(_ context.Context, key string, _ time.Duration) (string, error) {
	if strings.TrimSpace(l.PublicURL) == "" {
		return "", fmt.Errorf("%w: local storage has no public base URL", ErrInvalid)
	}
	if _, err := l.path(key); err != nil {
		return "", err
	}
	return strings.TrimRight(l.PublicURL, "/") + "/" + key, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
