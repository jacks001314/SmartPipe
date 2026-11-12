package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"smartpipe/backend/internal/secretbox"
)

const (
	ResourceKindSSH        = "ssh"
	ResourceAuthPassword   = "password"
	ResourceAuthPrivateKey = "private_key"
)

// RuntimeResource is the public view of a reusable online resource. Encrypted
// material is deliberately absent from this type so it cannot be serialized by
// an API or audit handler by accident.
type RuntimeResource struct {
	ID                 string         `json:"id"`
	Kind               string         `json:"kind"`
	Name               string         `json:"name"`
	Description        string         `json:"description"`
	Host               string         `json:"host"`
	Port               int            `json:"port"`
	Username           string         `json:"username"`
	AuthType           string         `json:"auth_type"`
	SecretMask         string         `json:"secret_mask"`
	PassphraseSet      bool           `json:"passphrase_set"`
	HostKeyFingerprint string         `json:"host_key_fingerprint"`
	Tags               []string       `json:"tags"`
	Enabled            bool           `json:"enabled"`
	LastTestedAt       *time.Time     `json:"last_tested_at"`
	LastTestOK         *bool          `json:"last_test_ok"`
	LastTestMessage    string         `json:"last_test_message"`
	SystemInfo         map[string]any `json:"system_info"`
	InspectedAt        *time.Time     `json:"inspected_at"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
}

// SSHResourceInput creates an SSH resource.
type SSHResourceInput struct {
	Name               string
	Description        string
	Host               string
	Port               int
	Username           string
	AuthType           string
	Secret             string
	Passphrase         string
	HostKeyFingerprint string
	Tags               []string
	Enabled            bool
}

// SSHResourceUpdate replaces the editable fields of an SSH resource. Empty
// Secret and Passphrase values keep the currently stored values.
type SSHResourceUpdate struct {
	Name               string
	Description        string
	Host               string
	Port               int
	Username           string
	AuthType           string
	Secret             string
	Passphrase         string
	HostKeyFingerprint string
	Tags               []string
	Enabled            bool
}

const runtimeResourceColumns = `id, kind, name, description, host, port, username, auth_type,
    secret_mask, passphrase_ciphertext <> '', host_key_fingerprint, tags, enabled,
    last_tested_at, last_test_ok, last_test_message, system_info, inspected_at,
    created_at, updated_at`

func scanRuntimeResource(row interface{ Scan(...any) error }) (*RuntimeResource, error) {
	var (
		resource    RuntimeResource
		rawTags     []byte
		rawSystem   []byte
		lastTested  sql.NullTime
		lastTestOK  sql.NullBool
		inspectedAt sql.NullTime
	)
	err := row.Scan(
		&resource.ID, &resource.Kind, &resource.Name, &resource.Description,
		&resource.Host, &resource.Port, &resource.Username, &resource.AuthType,
		&resource.SecretMask, &resource.PassphraseSet, &resource.HostKeyFingerprint,
		&rawTags, &resource.Enabled, &lastTested, &lastTestOK,
		&resource.LastTestMessage, &rawSystem, &inspectedAt,
		&resource.CreatedAt, &resource.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	resource.Tags = []string{}
	if len(rawTags) > 0 {
		if err := json.Unmarshal(rawTags, &resource.Tags); err != nil {
			return nil, fmt.Errorf("store: decode resource tags: %w", err)
		}
	}
	if resource.Tags == nil {
		resource.Tags = []string{}
	}
	resource.SystemInfo = map[string]any{}
	if len(rawSystem) > 0 {
		if err := json.Unmarshal(rawSystem, &resource.SystemInfo); err != nil {
			return nil, fmt.Errorf("store: decode resource system info: %w", err)
		}
	}
	if resource.SystemInfo == nil {
		resource.SystemInfo = map[string]any{}
	}
	if lastTested.Valid {
		value := lastTested.Time
		resource.LastTestedAt = &value
	}
	if lastTestOK.Valid {
		value := lastTestOK.Bool
		resource.LastTestOK = &value
	}
	if inspectedAt.Valid {
		value := inspectedAt.Time
		resource.InspectedAt = &value
	}
	return &resource, nil
}

func normalizeResourceTags(tags []string) ([]string, error) {
	out := make([]string, 0, len(tags))
	seen := make(map[string]bool, len(tags))
	for _, raw := range tags {
		tag := strings.TrimSpace(raw)
		if tag == "" {
			continue
		}
		if len([]rune(tag)) > 32 || strings.ContainsAny(tag, "\r\n\t/\\") {
			return nil, errors.New("资源标签不能超过 32 个字符，且不能包含控制字符")
		}
		key := strings.ToLower(tag)
		if seen[key] {
			continue
		}
		if len(out) >= 16 {
			return nil, errors.New("单个资源最多设置 16 个用途标签")
		}
		seen[key] = true
		out = append(out, tag)
	}
	return out, nil
}

func normalizeSSHResource(name, host, username, authType, fingerprint string, port int) (string, string, string, string, string, int, error) {
	name = strings.TrimSpace(name)
	host = strings.TrimSpace(host)
	username = strings.TrimSpace(username)
	authType = strings.TrimSpace(authType)
	fingerprint = strings.TrimSpace(fingerprint)
	if name == "" || len([]rune(name)) > 100 {
		return "", "", "", "", "", 0, errors.New("资源名称不能为空，且不能超过 100 个字符")
	}
	if host == "" || len(host) > 255 || strings.ContainsAny(host, " /\\\t\r\n") {
		return "", "", "", "", "", 0, errors.New("主机名或 IP 不合法")
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	if strings.Contains(host, ":") && net.ParseIP(strings.Trim(host, "[]")) == nil {
		return "", "", "", "", "", 0, errors.New("主机字段不要包含端口，请使用独立的 SSH 端口字段")
	}
	if port == 0 {
		port = 22
	}
	if port < 1 || port > 65535 {
		return "", "", "", "", "", 0, errors.New("SSH 端口必须在 1 到 65535 之间")
	}
	if username == "" || len(username) > 128 || strings.ContainsAny(username, " \t\r\n") {
		return "", "", "", "", "", 0, errors.New("SSH 用户名不能为空，且不能包含空白字符")
	}
	if authType == "" {
		authType = ResourceAuthPassword
	}
	if authType != ResourceAuthPassword && authType != ResourceAuthPrivateKey {
		return "", "", "", "", "", 0, errors.New("SSH 认证方式必须是 password 或 private_key")
	}
	if fingerprint != "" && (!strings.HasPrefix(fingerprint, "SHA256:") || len(fingerprint) > 256) {
		return "", "", "", "", "", 0, errors.New("主机指纹必须使用 SHA256:... 格式")
	}
	return name, host, username, authType, fingerprint, port, nil
}

// CreateSSHResource encrypts the SSH password/private key and saves a reusable
// resource record. The plaintext is never returned.
func (s *Store) CreateSSHResource(ctx context.Context, in SSHResourceInput) (*RuntimeResource, error) {
	name, host, username, authType, fingerprint, port, err := normalizeSSHResource(in.Name, in.Host, in.Username, in.AuthType, in.HostKeyFingerprint, in.Port)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Secret) == "" {
		return nil, errors.New("SSH 密码或私钥不能为空")
	}
	tags, err := normalizeResourceTags(in.Tags)
	if err != nil {
		return nil, err
	}
	rawTags, err := json.Marshal(tags)
	if err != nil {
		return nil, fmt.Errorf("store: encode resource tags: %w", err)
	}
	sealed, err := s.box.Seal(in.Secret)
	if err != nil {
		return nil, fmt.Errorf("store: seal SSH secret: %w", err)
	}
	sealedPassphrase := ""
	if strings.TrimSpace(in.Passphrase) != "" {
		sealedPassphrase, err = s.box.Seal(in.Passphrase)
		if err != nil {
			return nil, fmt.Errorf("store: seal SSH passphrase: %w", err)
		}
	}
	id := newID("res")
	endpoint := net.JoinHostPort(host, strconv.Itoa(port))
	row := s.db.QueryRowContext(ctx, `
INSERT INTO runtime_resources (
    id, kind, name, description, endpoint, host, port, username, auth_type,
    secret_ciphertext, secret_mask, passphrase_ciphertext, host_key_fingerprint,
    tags, enabled
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14::jsonb,$15)
RETURNING `+runtimeResourceColumns,
		id, ResourceKindSSH, name, strings.TrimSpace(in.Description), endpoint, host, port,
		username, authType, sealed, secretbox.Mask(in.Secret), sealedPassphrase,
		fingerprint, string(rawTags), in.Enabled,
	)
	resource, err := scanRuntimeResource(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: resource %s already exists", ErrConflict, name)
		}
		return nil, fmt.Errorf("store: create SSH resource: %w", err)
	}
	return resource, nil
}

// ListSSHResources returns all SSH resources, newest updates first.
func (s *Store) ListSSHResources(ctx context.Context) ([]RuntimeResource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runtimeResourceColumns+`
  FROM runtime_resources WHERE kind = $1 ORDER BY updated_at DESC, created_at DESC`, ResourceKindSSH)
	if err != nil {
		return nil, fmt.Errorf("store: list SSH resources: %w", err)
	}
	defer rows.Close()
	out := make([]RuntimeResource, 0, 8)
	for rows.Next() {
		resource, err := scanRuntimeResource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *resource)
	}
	return out, rows.Err()
}

// ListAvailableSSHResources is the consumer-facing catalog. It returns only
// enabled resources and supports an optional exact tag filter such as "build".
func (s *Store) ListAvailableSSHResources(ctx context.Context, tag string) ([]RuntimeResource, error) {
	tag = strings.TrimSpace(tag)
	var rows *sql.Rows
	var err error
	if tag == "" {
		rows, err = s.db.QueryContext(ctx, `SELECT `+runtimeResourceColumns+`
  FROM runtime_resources WHERE kind = $1 AND enabled AND last_test_ok IS TRUE ORDER BY name`, ResourceKindSSH)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT `+runtimeResourceColumns+`
  FROM runtime_resources WHERE kind = $1 AND enabled AND last_test_ok IS TRUE AND tags ? $2 ORDER BY name`, ResourceKindSSH, tag)
	}
	if err != nil {
		return nil, fmt.Errorf("store: list available SSH resources: %w", err)
	}
	defer rows.Close()
	out := make([]RuntimeResource, 0, 4)
	for rows.Next() {
		resource, err := scanRuntimeResource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *resource)
	}
	return out, rows.Err()
}

