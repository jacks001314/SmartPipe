package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"smartpipe/backend/internal/bridge"
	"smartpipe/backend/internal/releasev2"
	"smartpipe/backend/internal/store"
)

// releaseVersionFiles 返回项目配置里需要同步版本号的文件列表。
// 一个仓库常有多份 package.json（根目录 + 应用目录），用逗号或换行分隔即可同时改。
func releaseVersionFiles(cfg *releasev2.ProjectReleaseConfig) []string {
	if cfg == nil {
		return nil
	}
	fields := strings.FieldsFunc(cfg.VersionFile, func(r rune) bool {
		switch r {
		case ',', ';', ' ', '\t', '\n', '\r':
			return true
		}
		return false
	})
	seen := make(map[string]bool, len(fields))
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		path := strings.TrimSpace(field)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

// setJSONString 只替换 JSON 文本里目标路径的字符串值，其余内容和键顺序保持不变。
// 版本文件是 package.json 这类人手维护的文件，整体重新序列化会产生巨大的无意义 diff。
// 只支持对象路径（如 version、package.version）；数组下标路径不参与匹配。
func setJSONString(raw, jsonPath, value string) (string, error) {
	path := strings.Split(firstNonEmpty(strings.TrimSpace(jsonPath), "version"), ".")
	quoted, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	// pathKey 是这个容器在父级里的键（根为空），childKey 是当前正在解析的值的键。
	// 两者必须分开：同一个对象既可能是别人的值，也要记录自己的子键。
	type frame struct {
		kind     byte
		pathKey  string
		childKey string
		index    int
	}
	var stack []frame
	decoder := json.NewDecoder(strings.NewReader(raw))
	matches := func(parent int) bool {
		if parent < 0 || len(stack) != len(path) {
			return false
		}
		for i := 1; i < len(stack); i++ {
			if stack[i].pathKey != path[i-1] {
				return false
			}
		}
		last := stack[parent]
		if last.kind == '[' {
			return strconv.Itoa(last.index) == path[len(path)-1]
		}
		return last.childKey == path[len(path)-1]
	}
	for {
		start := decoder.InputOffset()
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		parent := len(stack) - 1
		switch t := token.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				child := frame{kind: byte(t)}
				if parent >= 0 {
					if stack[parent].kind == '{' {
						child.pathKey = stack[parent].childKey
						stack[parent].childKey = ""
					} else {
						child.pathKey = strconv.Itoa(stack[parent].index)
					}
				}
				stack = append(stack, child)
			case '}', ']':
				stack = stack[:len(stack)-1]
				if len(stack) > 0 && stack[len(stack)-1].kind == '[' {
					stack[len(stack)-1].index++
				}
			}
		case string:
			if parent >= 0 && stack[parent].kind == '{' && stack[parent].childKey == "" {
				stack[parent].childKey = t
				continue
			}
			if !matches(parent) {
				if parent >= 0 {
					if stack[parent].kind == '[' {
						stack[parent].index++
					} else {
						stack[parent].childKey = ""
					}
				}
				continue
			}
			end := decoder.InputOffset()
			segment := raw[start:end]
			offset := strings.IndexByte(segment, '"')
			if offset < 0 {
				return "", fmt.Errorf("字段 %s 不是字符串", jsonPath)
			}
			return raw[:start+int64(offset)] + string(quoted) + raw[end:], nil
		default:
			if parent >= 0 {
				if stack[parent].kind == '[' {
					stack[parent].index++
				} else {
					stack[parent].childKey = ""
				}
			}
		}
	}
	return "", fmt.Errorf("JSON 里没有找到 %s", jsonPath)
}

// releaseVersionPrep 记录一次版本写入的结果，供建单后写入运行事件。
type releaseVersionPrep struct {
	Version string   `json:"version"`
	Tag     string   `json:"tag"`
	Commit  string   `json:"commit"`
	Files   []string `json:"files"`
	Commits []string `json:"commits,omitempty"`
}

// prepareReleaseVersion 把目标版本写进项目配置的版本文件，提交到主干并打 tag。
// 每次发布都从分支最新提交出发，保证构建用的是最新代码；重复版本会被拦下。
func (a *API) prepareReleaseVersion(ctx context.Context, project *store.Project, cfg *releasev2.ProjectReleaseConfig, version string) (*releaseVersionPrep, error) {
	if project == nil {
		return nil, fmt.Errorf("项目不存在")
	}
	version, err := releasev2.NormalizeVersion(version)
	if err != nil {
		return nil, err
	}
	repo := firstNonEmpty(project.GitHubRepoURL, project.GitHubIssueRepo)
	if repo == "" {
		return nil, fmt.Errorf("项目未配置 GitHub 仓库，无法提交版本号")
	}
	branch := firstNonEmpty(project.GitHubDefaultBranch, "main")
	files := releaseVersionFiles(cfg)
	if len(files) == 0 {
		return nil, fmt.Errorf("项目发版配置里没有填写版本文件，无法写入版本号")
	}
	client, err := bridge.ClientForRepo(ctx, a.store, &project.ID, repo)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fmt.Errorf("没有可用的 GitHub 凭据，无法提交版本号")
	}
	tag := "v" + version
	head, err := client.BranchSHA(ctx, repo, branch)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 最新提交: %w", branch, err)
	}
	if existing, err := client.TagSHA(ctx, repo, tag); err != nil {
		return nil, err
	} else if existing != "" && existing != head {
		return nil, fmt.Errorf("tag %s 已存在并指向 %s，不是 %s 的最新提交 %s；请换一个更大的版本号", tag, short(existing), branch, short(head))
	}
	prep := &releaseVersionPrep{Version: version, Tag: tag, Commit: head, Files: files}
	for _, path := range files {
		content, blob, ok, err := client.ReadFileWithSHA(ctx, repo, head, path)
		if err != nil {
			return nil, fmt.Errorf("读取版本文件 %s: %w", path, err)
		}
		if !ok {
			return nil, fmt.Errorf("版本文件 %s 在 %s 分支上不存在", path, branch)
		}
		updated, err := setJSONString(content, cfg.VersionJSONPath, version)
		if err != nil {
			return nil, fmt.Errorf("更新版本文件 %s: %w", path, err)
		}
		if updated == content {
			continue
		}
		sha, err := client.CommitFile(ctx, repo, branch, path, "chore(release): bump "+version, updated, blob)
		if err != nil {
			return nil, fmt.Errorf("提交版本文件 %s: %w", path, err)
		}
		prep.Commits = append(prep.Commits, sha)
	}
	if len(prep.Commits) > 0 {
		if head, err = client.BranchSHA(ctx, repo, branch); err != nil {
			return nil, fmt.Errorf("读取提交后的 %s: %w", branch, err)
		}
		prep.Commit = head
	}
	existing, err := client.TagSHA(ctx, repo, tag)
	if err != nil {
		return nil, err
	}
	switch {
	case existing == "":
		if err := client.CreateTagRef(ctx, repo, tag, prep.Commit); err != nil {
			return nil, fmt.Errorf("创建 tag %s: %w", tag, err)
		}
	case existing != prep.Commit:
		return nil, fmt.Errorf("tag %s 指向 %s，与 %s 最新提交 %s 不一致", tag, short(existing), branch, short(prep.Commit))
	}
	return prep, nil
}
