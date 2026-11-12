// Command update-service-stub is a development stand-in for syntropy_update's
// two phase admin API: stage creates an invisible import, promote performs a
// compare-and-swap on the channel pointer.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type slot struct {
	Slot   string `json:"slot"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	State  string `json:"state,omitempty"`
}

type record struct {
	ID             string    `json:"id"`
	State          string    `json:"state"`
	Product        string    `json:"product"`
	Channel        string    `json:"channel"`
	ProductVersion string    `json:"productVersion"`
	BuildVersion   string    `json:"buildVersion"`
	BOMSHA256      string    `json:"bomSha256"`
	BundleSHA256   string    `json:"bundleSha256"`
	Slots          []slot    `json:"slots"`
	CreatedAt      time.Time `json:"createdAt"`
}

type pointer struct {
	Channel        string    `json:"channel"`
	ProductVersion string    `json:"productVersion"`
	BuildVersion   string    `json:"buildVersion"`
	ReleaseID      string    `json:"releaseId"`
	BOMSHA256      string    `json:"bomSha256"`
	PromotedAt     time.Time `json:"promotedAt"`
}

type stub struct {
	mu     sync.Mutex
	path   string
	token  string
	client *http.Client
	recs   map[string]*record
	chans  map[string]pointer
	idem   map[string]string
}

func newStub(path, token string) *stub {
	s := &stub{path: path, token: token, client: &http.Client{Timeout: 2 * time.Minute},
		recs: map[string]*record{}, chans: map[string]pointer{}, idem: map[string]string{}}
	if path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			data := struct {
				Imports  map[string]*record `json:"imports"`
				Channels map[string]pointer `json:"channels"`
				Idem     map[string]string  `json:"idempotency"`
			}{}
			if json.Unmarshal(raw, &data) == nil {
				if data.Imports != nil {
					s.recs = data.Imports
				}
				if data.Channels != nil {
					s.chans = data.Channels
				}
				if data.Idem != nil {
					s.idem = data.Idem
				}
			}
		}
	}
	return s
}

func (s *stub) persist() {
	if s.path == "" {
		return
	}
	raw, err := json.MarshalIndent(map[string]any{"imports": s.recs, "channels": s.chans, "idempotency": s.idem}, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.path+".tmp", raw, 0o644)
	_ = os.Rename(s.path+".tmp", s.path)
}

func (s *stub) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" && strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) != s.token {
			reply(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func (s *stub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusOK, map[string]any{"status": "ok", "service": "update-service-stub"})
	})
	mux.HandleFunc("/v1/admin/update-imports", s.auth(s.stage))
	mux.HandleFunc("/v1/admin/update-imports/", s.auth(s.importAction))
	mux.HandleFunc("/v1/distributions/latest", s.auth(s.latest))
	return mux
}

func (s *stub) stage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Product        string          `json:"product"`
		Channel        string          `json:"channel"`
		ProductVersion string          `json:"productVersion"`
		BuildVersion   string          `json:"buildVersion"`
		BundleSHA256   string          `json:"bundleSha256"`
		BundleURL      string          `json:"bundleUrl"`
		Bundle         json.RawMessage `json:"bundle"`
		IdempotencyKey string          `json:"idempotencyKey"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&in); err != nil {
		reply(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if in.Product == "" || in.Channel == "" || in.ProductVersion == "" || in.BundleSHA256 == "" {
		reply(w, http.StatusBadRequest, map[string]any{"error": "product, channel, productVersion, bundleSha256 required"})
		return
	}
	s.mu.Lock()
	if id := s.idem["stage:"+in.IdempotencyKey]; id != "" && in.IdempotencyKey != "" {
		rec := s.recs[id]
		s.mu.Unlock()
		if rec == nil {
			reply(w, http.StatusConflict, map[string]any{"error": "idempotent import missing"})
			return
		}
		reply(w, http.StatusOK, rec)
		return
	}
	s.mu.Unlock()

	raw, err := s.bundleBytes(r.Context(), in.BundleURL, in.Bundle)
	if err != nil {
		reply(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	sum := sha256.Sum256(raw)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if digest != in.BundleSHA256 {
		reply(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("bundle digest mismatch: got %s want %s", digest, in.BundleSHA256)})
		return
	}
	slots, err := bundleSlots(raw)
	if err != nil {
		reply(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	rec := &record{
		ID: fmt.Sprintf("imp_%d", time.Now().UnixNano()), State: "staged", Product: in.Product,
		Channel: in.Channel, ProductVersion: in.ProductVersion, BuildVersion: in.BuildVersion,
		BOMSHA256: digest, BundleSHA256: digest, Slots: slots, CreatedAt: time.Now().UTC(),
	}
	s.recs[rec.ID] = rec
	if in.IdempotencyKey != "" {
		s.idem["stage:"+in.IdempotencyKey] = rec.ID
	}
	s.persist()
	reply(w, http.StatusOK, rec)
}

func (s *stub) bundleBytes(ctx context.Context, url string, inline json.RawMessage) ([]byte, error) {
	if strings.TrimSpace(url) != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := s.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("download bundle: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("download bundle: HTTP %d", resp.StatusCode)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	}
	if len(inline) == 0 {
		return nil, errors.New("bundle or bundleUrl is required")
	}
	return inline, nil
}

func bundleSlots(raw []byte) ([]slot, error) {
	var doc struct {
		Components map[string]struct {
			Slots []slot `json:"slots"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("bundle is not valid json: %w", err)
	}
	names := make([]string, 0, len(doc.Components))
	for name := range doc.Components {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []slot
	seen := map[string]bool{}
	for _, name := range names {
		for _, sl := range doc.Components[name].Slots {
			if sl.Slot == "" || sl.OS == "" || sl.Arch == "" || sl.Name == "" || sl.SHA256 == "" {
				return nil, fmt.Errorf("component %s has an incomplete slot", name)
			}
			key := sl.Slot + "|" + sl.OS + "|" + sl.Arch
			if seen[key] {
				return nil, fmt.Errorf("duplicate slot %s", key)
			}
			seen[key] = true
			sl.State = "verified"
			out = append(out, sl)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("bundle has no slots")
	}
	return out, nil
}

func (s *stub) importAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/admin/update-imports/"), "/"), "/")
	s.mu.Lock()
	rec := s.recs[parts[0]]
	s.mu.Unlock()
	if rec == nil {
		reply(w, http.StatusNotFound, map[string]any{"error": "import not found"})
		return
	}
	if len(parts) == 1 {
		reply(w, http.StatusOK, rec)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch parts[1] {
	case "cancel":
		s.mu.Lock()
		rec.State = "cancelled"
		s.persist()
		s.mu.Unlock()
		reply(w, http.StatusOK, rec)
	case "promote":
		s.promote(w, r, rec)
	default:
		http.NotFound(w, r)
	}
}

func (s *stub) promote(w http.ResponseWriter, r *http.Request, rec *record) {
	var in struct {
		ExpectedCurrentProductVersion string `json:"expectedCurrentProductVersion"`
		ExpectedBOMSHA256             string `json:"expectedBomSha256"`
		IdempotencyKey                string `json:"idempotencyKey"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		reply(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.State == "promoted" {
		if id := s.idem["promote:"+in.IdempotencyKey]; id == rec.ID && in.IdempotencyKey != "" {
			p := s.chans[rec.Channel]
			reply(w, http.StatusOK, map[string]any{"state": "promoted", "channel": p.Channel, "productVersion": p.ProductVersion,
				"releaseId": p.ReleaseID, "bomSha256": p.BOMSHA256, "promotedAt": p.PromotedAt})
			return
		}
	}
	if rec.State != "staged" {
		reply(w, http.StatusConflict, map[string]any{"error": "import is " + rec.State})
		return
	}
	cur := s.chans[rec.Channel]
	if in.ExpectedBOMSHA256 != "" && cur.BOMSHA256 != in.ExpectedBOMSHA256 {
		reply(w, http.StatusConflict, map[string]any{"error": fmt.Sprintf("channel moved: bom %q != expected %q", cur.BOMSHA256, in.ExpectedBOMSHA256)})
		return
	}
	if in.ExpectedBOMSHA256 == "" && in.ExpectedCurrentProductVersion != "" && cur.ProductVersion != in.ExpectedCurrentProductVersion {
		reply(w, http.StatusConflict, map[string]any{"error": fmt.Sprintf("channel version %q != expected %q", cur.ProductVersion, in.ExpectedCurrentProductVersion)})
		return
	}
	pointer := pointer{Channel: rec.Channel, ProductVersion: rec.ProductVersion, BuildVersion: rec.BuildVersion,
		ReleaseID: rec.ID, BOMSHA256: rec.BOMSHA256, PromotedAt: time.Now().UTC()}
	s.chans[rec.Channel] = pointer
	rec.State = "promoted"
	if in.IdempotencyKey != "" {
		s.idem["promote:"+in.IdempotencyKey] = rec.ID
	}
	s.persist()
	reply(w, http.StatusOK, map[string]any{"state": "promoted", "channel": pointer.Channel, "productVersion": pointer.ProductVersion,
		"releaseId": pointer.ReleaseID, "bomSha256": pointer.BOMSHA256, "promotedAt": pointer.PromotedAt})
}

func (s *stub) latest(w http.ResponseWriter, r *http.Request) {
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	if channel == "" {
		channel = "stable"
	}
	s.mu.Lock()
	p := s.chans[channel]
	s.mu.Unlock()
	if p.Channel == "" {
		reply(w, http.StatusNotFound, map[string]any{"error": "channel has no release"})
		return
	}
	reply(w, http.StatusOK, p)
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func main() {
	addr := flag.String("addr", ":18100", "listen address")
	statePath := flag.String("state", "/var/lib/smartpipe-v2/update-stub.json", "state file")
	flag.Parse()
	token := strings.TrimSpace(os.Getenv("STUB_TOKEN"))
	s := newStub(*statePath, token)
	log.Printf("update-service-stub on %s (state %s, auth %v)", *addr, *statePath, token != "")
	srv := &http.Server{Addr: *addr, Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
