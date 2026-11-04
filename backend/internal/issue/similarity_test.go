package issue

import "testing"

func TestContainmentIdentical(t *testing.T) {
	if got := Containment("登录失败", "登录失败"); got != 1 {
		t.Fatalf("identical = %v", got)
	}
}

func TestContainmentShortInLong(t *testing.T) {
	score := Containment("登录失败", "用户反馈：登录页面点击登录按钮后提示登录失败，无法进入首页")
	if score < 0.5 {
		t.Fatalf("short-in-long containment too low: %v", score)
	}
}

func TestContainmentUnrelated(t *testing.T) {
	if got := Containment("导出报表乱码", "登录失败"); got > 0.2 {
		t.Fatalf("unrelated too high: %v", got)
	}
}
