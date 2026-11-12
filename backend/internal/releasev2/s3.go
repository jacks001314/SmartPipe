package releasev2

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// S3Store is a dependency free S3/MinIO adapter using SigV4 with path style
// addressing, so the same code works against MinIO, Ceph and AWS S3.
type S3Store struct {
	Endpoint     string
	Region       string
	Bucket       string
	AccessKey    string
	SecretKey    string
	SessionToken string
	Prefix       string
	HTTP         *http.Client
}

func NewS3Store(endpoint, region, bucket, accessKey, secretKey, sessionToken, prefix string) (*S3Store, error) {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" || strings.TrimSpace(bucket) == "" {
		return nil, fmt.Errorf("%w: s3 endpoint and bucket are required", ErrInvalid)
	}
	if strings.TrimSpace(accessKey) == "" || strings.TrimSpace(secretKey) == "" {
		return nil, fmt.Errorf("%w: s3 access key and secret key are required", ErrInvalid)
	}
	if region == "" {
		region = "us-east-1"
	}
	return &S3Store{
		Endpoint: endpoint, Region: region, Bucket: strings.TrimSpace(bucket),
		AccessKey: strings.TrimSpace(accessKey), SecretKey: strings.TrimSpace(secretKey),
		SessionToken: strings.TrimSpace(sessionToken), Prefix: strings.Trim(strings.TrimSpace(prefix), "/"),
	}, nil
}

func (s *S3Store) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 2 * time.Hour}
}

func (s *S3Store) objectKey(key string) string {
	key = strings.TrimPrefix(strings.TrimSpace(key), "/")
	if s.Prefix != "" {
		return s.Prefix + "/" + key
	}
	return key
}

func (s *S3Store) objectURL(key string, query url.Values) (*url.URL, error) {
	base, err := url.Parse(s.Endpoint)
	if err != nil {
		return nil, err
	}
	segments := []string{s.Bucket}
	for _, part := range strings.Split(strings.TrimPrefix(key, "/"), "/") {
		segments = append(segments, part)
	}
	base.Path = "/" + strings.Join(segments, "/")
	base.RawQuery = query.Encode()
	return base, nil
}

// bucketURL 构造桶级请求（List 用），path 为主机下的桶路径。
func (s *S3Store) bucketURL(query url.Values) (*url.URL, error) {
	base, err := url.Parse(s.Endpoint)
	if err != nil {
		return nil, err
	}
	base.Path = "/" + s.Bucket
	if base.Path == "/" {
		base.Path = "/"
	}
	base.RawQuery = query.Encode()
	return base, nil
}

func (s *S3Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (StoredObject, error) {
	fullKey := s.objectKey(key)
	tmp, err := os.CreateTemp("", "s3-put-*")
	if err != nil {
		return StoredObject{}, err
	}
	defer func() { _ = os.Remove(tmp.Name()); _ = tmp.Close() }()
	h := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, h), &contextReader{ctx: ctx, r: r})
	if err != nil {
		return StoredObject{}, fmt.Errorf("releasev2: stage object: %w", err)
	}
	if size > 0 && written != size {
		return StoredObject{}, fmt.Errorf("%w: object size mismatch: wrote %d want %d", ErrInvalid, written, size)
	}
	payloadHash := hex.EncodeToString(h.Sum(nil))
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return StoredObject{}, err
	}
	u, err := s.objectURL(fullKey, url.Values{})
	if err != nil {
		return StoredObject{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), tmp)
	if err != nil {
		return StoredObject{}, err
	}
	req.ContentLength = written
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("x-amz-meta-sha256", "sha256:"+payloadHash)
	s.sign(req, payloadHash, time.Now().UTC())
	resp, err := s.client().Do(req)
	if err != nil {
		return StoredObject{}, fmt.Errorf("releasev2: s3 put: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return StoredObject{}, fmt.Errorf("releasev2: s3 put %s: %s %s", fullKey, resp.Status, strings.TrimSpace(string(body)))
	}
	return StoredObject{Key: key, Size: written, SHA256: "sha256:" + payloadHash, ContentType: contentType}, nil
}

