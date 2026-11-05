package bridge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"smartpipe/backend/internal/store"
)

// WebhookHandler 接收 GitHub Webhook：验签、去重、记录事件。
//
// 事件到 lead 的路由在 M1 下一阶段接入；这里先把可靠性打好：
// 同一个 X-GitHub-Delivery 只处理一次，签名不对一律 401。
type WebhookHandler struct {
	Store  *store.Store
	Secret string
	Now    func() time.Time
	// Approvals 处理 issue_comment 里的 /plan 与 /approve 命令。
	Approvals *ApprovalService
}

type webhookPayload struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Issue struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
	} `json:"issue"`
	Comment struct {
		ID                int64  `json:"id"`
		Body              string `json:"body"`
		AuthorAssociation string `json:"author_association"`
		User              struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"comment"`
}

func (h *WebhookHandler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(h.Secret) != "" {
		if !VerifySignature(h.Secret, body, r.Header.Get("X-Hub-Signature-256")) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	event := strings.TrimSpace(r.Header.Get("X-GitHub-Event"))
	delivery := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	if delivery == "" {
		sum := sha256.Sum256(body)
		delivery = "local-" + hex.EncodeToString(sum[:8])
	}

	var payload webhookPayload
	_ = json.Unmarshal(body, &payload)
	repo := payload.Repository.FullName

	ctx := r.Context()
	if h.Store != nil {
		if err := h.Store.RecordWebhookDelivery(ctx, delivery, event, repo); err != nil {
			if errors.Is(err, store.ErrDuplicate) {
				writeJSON(w, http.StatusAccepted, map[string]any{"status": "duplicate", "delivery_id": delivery})
				return
			}
			http.Error(w, "cannot record delivery", http.StatusInternalServerError)
			return
		}
		_ = h.Store.AppendAudit(ctx, store.AuditEntry{
			Actor:      "github-webhook",
			Action:     "webhook." + event,
			TargetType: "issue",
			TargetID:   repo,
			Result:     "ok",
			TraceID:    delivery,
			Detail: map[string]any{
				"action":       payload.Action,
				"issue_number": payload.Issue.Number,
				"comment_user": payload.Comment.User.Login,
			},
		})
	}

	resp := map[string]any{
		"status":      "accepted",
		"event":       event,
		"delivery_id": delivery,
		"action":      payload.Action,
		"issue":       payload.Issue.Number,
	}
	if event == "issue_comment" && h.Approvals != nil && (payload.Action == "created" || payload.Action == "edited") {
		outcome, approvalErr := h.Approvals.HandleComment(ctx, CommentEvent{
			Repo:         repo,
			IssueNumber:  payload.Issue.Number,
			CommentID:    payload.Comment.ID,
			Body:         payload.Comment.Body,
			User:         payload.Comment.User.Login,
			Association:  payload.Comment.AuthorAssociation,
			IssueHTMLURL: payload.Issue.HTMLURL,
		})
		resp["approval"] = outcome
		if approvalErr != nil {
			resp["approval_error"] = approvalErr.Error()
		}
	}
	writeJSON(w, http.StatusAccepted, resp)
}

// VerifySignature 校验 X-Hub-Signature-256（sha256=hex）。
func VerifySignature(secret string, body []byte, header string) bool {
	header = strings.TrimSpace(header)
	if header == "" || !strings.HasPrefix(header, "sha256=") {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}

// Sign 生成用于测试或本地联调的签名头。
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