// GetSSHResource returns one public resource view.
func (s *Store) GetSSHResource(ctx context.Context, id string) (*RuntimeResource, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+runtimeResourceColumns+`
  FROM runtime_resources WHERE id = $1 AND kind = $2`, id, ResourceKindSSH)
	return scanRuntimeResource(row)
}

// GetSSHResourceSecret decrypts the material needed by the SSH client. It is a
// backend-only method; handlers must never serialize its plaintext values.
func (s *Store) GetSSHResourceSecret(ctx context.Context, id string) (*RuntimeResource, string, string, error) {
	var (
		sealedSecret     string
		sealedPassphrase string
	)
	row := s.db.QueryRowContext(ctx, `SELECT `+runtimeResourceColumns+`,
    secret_ciphertext, passphrase_ciphertext FROM runtime_resources
   WHERE id = $1 AND kind = $2`, id, ResourceKindSSH)
	// Scan the public fields and encrypted payload together.
	var (
		resource    RuntimeResource
		rawTags     []byte
		rawSystem   []byte
		lastTested  sql.NullTime
		lastTestOK  sql.NullBool
		inspectedAt sql.NullTime
	)
	err := row.Scan(
		&resource.ID, &resource.Kind, &resource.Name, &resource.Description,
		&resource.Host, &resource.Port, &resource.Username, &resource.AuthType,
		&resource.SecretMask, &resource.PassphraseSet, &resource.HostKeyFingerprint,
		&rawTags, &resource.Enabled, &lastTested, &lastTestOK,
		&resource.LastTestMessage, &rawSystem, &inspectedAt,
		&resource.CreatedAt, &resource.UpdatedAt, &sealedSecret, &sealedPassphrase,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", "", ErrNotFound
	}
	if err != nil {
		return nil, "", "", fmt.Errorf("store: get SSH resource secret: %w", err)
	}
	_ = json.Unmarshal(rawTags, &resource.Tags)
	if resource.Tags == nil {
		resource.Tags = []string{}
	}
	_ = json.Unmarshal(rawSystem, &resource.SystemInfo)
	if resource.SystemInfo == nil {
		resource.SystemInfo = map[string]any{}
	}
	if lastTested.Valid {
		value := lastTested.Time
		resource.LastTestedAt = &value
	}
	if lastTestOK.Valid {
		value := lastTestOK.Bool
		resource.LastTestOK = &value
	}
	if inspectedAt.Valid {
		value := inspectedAt.Time
		resource.InspectedAt = &value
	}
	secret, err := s.box.Open(sealedSecret)
	if err != nil {
		return nil, "", "", fmt.Errorf("store: decrypt SSH secret: %w", err)
	}
	passphrase := ""
	if sealedPassphrase != "" {
		passphrase, err = s.box.Open(sealedPassphrase)
		if err != nil {
			return nil, "", "", fmt.Errorf("store: decrypt SSH passphrase: %w", err)
		}
	}
	return &resource, secret, passphrase, nil
}

