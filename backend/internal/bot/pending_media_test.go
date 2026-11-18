package bot

import (
	"testing"
	"time"

	"smartpipe/sdks/lanxin"
)

func TestRememberAndTakeRecentImages(t *testing.T) {
	h := NewHandler(Config{}, nil, nil)
	atts := []Attachment{
		{Name: "a.png", MimeType: "image/png", Data: []byte{1}},
		{Name: "b.txt", MimeType: "text/plain", Data: []byte{2}},
		{Name: "c.jpg", MimeType: "image/jpeg", Data: []byte{3}},
	}
	h.rememberRecentImages("chat-1", "user-1", atts)

	got := h.takeRecentImages("chat-1", "user-1", time.Now())
	if len(got) != 2 || got[0].Name != "a.png" || got[1].Name != "c.jpg" {
		t.Fatalf("unexpected images: %+v", got)
	}
	if again := h.takeRecentImages("chat-1", "user-1", time.Now()); again != nil {
		t.Fatalf("expected cache to be consumed, got %+v", again)
	}
}

func TestRecentImagesExpire(t *testing.T) {
	h := NewHandler(Config{}, nil, nil)
	h.rememberRecentImages("chat-1", "user-1", []Attachment{{Name: "a.png", MimeType: "image/png", Data: []byte{1}}})
	got := h.takeRecentImages("chat-1", "user-1", time.Now().Add(pendingMediaTTL+time.Second))
	if got != nil {
		t.Fatalf("expected expired cache, got %+v", got)
	}
}

func TestRememberMergesImages(t *testing.T) {
	h := NewHandler(Config{}, nil, nil)
	h.rememberRecentImages("chat-1", "user-1", []Attachment{{Name: "a.png", MimeType: "image/png", Data: []byte{1}}})
	h.rememberRecentImages("chat-1", "user-1", []Attachment{{Name: "b.png", MimeType: "image/png", Data: []byte{2}}})
	got := h.takeRecentImages("chat-1", "user-1", time.Now())
	if len(got) != 2 {
		t.Fatalf("expected merged images, got %+v", got)
	}
}

func TestRecentImagesSkippedForCommands(t *testing.T) {
	h := NewHandler(Config{}, nil, nil)
	h.rememberRecentImages("chat-1", "user-1", []Attachment{{Name: "a.png", MimeType: "image/png", Data: []byte{1}}})
	m := &lanxin.InboundMessage{GroupID: "chat-1", From: "user-1"}
	if got := h.recentImages(m, true); got != nil {
		t.Fatalf("commands should not carry images, got %+v", got)
	}
	if got := h.recentImages(m, false); len(got) != 1 {
		t.Fatalf("expected cached image, got %+v", got)
	}
}
