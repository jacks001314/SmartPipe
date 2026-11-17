package scheduler

import "testing"

func TestEscapeControlCharsInJSONStrings(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "raw newline and tab inside string",
			in:   "{\"reply\":\"a\nb\tc\"}",
			want: `{"reply":"a\nb\tc"}`,
		},
		{
			name: "other control chars are escaped as unicode",
			in:   "{\"reply\":\"a\x01b\"}",
			want: `{"reply":"a\u0001b"}`,
		},
		{
			name: "escaped quote and backslash are preserved",
			in:   "{\"reply\":\"a\\\"b\\\\c\"}",
			want: `{"reply":"a\"b\\c"}`,
		},
		{
			name: "newlines outside strings are untouched",
			in:   "{\n  \"reply\": \"ok\"\n}",
			want: "{\n  \"reply\": \"ok\"\n}",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := escapeControlCharsInJSONStrings(c.in); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestParseChatDecisionRepairsRawNewline(t *testing.T) {
	raw := "{\"action\":\"answer\",\"reply\":\"第一行\n第二行\"}"
	d, err := ParseChatDecision(raw)
	if err != nil {
		t.Fatalf("ParseChatDecision returned error: %v", err)
	}
	if d.Action != "answer" || d.Reply != "第一行\n第二行" {
		t.Fatalf("unexpected decision: %+v", d)
	}
}

func TestParseChatDecisionStillAcceptsEscapedNewline(t *testing.T) {
	raw := "```json\n{\"action\":\"answer\",\"reply\":\"第一行\\n第二行\"}\n```"
	d, err := ParseChatDecision(raw)
	if err != nil {
		t.Fatalf("ParseChatDecision returned error: %v", err)
	}
	if d.Reply != "第一行\n第二行" {
		t.Fatalf("unexpected reply: %q", d.Reply)
	}
}

func TestParseTriageDecisionRepairsRawNewline(t *testing.T) {
	raw := "{\"action\":\"answer\",\"reply\":\"line1\nline2\",\"reason\":\"多行\n原因\",\"confidence\":0.6}"
	d, err := ParseTriageDecision(raw)
	if err != nil {
		t.Fatalf("ParseTriageDecision returned error: %v", err)
	}
	if d.Reply != "line1\nline2" || d.Reason != "多行\n原因" {
		t.Fatalf("unexpected decision: %+v", d)
	}
	if d.Confidence != 0.6 {
		t.Fatalf("unexpected confidence: %v", d.Confidence)
	}
}
