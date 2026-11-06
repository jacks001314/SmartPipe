package adminapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/kataras/iris/v12"
	"golang.org/x/crypto/bcrypt"

	"smartpipe/backend/internal/store"
)

const sessionCookie = "smartpipe_session"

// registerAuth 挂载登录相关接口；这些路由不走 Bearer 鉴权中间件。
func (a *API) registerAuth(app *iris.Application) {
	pub := app.Party("/admin")
	pub.Use(a.cors)
	pub.Get("/auth-config", a.authConfig)
	pub.Post("/login", a.login)
	pub.Post("/logout", a.logout)
	pub.Get("/session", a.sessionInfo)
}

// Bootstrap 按环境变量创建首个管理员账号。已有账号时不会覆盖密码，
// 除非显式设置 SMARTPIPE_ADMIN_PASSWORD_RESET=true。
func (a *API) Bootstrap(ctx context.Context) error {
	if !a.loginEnabled {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(a.adminPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	count, err := a.store.CountAdminUsers(ctx)
	if err != nil {
		return err
	}
	if count == 0 {
		_, err = a.store.CreateAdminUser(ctx, a.adminUser, string(hash), a.adminUser)
		return err
	}
	user, err := a.store.GetAdminUserByUsername(ctx, a.adminUser)
	if err == store.ErrNotFound {
		_, err = a.store.CreateAdminUser(ctx, a.adminUser, string(hash), a.adminUser)
		return err
	}
	if err != nil {
		return err
	}
	if a.passwordReset {
		return a.store.UpdateAdminPassword(ctx, user.ID, string(hash))
	}
	return nil
}

func (a *API) authConfig(c iris.Context) {
	_ = c.JSON(envelope{Data: iris.Map{
		"login_enabled":  a.loginEnabled,
		"dev_open":       a.devOpen,
		"token_required": !a.devOpen,
	}})
}

func (a *API) login(c iris.Context) {
	if !a.loginEnabled {
		a.badRequest(c, "服务端未启用登录（未配置 SMARTPIPE_ADMIN_PASSWORD）")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeBody(c, &in); err != nil {
		a.badRequest(c, "请求体不是合法 JSON")
		return
	}
	username := strings.TrimSpace(in.Username)
	if username == "" || in.Password == "" {
		a.badRequest(c, "用户名和密码都不能为空")
		return
	}
	ctx := c.Request().Context()
	user, err := a.store.GetAdminUserByUsername(ctx, username)
	if err != nil || !user.Enabled {
		a.loginFailed(c, username)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(in.Password)) != nil {
		a.loginFailed(c, username)
		return
	}
	token, sessionID, err := newSessionToken()
	if err != nil {
		a.fail(c, err)
		return
	}
	ttl := a.sessionTTL
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	expires := time.Now().Add(ttl)
	if err := a.store.CreateAdminSession(ctx, sessionID, user.ID, expires, c.GetHeader("User-Agent"), c.RemoteAddr()); err != nil {
		a.fail(c, err)
		return
	}
	_ = a.store.TouchAdminLogin(ctx, user.ID)
	http.SetCookie(c.ResponseWriter(), &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.cookieSecure,
		Expires:  expires,
		MaxAge:   int(ttl.Seconds()),
	})
	a.audit(ctx, c, "auth.login", "admin_user", user.ID, nil, "ok", map[string]any{"username": user.Username})
	_ = c.JSON(envelope{Data: iris.Map{
		"username":     user.Username,
		"display_name": user.DisplayName,
		"expires_at":   expires,
	}})
}

func (a *API) loginFailed(c iris.Context, username string) {
	a.audit(c.Request().Context(), c, "auth.login", "admin_user", username, nil, "failed", nil)
	c.StatusCode(http.StatusUnauthorized)
	_ = c.JSON(envelope{Err: &apiError{Code: "invalid_credentials", Message: "用户名或密码错误"}})
}

func (a *API) logout(c iris.Context) {
	if cookie, err := c.Request().Cookie(sessionCookie); err == nil && cookie.Value != "" {
		_ = a.store.DeleteAdminSession(c.Request().Context(), hashToken(cookie.Value))
	}
	http.SetCookie(c.ResponseWriter(), &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1,
		SameSite: http.SameSiteLaxMode, Secure: a.cookieSecure,
	})
	c.StatusCode(http.StatusNoContent)
}

func (a *API) sessionInfo(c iris.Context) {
	ctx := c.Request().Context()
	if a.devOpen {
		_ = c.JSON(envelope{Data: iris.Map{"username": "dev-admin", "display_name": "本地开发", "mode": "dev"}})
		return
	}
	if header := c.GetHeader("Authorization"); strings.HasPrefix(header, "Bearer ") {
		if subtleCompare(strings.TrimPrefix(header, "Bearer "), a.token) {
			_ = c.JSON(envelope{Data: iris.Map{"username": "admin", "display_name": "管理员", "mode": "token"}})
			return
		}
	}
	cookie, err := c.Request().Cookie(sessionCookie)
	if err != nil || cookie == nil || cookie.Value == "" {
		a.unauthorized(c, "请先登录")
		return
	}
	sess, err := a.store.GetAdminSession(ctx, hashToken(cookie.Value))
	if err != nil {
		a.unauthorized(c, "登录已过期，请重新登录")
		return
	}
	_ = c.JSON(envelope{Data: iris.Map{
		"username":     sess.Username,
		"display_name": sess.DisplayName,
		"mode":         "session",
		"expires_at":   sess.ExpiresAt,
	}})
}

func (a *API) unauthorized(c iris.Context, message string) {
	c.StatusCode(http.StatusUnauthorized)
	_ = c.JSON(envelope{Err: &apiError{Code: "unauthorized", Message: message}})
}

// sessionFromRequest 返回当前请求的会话（Cookie 登录时）。
func (a *API) sessionFromRequest(c iris.Context) (*store.AdminSession, bool) {
	cookie, err := c.Request().Cookie(sessionCookie)
	if err != nil || cookie == nil || cookie.Value == "" {
		return nil, false
	}
	sess, err := a.store.GetAdminSession(c.Request().Context(), hashToken(cookie.Value))
	if err != nil {
		return nil, false
	}
	a.store.TouchAdminSession(c.Request().Context(), sess.ID)
	return sess, true
}

func newSessionToken() (string, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token := hex.EncodeToString(buf)
	return token, hashToken(token), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func subtleCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
