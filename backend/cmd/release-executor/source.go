package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
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
	return extractTarGz(f, dir)
}

// extractTarGz 解开归档，并挡住跳出目标目录的条目。
func extractTarGz(r io.Reader, dir string) error {
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
		clean := filepath.Clean(strings.TrimPrefix(hdr.Name, "./"))
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