// UpdateSSHResource replaces editable fields. Empty Secret/Passphrase keep the
// existing encrypted material; changing connection identity resets test state.
func (s *Store) UpdateSSHResource(ctx context.Context, id string, in SSHResourceUpdate) (*RuntimeResource, error) {
	name, host, username, authType, fingerprint, port, err := normalizeSSHResource(in.Name, in.Host, in.Username, in.AuthType, in.HostKeyFingerprint, in.Port)
	if err != nil {
		return nil, err
	}
	tags, err := normalizeResourceTags(in.Tags)
	if err != nil {
		return nil, err
	}
	rawTags, err := json.Marshal(tags)
	if err != nil {
		return nil, fmt.Errorf("store: encode resource tags: %w", err)
	}
	current, err := s.GetSSHResource(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.AuthType != authType && strings.TrimSpace(in.Secret) == "" {
		return nil, errors.New("切换认证方式时必须重新填写密码或私钥")
	}
	var sealedSecret, mask, sealedPassphrase string
	if err := s.db.QueryRowContext(ctx, `SELECT secret_ciphertext, secret_mask, passphrase_ciphertext
  FROM runtime_resources WHERE id = $1 AND kind = $2`, id, ResourceKindSSH).Scan(&sealedSecret, &mask, &sealedPassphrase); err != nil {
		return nil, fmt.Errorf("store: load SSH resource secret state: %w", err)
	}
	if strings.TrimSpace(in.Secret) != "" {
		sealedSecret, err = s.box.Seal(in.Secret)
		if err != nil {
			return nil, fmt.Errorf("store: seal SSH secret: %w", err)
		}
		mask = secretbox.Mask(in.Secret)
	}
	if strings.TrimSpace(in.Passphrase) != "" {
		sealedPassphrase, err = s.box.Seal(in.Passphrase)
		if err != nil {
			return nil, fmt.Errorf("store: seal SSH passphrase: %w", err)
		}
	}
	if authType == ResourceAuthPassword {
		sealedPassphrase = ""
	}
	connectionChanged := current.Host != host || current.Port != port || current.Username != username ||
		current.AuthType != authType || strings.TrimSpace(in.Secret) != "" || strings.TrimSpace(in.Passphrase) != "" ||
		current.HostKeyFingerprint != fingerprint
	endpoint := net.JoinHostPort(host, strconv.Itoa(port))
	row := s.db.QueryRowContext(ctx, `
UPDATE runtime_resources SET
    name = $3, description = $4, endpoint = $5, host = $6, port = $7,
    username = $8, auth_type = $9, secret_ciphertext = $10, secret_mask = $11,
    passphrase_ciphertext = $12, host_key_fingerprint = $13, tags = $14::jsonb,
    enabled = $15,
    last_tested_at = CASE WHEN $16 THEN NULL ELSE last_tested_at END,
    last_test_ok = CASE WHEN $16 THEN NULL ELSE last_test_ok END,
    last_test_message = CASE WHEN $16 THEN '' ELSE last_test_message END,
    updated_at = now()
 WHERE id = $1 AND kind = $2
RETURNING `+runtimeResourceColumns,
		id, ResourceKindSSH, name, strings.TrimSpace(in.Description), endpoint, host, port,
		username, authType, sealedSecret, mask, sealedPassphrase, fingerprint,
		string(rawTags), in.Enabled, connectionChanged,
	)
	resource, err := scanRuntimeResource(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: resource %s already exists", ErrConflict, name)
		}
		return nil, fmt.Errorf("store: update SSH resource: %w", err)
	}
	return resource, nil
}

