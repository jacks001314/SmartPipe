package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// fetchSourceArchive 从控制面下载该运行固定提交的源码包并解到 dir。
//
// 构建机常在内网、连不上代码托管；clone 失败时走这条路，只需要能访问 SmartPipe。
func fetchSourceArchive(ctx context.Context, client *http.Client, cfg config, env *targetEnvelope, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	endpoint := strings.TrimRight(cfg.ServiceURL, "/") +
		"/internal/release-executors/" + url.PathEscape(cfg.ExecutorID) + "/source" +
		"?run_id=" + url.QueryEscape(env.Run.ID) +
		"&commit=" + url.QueryEscape(env.Run.ResolvedCommit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	tmp, err := os.CreateTemp("", "smartpipe-source-*.tar.gz")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := extractTarGz(f, dir); err != nil {
		return err
	}
	return writeSourceMetadata(dir, env)
}

// writeSourceMetadata 让构建脚本不依赖 .git 也能拿到本次发布的版本与提交。
func writeSourceMetadata(dir string, env *targetEnvelope) error {
	if env == nil || env.Run == nil {
		return nil
	}
	payload := map[string]any{
		"run_id": env.Run.ID, "version": env.Run.Version, "tag": env.Run.Tag,
		"build_version": env.Run.BuildVersion, "commit": env.Run.ResolvedCommit,
		"channel": env.Run.Channel,
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ".smartpipe-source.json"), append(raw, '\n'), 0o644)
}

// describeTopLevel 返回解包目录的前几项，构建失败时能直接从日志判断
// 归档层级是否按预期剥离，而不是只看到一句 cd 失败。
func describeTopLevel(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "unreadable: " + err.Error()
	}
	names := make([]string, 0, len(entries)+1)
	for i, entry := range entries {
		if i >= 12 {
			names = append(names, "...")
			break
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return "(empty)"
	}
	return strings.Join(names, " ")
}

// extractTarGz 解开归档，并挡住跳出目标目录的条目。
//
// GitHub 的 tarball 会自带一层 <owner>-<repo>-<sha>/ 根目录，而 git archive
// 直接从仓库根开始。这里统一剥掉唯一顶层目录，构建脚本看到的目录结构一致。
func extractTarGz(r io.ReadSeeker, dir string) error {
	prefix, err := archiveRootPrefix(r)
	if err != nil {
		return err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return err
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./")
		if prefix != "" {
			name = strings.TrimPrefix(name, prefix+"/")
		}
		if name == "" {
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(name))
		if clean == "." || clean == "" {
			continue
		}
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			return fmt.Errorf("归档里出现越界路径 %q", hdr.Name)
		}
		target := filepath.Join(dir, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(hdr.Mode) & 0o777
			if mode == 0 {
				mode = 0o644
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if strings.HasPrefix(hdr.Linkname, "/") {
				return fmt.Errorf("归档里的软链接指向绝对路径 %q", hdr.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}
}

// archiveRootPrefix 返回归档唯一的顶层目录；没有唯一顶层目录时返回空串。
func archiveRootPrefix(r io.Reader) (string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	top := ""
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		// GitHub tarball 会带一条 pax_global_header（type 'g'），它没有目录层级。
		// 把它当成“仓库根有文件”会让唯一顶层目录不被剥离，构建脚本随后找不到
		// 子目录，所以这里只统计真实文件/目录条目。
		if hdr.Typeflag == tar.TypeXGlobalHeader || hdr.Typeflag == tar.TypeXHeader {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./"), "/")
		if name == "" || name == "." {
			continue
		}
		idx := strings.IndexByte(name, '/')
		if idx < 0 {
			// 归档里的顶层目录条目本身（type dir）不算“仓库根有文件”；
			// 只有真正的根级文件才说明这不是带前缀的 tarball。
			if hdr.Typeflag != tar.TypeDir {
				return "", nil
			}
			if top == "" {
				top = name
				continue
			}
			if top != name {
				return "", nil
			}
			continue
		}
		segment := name[:idx]
		if top == "" {
			top = segment
			continue
		}
		if top != segment {
			return "", nil
		}
	}
	return top, nil
}
