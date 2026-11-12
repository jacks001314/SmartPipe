package adminapi

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/kataras/iris/v12"
)

// executorBootstrap 返回在构建机上直接执行的接入命令。
//
// 执行器是 pull 模式：它带着服务令牌向 release-service 注册、领任务，所以
// 「添加构建机」不需要远程登录，只要给出一段能粘贴到那台机器上的命令。
// 令牌只在已登录的管理员请求里返回，不写进前端产物或日志。
type executorBootstrap struct {
	ID         string `json:"id"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	ServiceURL string `json:"service_url"`
	Command    string `json:"command"`
}

func (a *API) getReleaseV2ExecutorBootstrap(c iris.Context) {
	executorID := strings.TrimSpace(c.URLParamDefault("id", "build-01"))
	osName := strings.ToLower(strings.TrimSpace(c.URLParamDefault("os", "linux")))
	arch := strings.ToLower(strings.TrimSpace(c.URLParamDefault("arch", "amd64")))
	if executorID == "" {
		a.badRequest(c, "构建机 id 不能为空")
		return
	}
	switch osName {
	case "darwin", "linux", "windows":
	default:
		a.badRequest(c, "系统只支持 darwin / linux / windows")
		return
	}
	switch arch {
	case "amd64", "arm64":
	default:
		a.badRequest(c, "架构只支持 amd64 / arm64")
		return
	}
	if strings.TrimSpace(a.releaseToken) == "" {
		a.badRequest(c, "服务端没有配置 release-service 令牌，无法生成接入命令")
		return
	}
	serviceURL := a.executorServiceURL(c)
	command := fmt.Sprintf(`export SMARTPIPE_RELEASE_SERVICE_URL=%s
export SMARTPIPE_RELEASE_SERVICE_TOKEN=%s
export SMARTPIPE_RELEASE_EXECUTOR_ID=%s
export SMARTPIPE_RELEASE_EXECUTOR_OS=%s
export SMARTPIPE_RELEASE_EXECUTOR_ARCH=%s
./release-executor`, serviceURL, a.releaseToken, executorID, osName, arch)
	a.audit(c.Request().Context(), c, "release.v2.executor.bootstrap", "release_executor", executorID, nil, "ok", map[string]any{
		"os": osName, "arch": arch,
	})
	_ = c.JSON(envelope{Data: executorBootstrap{
		ID: executorID, OS: osName, Arch: arch, ServiceURL: serviceURL, Command: command,
	}})
}

// executorServiceURL 给出构建机应当访问的 release-service 地址：
// 配置里若是 127.0.0.1/localhost（服务端本机），换成管理员当前访问的主机名，
// 这样局域网里的 macOS 构建机能直接连上。
func (a *API) executorServiceURL(c iris.Context) string {
	parsed, err := url.Parse(strings.TrimSpace(a.releaseURL))
	if err != nil || parsed.Host == "" {
		return strings.TrimRight(strings.TrimSpace(a.releaseURL), "/")
	}
	host := parsed.Hostname()
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		if requested := c.Request().Host; requested != "" {
			if name := strings.Split(requested, ":")[0]; name != "" && name != "localhost" && name != "127.0.0.1" {
				host = name
			}
		}
	}
	port := parsed.Port()
	if port != "" {
		host = host + ":" + port
	}
	return parsed.Scheme + "://" + host
}
