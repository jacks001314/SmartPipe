package resourceprobe

import (
	"strings"
	"testing"
)

func TestParseSystemInfo(t *testing.T) {
	output := strings.Join([]string{
		"hostname\tbuild-01",
		"os\tUbuntu 24.04.2 LTS",
		"kernel\t6.8.0-55-generic",
		"architecture\tx86_64",
		"cpu_model\tAMD EPYC 7B13 64-Core Processor",
		"cpu_count\t32",
		"memory_total_kb\t131072000",
		"disk_total_kb\t1048576000",
		"disk_available_kb\t524288000",
		"uptime_seconds\t172800",
		"login_user\tbuilder",
		"home\t/home/builder",
		"tool_docker\t28.3.2",
		"tool_git\tgit version 2.43.0",
		"tool_go\tgo version go1.26.10 linux/amd64",
		"gpu\tNVIDIA L40S, 46068, 570.133.20",
	}, "\n")
	info := ParseSystemInfo(output)
	if info["hostname"] != "build-01" || info["architecture"] != "x86_64" {
		t.Fatalf("basic fields missing: %#v", info)
	}
	if info["cpu_count"] != 32 {
		t.Fatalf("cpu count = %#v", info["cpu_count"])
	}
	if info["memory_total_bytes"] != uint64(131072000*1024) {
		t.Fatalf("memory total = %#v", info["memory_total_bytes"])
	}
	tools, ok := info["tools"].(map[string]string)
	if !ok || tools["docker"] != "28.3.2" {
		t.Fatalf("tools = %#v", info["tools"])
	}
	gpus, ok := info["gpus"].([]map[string]any)
	if !ok || len(gpus) != 1 || gpus[0]["name"] != "NVIDIA L40S" {
		t.Fatalf("gpus = %#v", info["gpus"])
	}
}

func TestParseSystemInfoRejectsEmptyProfile(t *testing.T) {
	info := ParseSystemInfo("not a probe response\nunknown\tvalue")
	if len(info) != 0 {
		t.Fatalf("expected empty profile, got %#v", info)
	}
}
