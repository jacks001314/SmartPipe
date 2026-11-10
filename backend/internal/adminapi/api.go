// Package adminapi 实现集中配置管理台的服务端接口：项目管理、凭据管理、
// 连接测试、审计查询。所有写操作都会落审计，凭据明文永不返回。
package adminapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/conncheck"
	"smartpipe/backend/internal/store"
)

// Config 是管理 API 的依赖注入配置。
type Config struct {
	Store          *store.Store
	Checker        *conncheck.Checker
	AdminToken     string
	AllowOrigins   []string
	VerifyURL      string
	VerifyToken    string
	ReleaseURL     string
	ReleaseToken   string
	BotAddr        string
	BotToken       string
	SchedulerURL   string
	SchedulerToken string
	// CrashRetentionDays 原始崩溃包保留天数（0 表示用默认 30 天）。
	CrashRetentionDays int
	// RejectedRetentionDays 被拒绝上报的保留天数（0 表示默认 7 天）。
	RejectedRetentionDays int
	// DevOpen 在未配置 token 时允许无鉴权访问，仅供本地开发。
	DevOpen bool
	// AdminUser / AdminPassword 是控制台登录账号；密码为空表示不启用登录。
	AdminUser          string
	AdminPassword      string
	AdminPasswordReset bool
	SessionTTL         time.Duration
	CookieSecure       bool
	Version            string
}

// API 持有处理请求所需的依赖。
type API struct {
	store                 *store.Store
	checker               *conncheck.Checker
	token                 string
	origins               []string
	devOpen               bool
	version               string
	verifyURL             string
	verifyToken           string
	releaseURL            string
	releaseToken          string
	botAddr               string
	botToken              string
	schedulerURL          string
	schedulerToken        string
	githubHTTPMu          sync.RWMutex
	githubHTTP            *http.Client
	crashRetentionDays    int
	rejectedRetentionDays int
	loginEnabled          bool
	adminUser             string
	adminPassword         string
	passwordReset         bool
	sessionTTL            time.Duration
	cookieSecure          bool
	oauthFlows            *githubOAuthState
	appSetups             *githubAppSetupState
}

// New 构造 API。
func New(cfg Config) *API {
	checker := cfg.Checker
	if checker == nil {
		checker = conncheck.New()
	}
	token := strings.TrimSpace(cfg.AdminToken)
	return &API{
		store:                 cfg.Store,
		checker:               checker,
		token:                 token,
		origins:               cfg.AllowOrigins,
		devOpen:               cfg.DevOpen || token == "",
		version:               cfg.Version,
		verifyURL:             strings.TrimSpace(cfg.VerifyURL),
		verifyToken:           strings.TrimSpace(cfg.VerifyToken),
		releaseURL:            strings.TrimSpace(cfg.ReleaseURL),
		releaseToken:          strings.TrimSpace(cfg.ReleaseToken),
		botAddr:               strings.TrimSpace(cfg.BotAddr),
		botToken:              strings.TrimSpace(cfg.BotToken),
		schedulerURL:          strings.TrimSpace(cfg.SchedulerURL),
		schedulerToken:        strings.TrimSpace(cfg.SchedulerToken),
		crashRetentionDays:    cfg.CrashRetentionDays,
		rejectedRetentionDays: cfg.RejectedRetentionDays,
		loginEnabled:          strings.TrimSpace(cfg.AdminPassword) != "",
		adminUser:             firstNonEmpty(cfg.AdminUser, "admin"),
		adminPassword:         cfg.AdminPassword,
		passwordReset:         cfg.AdminPasswordReset,
		sessionTTL:            cfg.SessionTTL,
		cookieSecure:          cfg.CookieSecure,
		oauthFlows:            newGitHubOAuthState(),
		appSetups:             newGitHubAppSetupState(),
	}
}

