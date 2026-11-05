package crash

import (
	"strings"
	"testing"
	"time"
)

func sampleBundle() Bundle {
	return Bundle{
		App: "desktop-client", Version: "4.2.1", Build: "20261101.3",
		OS: "Windows 11 23H2", Arch: "amd64", UserID: "u_1", DeviceID: "d_1",
		OccurredAt: "2026-11-05T10:00:00Z",
		Exception: Exception{
			Type:    "NullReferenceException",
			Message: "Object reference not set",
			Stack:   "at Login.Form.Show() in C:\\work\\src\\Login.cs:line 42\nat Login.Main() in C:\\work\\src\\Main.cs:line 7",
		},
		Logs:     []string{"open login", "token=abcdef123456", "contact alice@example.com"},
		Metadata: map[string]string{"module": "login", "home": "C:\\Users\\alice\\AppData"},
	}
}

func TestNormalizeRedactsSecretsAndPaths(t *testing.T) {
	b := sampleBundle()
	b.Exception.Message = "auth failed with ghp_ABCDEFGHIJKLMNOPQRSTUVWX for alice@example.com"
	b.Logs = append(b.Logs, "cookie: session=abc123456")
	n, err := b.Normalize(time.Now())
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	all := n.Bundle.Exception.Message + strings.Join(n.Bundle.Logs, "\n") + n.Bundle.Metadata["home"]
	for _, secret := range []string{"ghp_ABCDEFGHIJKLMNOPQRSTUVWX", "alice@example.com", `C:\Users\alice`, "session=abc123456"} {
		if strings.Contains(all, secret) {
			t.Fatalf("secret %q not redacted: %s", secret, all)
		}
	}
	if !strings.Contains(all, "[REDACTED]") {
		t.Fatalf("expected redaction marker: %s", all)
	}
}

func TestFingerprintStableAcrossAddresses(t *testing.T) {
	a := sampleBundle()
	b := sampleBundle()
	b.Exception.Stack = "at Login.Form.Show() in C:\\work\\src\\Login.cs:line 99\nat Login.Main() in C:\\work\\src\\Main.cs:line 123"
	if ComputeFingerprint(a) != ComputeFingerprint(b) {
		t.Fatalf("fingerprint should ignore line numbers: %s vs %s", ComputeFingerprint(a), ComputeFingerprint(b))
	}
	c := sampleBundle()
	c.Exception.Type = "InvalidOperationException"
	if ComputeFingerprint(a) == ComputeFingerprint(c) {
		t.Fatal("different exception type must change fingerprint")
	}
}

func TestNormalizeValidation(t *testing.T) {
	if _, err := (&Bundle{Exception: Exception{Message: "x"}}).Normalize(time.Now()); err == nil {
		t.Fatal("missing app must fail")
	}
	if _, err := (&Bundle{App: "x"}).Normalize(time.Now()); err == nil {
		t.Fatal("missing exception must fail")
	}
	b := sampleBundle()
	for i := 0; i < 300; i++ {
		b.Logs = append(b.Logs, "line "+strings.Repeat("x", 10))
	}
	n, err := b.Normalize(time.Now())
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(n.Bundle.Logs) != MaxLogs {
		t.Fatalf("logs = %d, want %d", len(n.Bundle.Logs), MaxLogs)
	}
	if !strings.HasPrefix(n.Bundle.Logs[len(n.Bundle.Logs)-1], "line ") {
		t.Fatalf("logs should keep the tail: %q", n.Bundle.Logs[0])
	}
	long := sampleBundle()
	long.Exception.Message = strings.Repeat("m", MaxMessageBytes+500)
	n, err = long.Normalize(time.Now())
	if err != nil {
		t.Fatalf("normalize long: %v", err)
	}
	if len(n.Bundle.Exception.Message) > MaxMessageBytes+32 {
		t.Fatalf("message not truncated: %d", len(n.Bundle.Exception.Message))
	}
}

func TestTitleAndBody(t *testing.T) {
	b := sampleBundle()
	n, err := b.Normalize(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	title := Title(n.Bundle)
	if !strings.HasPrefix(title, "[crash] desktop-client NullReferenceException") || len(title) > 120 {
		t.Fatalf("title = %q", title)
	}
	body := Body(*n, "lanxin", 7, 1, time.Now())
	for _, want := range []string{"## 崩溃摘要", "desktop-client 4.2.1", n.Fingerprint, "crash_report #7", "## 异常", "## 最近日志", "## 处理说明"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	dup := DupComment(*n, 3, 7, time.Now())
	if !strings.Contains(dup, "第 3 次") || !strings.Contains(dup, n.Fingerprint) {
		t.Fatalf("dup comment = %q", dup)
	}
}