// SetSSHResourceEnabled marks whether consumers may select the resource.
func (s *Store) SetSSHResourceEnabled(ctx context.Context, id string, enabled bool) (*RuntimeResource, error) {
	row := s.db.QueryRowContext(ctx, `UPDATE runtime_resources SET enabled = $3, updated_at = now()
  WHERE id = $1 AND kind = $2 RETURNING `+runtimeResourceColumns, id, ResourceKindSSH, enabled)
	return scanRuntimeResource(row)
}

// DeleteSSHResource removes the resource and its encrypted material.
func (s *Store) DeleteSSHResource(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM runtime_resources WHERE id = $1 AND kind = $2`, id, ResourceKindSSH)
	if err != nil {
		return fmt.Errorf("store: delete SSH resource: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordSSHResourceCheck stores a connection test and pins a newly observed
// fingerprint. Existing pins are preserved unless the caller deliberately
// updated the resource first.
func (s *Store) RecordSSHResourceCheck(ctx context.Context, id string, ok bool, message, fingerprint string) (*RuntimeResource, error) {
	if len(message) > 1000 {
		message = message[:1000]
	}
	row := s.db.QueryRowContext(ctx, `
UPDATE runtime_resources SET
    last_tested_at = now(), last_test_ok = $3, last_test_message = $4,
    host_key_fingerprint = CASE WHEN $3 AND host_key_fingerprint = '' THEN $5 ELSE host_key_fingerprint END,
    updated_at = now()
 WHERE id = $1 AND kind = $2
RETURNING `+runtimeResourceColumns, id, ResourceKindSSH, ok, message, strings.TrimSpace(fingerprint))
	return scanRuntimeResource(row)
}

// RecordSSHResourceInspection stores test state and the normalized system
// profile gathered from the host.
func (s *Store) RecordSSHResourceInspection(ctx context.Context, id string, ok bool, message, fingerprint string, systemInfo map[string]any) (*RuntimeResource, error) {
	if len(message) > 1000 {
		message = message[:1000]
	}
	rawSystem, err := json.Marshal(systemInfo)
	if err != nil {
		return nil, fmt.Errorf("store: encode system info: %w", err)
	}
	row := s.db.QueryRowContext(ctx, `
UPDATE runtime_resources SET
    last_tested_at = now(), last_test_ok = $3, last_test_message = $4,
    host_key_fingerprint = CASE WHEN $3 AND host_key_fingerprint = '' THEN $5 ELSE host_key_fingerprint END,
    system_info = CASE WHEN $3 THEN $6::jsonb ELSE system_info END,
    inspected_at = CASE WHEN $3 THEN now() ELSE inspected_at END,
    updated_at = now()
 WHERE id = $1 AND kind = $2
RETURNING `+runtimeResourceColumns, id, ResourceKindSSH, ok, message, strings.TrimSpace(fingerprint), string(rawSystem))
	return scanRuntimeResource(row)
}