type envelope struct {
	Data any       `json:"data,omitempty"`
	Err  *apiError `json:"error,omitempty"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Register 把管理路由挂到 Iris 应用上。
func (a *API) Register(app *iris.Application) {
	a.registerAuth(app)
	party := app.Party("/admin")
	party.Use(a.cors, a.auth)

	party.Get("/health", a.health)

	party.Get("/projects", a.listProjects)
	party.Post("/projects", a.createProject)
	party.Get("/projects/{id}", a.getProject)
	party.Patch("/projects/{id}", a.updateProject)
	party.Delete("/projects/{id}", a.deleteProject)
	party.Post("/projects/{id}/enabled", a.setProjectEnabled)

	party.Get("/credentials", a.listCredentials)
	party.Post("/credentials", a.createCredential)
	party.Post("/credentials/{id}/rotate", a.rotateCredential)
	party.Post("/credentials/{id}/enabled", a.setCredentialEnabled)
	party.Post("/credentials/{id}/test", a.testCredential)
	party.Delete("/credentials/{id}", a.deleteCredential)

	party.Get("/audit", a.listAudit)
	party.Get("/attempts", a.listAttempts)
	a.registerRepoSync(party)

	a.registerVerification(party)
	a.registerMerges(party)
	a.registerReleases(party)
	a.registerCrashes(party)
	a.registerIssues(party)
	a.registerIdentities(party)
	a.registerModels(party)
	a.registerLanxin(party)
	a.registerScheduler(party)
	a.registerGitHubOAuth(party)
	a.registerGitHubApp(party)
	a.registerGitHubClone(party)
	a.registerGitHubPAT(party)
	a.registerGitHubRepos(party)
	a.registerGitHubProxy(party)
	a.registerGitHubTunnel(party)
	a.registerLanxinInternal(app)
}

func (a *API) cors(ctx iris.Context) {
	origin := ctx.GetHeader("Origin")
	if origin != "" && a.originAllowed(origin) {
		ctx.Header("Access-Control-Allow-Origin", origin)
		ctx.Header("Vary", "Origin")
		ctx.Header("Access-Control-Allow-Credentials", "true")
		ctx.Header("Access-Control-Allow-Methods", "GET,POST,PATCH,DELETE,OPTIONS")
		ctx.Header("Access-Control-Allow-Headers", "Authorization,Content-Type,X-Trace-Id")
		ctx.Header("Access-Control-Max-Age", "600")
	}
	if ctx.Method() == http.MethodOptions {
		ctx.StatusCode(http.StatusNoContent)
		ctx.StopExecution()
		return
	}
	ctx.Next()
}

func (a *API) originAllowed(origin string) bool {
	if len(a.origins) == 0 {
		return strings.HasPrefix(origin, "http://localhost:") ||
			strings.HasPrefix(origin, "http://127.0.0.1:")
	}
	for _, allowed := range a.origins {
		if allowed == "*" || strings.EqualFold(allowed, origin) {
			return true
		}
	}
	return false
}

func (a *API) auth(ctx iris.Context) {
	if a.devOpen {
		ctx.Values().Set("actor", "dev-admin")
		ctx.Next()
		return
	}
	header := ctx.GetHeader("Authorization")
	got := strings.TrimPrefix(header, "Bearer ")
	if header != "" && subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) == 1 {
		ctx.Values().Set("actor", "admin")
		ctx.Next()
		return
	}
	if sess, ok := a.sessionFromRequest(ctx); ok {
		ctx.Values().Set("actor", sess.Username)
		ctx.Values().Set("session", sess)
		ctx.Next()
		return
	}
	a.unauthorized(ctx, "需要登录或管理员令牌")
}

func (a *API) health(ctx iris.Context) {
	dbErr := a.store.Ping(ctx.Request().Context())
	status := "ok"
	code := http.StatusOK
	if dbErr != nil {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}
	ctx.StatusCode(code)
	_ = ctx.JSON(envelope{Data: iris.Map{
		"status":     status,
		"database":   dbErr == nil,
		"version":    a.version,
		"auth":       map[bool]string{true: "dev-open", false: "token"}[a.devOpen],
		"checked_at": time.Now().UTC().Format(time.RFC3339),
	}})
}

func (a *API) actor(ctx iris.Context) string {
	if v, ok := ctx.Values().Get("actor").(string); ok && v != "" {
		return v
	}
	return "unknown"
}

func (a *API) traceID(ctx iris.Context) string {
	if id := ctx.GetHeader("X-Trace-Id"); id != "" {
		return id
	}
	return fmt.Sprintf("adm-%d", time.Now().UnixNano())
}

func (a *API) audit(ctx context.Context, c iris.Context, action, targetType, targetID string, projectID *string, result string, detail map[string]any) {
	entry := store.AuditEntry{
		Actor:      a.actor(c),
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		ProjectID:  projectID,
		Result:     result,
		Detail:     detail,
		TraceID:    a.traceID(c),
	}
	// 审计写入失败不应吞掉主流程结果；错误由日志与后续巡检发现。
	_ = a.store.AppendAudit(ctx, entry)
}

func (a *API) fail(ctx iris.Context, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		ctx.StatusCode(http.StatusNotFound)
		_ = ctx.JSON(envelope{Err: &apiError{Code: "not_found", Message: "记录不存在"}})
	case errors.Is(err, store.ErrConflict):
		ctx.StatusCode(http.StatusConflict)
		_ = ctx.JSON(envelope{Err: &apiError{Code: "conflict", Message: err.Error()}})
	default:
		ctx.StatusCode(http.StatusInternalServerError)
		_ = ctx.JSON(envelope{Err: &apiError{Code: "internal", Message: "服务内部错误"}})
	}
}

func (a *API) badRequest(ctx iris.Context, msg string) {
	ctx.StatusCode(http.StatusBadRequest)
	_ = ctx.JSON(envelope{Err: &apiError{Code: "invalid_argument", Message: msg}})
}

func decodeBody(c iris.Context, out any) error {
	body, err := c.GetBody()
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return errors.New("请求体为空")
	}
	return json.Unmarshal(body, out)
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// intQuery 读取正整数查询参数，非法或缺失时用 fallback。
func intQuery(c iris.Context, key string, fallback int) int {
	if v := c.URLParam(key); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}
