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
	"strings"
	"time"

	"github.com/kataras/iris/v12"

	"smartpipe/backend/internal/conncheck"
	rt "smartpipe/backend/internal/runtime"
	"smartpipe/backend/internal/store"
)

// Config 是管理 API 的依赖注入配置。
type Config struct {
	Store          *store.Store
	Checker        *conncheck.Checker
	Runtime        *rt.Manager
	AdminToken     string
	AllowOrigins   []string
	AppServerBin   string
	RuntimeDataDir string
	AppServerToken string
	VerifyURL      string
	VerifyToken    string
	ReleaseURL     string
	ReleaseToken   string
	BotAddr        string
	BotToken       string
	MCPURL         string
	MCPPoolName    string
	// DevOpen 在未配置 token 时允许无鉴权访问，仅供本地开发。
	DevOpen bool
	Version string
}

// API 持有处理请求所需的依赖。
type API struct {
	store           *store.Store
	checker         *conncheck.Checker
	runtime         *rt.Manager
	token           string
	origins         []string
	devOpen         bool
	version         string
	appServerBinary string
	runtimeDataDir  string
	appServerToken  string
	verifyURL       string
	verifyToken     string
	releaseURL      string
	releaseToken    string
	botAddr         string
	botToken        string
	mcpURL          string
	mcpPoolName     string
}

// New 构造 API。
func New(cfg Config) *API {
	checker := cfg.Checker
	if checker == nil {
		checker = conncheck.New()
	}
	runtimeManager := cfg.Runtime
	if runtimeManager == nil {
		runtimeManager = rt.NewManager()
	}
	token := strings.TrimSpace(cfg.AdminToken)
	return &API{
		store:           cfg.Store,
		checker:         checker,
		runtime:         runtimeManager,
		token:           token,
		origins:         cfg.AllowOrigins,
		devOpen:         cfg.DevOpen || token == "",
		version:         cfg.Version,
		appServerBinary: cfg.AppServerBin,
		runtimeDataDir:  cfg.RuntimeDataDir,
		appServerToken:  cfg.AppServerToken,
		verifyURL:       strings.TrimSpace(cfg.VerifyURL),
		verifyToken:     strings.TrimSpace(cfg.VerifyToken),
		releaseURL:      strings.TrimSpace(cfg.ReleaseURL),
		releaseToken:    strings.TrimSpace(cfg.ReleaseToken),
		botAddr:         strings.TrimSpace(cfg.BotAddr),
		botToken:        strings.TrimSpace(cfg.BotToken),
		mcpURL:          strings.TrimSpace(cfg.MCPURL),
		mcpPoolName:     strings.TrimSpace(cfg.MCPPoolName),
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
	party := app.Party("/admin")
	party.Use(a.cors, a.auth)

	party.Get("/health", a.health)

	party.Get("/projects", a.listProjects)
	party.Post("/projects", a.createProject)
	party.Get("/projects/{id}", a.getProject)
	party.Patch("/projects/{id}", a.updateProject)
	party.Post("/projects/{id}/enabled", a.setProjectEnabled)

	party.Get("/credentials", a.listCredentials)
	party.Post("/credentials", a.createCredential)
	party.Post("/credentials/{id}/rotate", a.rotateCredential)
	party.Post("/credentials/{id}/enabled", a.setCredentialEnabled)
	party.Post("/credentials/{id}/test", a.testCredential)
	party.Delete("/credentials/{id}", a.deleteCredential)

	party.Get("/audit", a.listAudit)
	party.Get("/attempts", a.listAttempts)

	a.registerRuntime(party)
	a.registerBridge(party)
	a.registerVerification(party)
	a.registerMerges(party)
	a.registerDerivations(party)
	a.registerReleases(party)
	a.registerCrashes(party)
	a.registerAgentTokens(party)
	a.registerIssues(party)
	a.registerToolCalls(party)
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
	if header == "" || subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) != 1 {
		ctx.StatusCode(http.StatusUnauthorized)
		_ = ctx.JSON(envelope{Err: &apiError{Code: "unauthorized", Message: "需要管理员令牌"}})
		return
	}
	ctx.Values().Set("actor", "admin")
	ctx.Next()
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