func (s *S3Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	fullKey := s.objectKey(key)
	u, err := s.objectURL(fullKey, url.Values{})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-amz-content-sha256", emptyPayloadHash)
	s.sign(req, emptyPayloadHash, time.Now().UTC())
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("releasev2: s3 get: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, fmt.Errorf("%w: %s", ErrObjectMissing, key)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		return nil, fmt.Errorf("releasev2: s3 get %s: %s %s", fullKey, resp.Status, strings.TrimSpace(string(body)))
	}
	return resp.Body, nil
}

func (s *S3Store) Stat(ctx context.Context, key string) (StoredObject, error) {
	fullKey := s.objectKey(key)
	u, err := s.objectURL(fullKey, url.Values{})
	if err != nil {
		return StoredObject{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.String(), nil)
	if err != nil {
		return StoredObject{}, err
	}
	req.Header.Set("x-amz-content-sha256", emptyPayloadHash)
	s.sign(req, emptyPayloadHash, time.Now().UTC())
	resp, err := s.client().Do(req)
	if err != nil {
		return StoredObject{}, fmt.Errorf("releasev2: s3 stat: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return StoredObject{}, fmt.Errorf("%w: %s", ErrObjectMissing, key)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return StoredObject{}, fmt.Errorf("releasev2: s3 stat %s: %s", fullKey, resp.Status)
	}
	digest := resp.Header.Get("x-amz-meta-sha256")
	return StoredObject{Key: key, Size: resp.ContentLength, SHA256: digest, ContentType: resp.Header.Get("Content-Type")}, nil
}

// List 用 ListObjectsV2 枚举前缀下的对象；返回的 Key 去掉 store 的前缀。
func (s *S3Store) List(ctx context.Context, prefix string) ([]StoredObject, error) {
	fullPrefix := s.objectKey(strings.TrimPrefix(strings.TrimSpace(prefix), "/"))
	out := []StoredObject{}
	token := ""
	for page := 0; page < 100; page++ {
		query := url.Values{}
		query.Set("list-type", "2")
		query.Set("prefix", fullPrefix)
		query.Set("max-keys", "1000")
		if token != "" {
			query.Set("continuation-token", token)
		}
		u, err := s.bucketURL(query)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-amz-content-sha256", emptyPayloadHash)
		s.sign(req, emptyPayloadHash, time.Now().UTC())
		resp, err := s.client().Do(req)
		if err != nil {
			return nil, fmt.Errorf("releasev2: s3 list: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("releasev2: s3 list read: %w", readErr)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("releasev2: s3 list %s: %s %s", fullPrefix, resp.Status, strings.TrimSpace(string(body)))
		}
		var pageResult s3ListResult
		if err := xml.Unmarshal(body, &pageResult); err != nil {
			return nil, fmt.Errorf("releasev2: parse s3 list: %w", err)
		}
		for _, item := range pageResult.Contents {
			key := s.logicalKey(item.Key)
			mod, _ := time.Parse(time.RFC3339, item.LastModified)
			out = append(out, StoredObject{Key: key, Size: item.Size, ModTime: mod})
		}
		if !pageResult.IsTruncated || pageResult.NextContinuationToken == "" {
			break
		}
		token = pageResult.NextContinuationToken
	}
	return out, nil
}

// Delete 删除单个对象；404 视为已删除。
func (s *S3Store) Delete(ctx context.Context, key string) error {
	fullKey := s.objectKey(key)
	u, err := s.objectURL(fullKey, url.Values{})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-amz-content-sha256", emptyPayloadHash)
	s.sign(req, emptyPayloadHash, time.Now().UTC())
	resp, err := s.client().Do(req)
	if err != nil {
		return fmt.Errorf("releasev2: s3 delete: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return fmt.Errorf("releasev2: s3 delete %s: %s %s", fullKey, resp.Status, strings.TrimSpace(string(body)))
}

func (s *S3Store) logicalKey(full string) string {
	if s.Prefix != "" {
		return strings.TrimPrefix(full, s.Prefix+"/")
	}
	return full
}

type s3ListResult struct {
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
	Contents              []struct {
		Key          string `xml:"Key"`
		Size         int64  `xml:"Size"`
		LastModified string `xml:"LastModified"`
	} `xml:"Contents"`
}

// PresignGet returns a time limited download URL for the upgrade service.
func (s *S3Store) PresignGet(_ context.Context, key string, ttl time.Duration) (string, error) {
	return s.presign(http.MethodGet, s.objectKey(key), ttl)
}

// PresignPut returns a time limited upload URL for build executors.
func (s *S3Store) PresignPut(_ context.Context, key string, _ int64, _ string, ttl time.Duration) (string, map[string]string, error) {
	urlValue, err := s.presign(http.MethodPut, s.objectKey(key), ttl)
	if err != nil {
		return "", nil, err
	}
	return urlValue, map[string]string{}, nil
}

func (s *S3Store) presign(method, fullKey string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	scope := strings.Join([]string{now.Format("20060102"), s.Region, "s3", "aws4_request"}, "/")
	query := url.Values{}
	query.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	query.Set("X-Amz-Credential", s.AccessKey+"/"+scope)
	query.Set("X-Amz-Date", amzDate)
	query.Set("X-Amz-Expires", fmt.Sprintf("%d", int(ttl.Seconds())))
	query.Set("X-Amz-SignedHeaders", "host")
	if s.SessionToken != "" {
		query.Set("X-Amz-Security-Token", s.SessionToken)
	}
	u, err := s.objectURL(fullKey, query)
	if err != nil {
		return "", err
	}
	canonicalQuery := canonicalQueryString(u.Query())
	canonicalRequest := strings.Join([]string{
		method, u.EscapedPath(), canonicalQuery, "host:" + u.Host + "\n", "host", unsignedPayloadHash,
	}, "\n")
	stringToSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, sha256Hex(canonicalRequest)}, "\n")
	signature := hex.EncodeToString(hmacSHA256(signingKey(s.SecretKey, now.Format("20060102"), s.Region, "s3"), stringToSign))
	query.Set("X-Amz-Signature", signature)
	u.RawQuery = query.Encode()
	return u.String(), nil
}

const (
	emptyPayloadHash    = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	unsignedPayloadHash = "UNSIGNED-PAYLOAD"
)

func (s *S3Store) sign(req *http.Request, payloadHash string, now time.Time) {
	amzDate := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	req.Header.Set("x-amz-date", amzDate)
	if s.SessionToken != "" {
		req.Header.Set("x-amz-security-token", s.SessionToken)
	}
	headerNames := []string{"host"}
	headerValues := map[string]string{"host": req.URL.Host}
	for name := range req.Header {
		lower := strings.ToLower(name)
		if lower == "content-type" || strings.HasPrefix(lower, "x-amz-") {
			headerNames = append(headerNames, lower)
			headerValues[lower] = strings.TrimSpace(req.Header.Get(name))
		}
	}
	sort.Strings(headerNames)
	headerNames = dedupeStrings(headerNames)
	var canonicalHeaders strings.Builder
	for _, name := range headerNames {
		canonicalHeaders.WriteString(name + ":" + headerValues[name] + "\n")
	}
	signedHeaders := strings.Join(headerNames, ";")
	canonicalRequest := strings.Join([]string{
		req.Method, req.URL.EscapedPath(), canonicalQueryString(req.URL.Query()),
		canonicalHeaders.String(), signedHeaders, payloadHash,
	}, "\n")
	scope := strings.Join([]string{day, s.Region, "s3", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, sha256Hex(canonicalRequest)}, "\n")
	signature := hex.EncodeToString(hmacSHA256(signingKey(s.SecretKey, day, s.Region, "s3"), stringToSign))
	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.AccessKey, scope, signedHeaders, signature))
}

func canonicalQueryString(values url.Values) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := append([]string(nil), values[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

func signingKey(secret, day, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), day)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func sha256Hex(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

func dedupeStrings(in []string) []string {
	out := in[:0]
	last := ""
	for i, v := range in {
		if i == 0 || v != last {
			out = append(out, v)
			last = v
		}
	}
	return out
}
