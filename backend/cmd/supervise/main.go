// supervise 在一个容器里跑多个服务进程：任一进程退出就按退避重启，
// 连续失败超过阈值时让容器退出，交给 restart policy 处理。
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	maxBurstFailures = 5
	burstWindow      = time.Minute
	restartDelay     = 2 * time.Second
)

type prefixWriter struct {
	prefix string
}

var outMu sync.Mutex

func (w prefixWriter) Write(p []byte) (int, error) {
	outMu.Lock()
	defer outMu.Unlock()
	var b strings.Builder
	for _, line := range strings.SplitAfter(string(p), "\n") {
		if line == "" {
			continue
		}
		b.WriteString("[" + w.prefix + "] " + line)
	}
	if _, err := os.Stdout.WriteString(b.String()); err != nil {
		return 0, err
	}
	return len(p), nil
}

func supervise(ctx context.Context, name string) {
	failures := 0
	var lastFailure time.Time
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, name)
		cmd.Env = os.Environ()
		cmd.Stdout = prefixWriter{prefix: name}
		cmd.Stderr = prefixWriter{prefix: name}
		if err := cmd.Start(); err != nil {
			log.Printf("[supervise] start %s: %v", name, err)
		} else {
			log.Printf("[supervise] %s started (pid %d)", name, cmd.Process.Pid)
			err := cmd.Wait()
			if ctx.Err() != nil {
				return
			}
			log.Printf("[supervise] %s exited: %v", name, err)
		}
		now := time.Now()
		if now.Sub(lastFailure) > burstWindow {
			failures = 0
		}
		failures++
		lastFailure = now
		if failures > maxBurstFailures {
			log.Printf("[supervise] %s failed %d times in %s, exiting", name, failures, burstWindow)
			os.Exit(1)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(restartDelay):
		}
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: supervise <command> [command...]")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var wg sync.WaitGroup
	for _, name := range os.Args[1:] {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			supervise(ctx, name)
		}(name)
	}
	wg.Wait()
}
