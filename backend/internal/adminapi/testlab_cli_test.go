package adminapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"smartpipe/backend/internal/releasev2"
	"smartpipe/backend/internal/store"
	"smartpipe/backend/internal/testlab"
)

// Runs the actual CLI and CommandScenario, with a pinned fixture script.
// This verifies the execution pipeline, not an installed desktop product.
func TestDesktopCommandCLI(t *testing.T) {
	cli := os.Getenv("SMARTPIPE_WORKER_CLI_JS")
	if cli == "" {
		t.Skip("SMARTPIPE_WORKER_CLI_JS must point to built worker-cli.js")
	}
	if !filepath.IsAbs(cli) {
		t.Fatal("CLI path must be absolute")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	node, err = filepath.Abs(node)
	if err != nil {
		t.Fatal(err)
	}
	app, st := newTestAPI(t, "")
	ctx := t.Context()
	p, err := st.CreateProject(ctx, &store.Project{Name: "command-cli"})
	if err != nil {
		t.Fatal(err)
	}
	const machine = "command-cli-machine"
	const runID = "command-cli-run"
	platform := runtime.GOOS + "-" + runtime.GOARCH
	token := strings.Repeat("k", 64)
	if err = st.RegisterDesktopMachine(ctx, machine, platform, testlab.TokenDigest(token)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.DB().Exec("DELETE FROM desktop_test_runs WHERE id=$1", runID)
		_, _ = st.DB().Exec("DELETE FROM desktop_test_machines WHERE id=$1", machine)
		_, _ = st.DB().Exec("DELETE FROM desktop_gate_policies WHERE project_id=$1", p.ID)
	})
	root := t.TempDir()
	write := func(name string, b []byte) string {
		t.Helper()
		path := filepath.Join(root, name)
		if e := os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
		return path
	}
	digest := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	artifact := []byte("command CLI integration artifact")
	artifactPath := write("artifact.bin", artifact)
	script := []byte(`import {writeFileSync,readFileSync,unlinkSync} from 'node:fs';
const phase=process.argv[2];
if(phase==='setup')writeFileSync('fixture.active','ready');
else if(phase==='check'){
 if(readFileSync('fixture.active','utf8')!=='ready')throw Error('setup missing');
 if(readFileSync(process.env.SMARTPIPE_TEST_ARTIFACT,'utf8')!=='command CLI integration artifact')throw Error('artifact mismatch');
 if(process.env.SMARTPIPE_TEST_MACHINE_TOKEN)throw Error('token leaked to command');
 console.log('artifact verified');
}else if(phase==='cleanup'){unlinkSync('fixture.active');writeFileSync('fixture.cleaned','done');}
else throw Error('invalid phase');
`)
	scriptPath := write("scenario.mjs", script)
	spec := testlab.Spec{ProjectID: p.ID, SourceSHA: strings.Repeat("a", 40), ArtifactSHA256: digest(artifact), Scenario: "command-cli-fixture", ScenarioVersion: "1", Platform: platform, RequiredChecks: []string{"artifact_check"}}
	policy := store.DesktopGatePolicy{Scenario: spec.Scenario, Version: "1", Platform: platform, ArtifactRole: "installer", RequiredChecks: spec.RequiredChecks}
	if err = st.SetDesktopGatePolicies(ctx, p.ID, []store.DesktopGatePolicy{policy}); err != nil {
		t.Fatal(err)
	}
	release := &releasev2.Run{ProjectID: p.ID, ResolvedCommit: spec.SourceSHA}
	targets := []releasev2.Target{{TargetID: "native", OS: runtime.GOOS, Arch: runtime.GOARCH}}
	artifacts := []releasev2.Artifact{{TargetID: "native", Role: "installer", SHA256: "sha256:" + spec.ArtifactSHA256, Name: "artifact.bin"}}
	if st.CheckDesktopReleaseGate(ctx, release, targets, artifacts) == nil {
		t.Fatal("gate passed before run")
	}
	if _, err = st.CreateDesktopTestRun(ctx, runID, spec); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app)
	defer server.Close()
	step := func(id, phase string) any {
		return map[string]any{"id": id, "executable": node, "args": []string{scriptPath, phase}, "timeoutMs": 5000}
	}
	config := map[string]any{"apiURL": server.URL, "machineID": machine, "commandScenario": map[string]any{
		"name": spec.Scenario, "version": "1", "stateRoot": filepath.Join(root, "state"), "artifactPath": artifactPath, "workingDirectory": root,
		"scripts": []any{map[string]any{"path": scriptPath, "sha256": digest(script)}},
		"setup":   []any{step("setup", "setup")}, "checks": []any{step("artifact_check", "check")}, "cleanup": []any{step("cleanup", "cleanup")},
	}}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configPath := write("config.json", raw)
	invoke := func(want string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, node, cli, configPath)
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "SMARTPIPE_TEST_MACHINE_TOKEN=") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		cmd.Env = append(cmd.Env, "SMARTPIPE_TEST_MACHINE_TOKEN="+token)
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("CLI: %v: %s", e, out)
		}
		if strings.TrimSpace(string(out)) != want {
			t.Fatalf("CLI output: %s", out)
		}
	}
	invoke(`{"status":"reported"}`)
	r, err := st.GetDesktopTestRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "PASS" {
		t.Fatalf("run status %s", r.Status)
	}
	if _, err = os.Stat(filepath.Join(root, "fixture.cleaned")); err != nil {
		t.Fatal("cleanup did not execute", err)
	}
	for _, path := range []string{filepath.Join(root, "fixture.active"), filepath.Join(root, "state", "desktop.lock")} {
		if _, e := os.Stat(path); !os.IsNotExist(e) {
			t.Fatalf("cleanup left %s: %v", path, e)
		}
	}
	var busy string
	if err = st.DB().QueryRow("SELECT run_id FROM desktop_test_machines WHERE id=$1", machine).Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if busy != "" {
		t.Fatal("machine still busy")
	}
	// Read locally recorded outcomes and fetch the same bytes through admin HTTP.
	files, err := filepath.Glob(filepath.Join(root, "state", "runs", "*", "checks.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("outcomes: %v %v", files, err)
	}
	outcomes, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		ID       string `json:"id"`
		ExitCode int    `json:"exitCode"`
		Stdout   string `json:"stdout"`
	}
	if err = json.Unmarshal(outcomes, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].ID != "setup" || entries[1].ID != "artifact_check" || entries[2].ID != "cleanup" || entries[1].Stdout != "artifact verified\n" {
		t.Fatalf("unexpected outcomes: %s", outcomes)
	}
	for _, entry := range entries {
		if entry.ExitCode != 0 {
			t.Fatalf("failed step: %+v", entry)
		}
	}
	var compact any
	if err = json.Unmarshal(outcomes, &compact); err != nil {
		t.Fatal(err)
	}
	// JSON map key order differs from the JS encoder; obtain the report reference.
	reportRaw, _ := json.Marshal(r)
	var saved struct {
		Report struct {
			Checks []testlab.Check `json:"checks"`
		} `json:"report"`
	}
	if err = json.Unmarshal(reportRaw, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Report.Checks) != 1 {
		t.Fatalf("missing report checks: %s", reportRaw)
	}
	ref := strings.TrimPrefix(saved.Report.Checks[0].Evidence, "sha256:")
	req := httptest.NewRequest(http.MethodGet, "/admin/test-runs/"+runID+"/evidence/"+ref, nil)
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("download: %d %s", rr.Code, rr.Body.String())
	}
	var downloaded any
	if err = json.Unmarshal(rr.Body.Bytes(), &downloaded); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(compact)
	b, _ := json.Marshal(downloaded)
	if string(a) != string(b) || digest(rr.Body.Bytes()) != ref {
		t.Fatal("uploaded outcomes mismatch")
	}
	if err = st.CheckDesktopReleaseGate(ctx, release, targets, artifacts); err != nil {
		t.Fatal(err)
	}
	release.ResolvedCommit = strings.Repeat("b", 40)
	if st.CheckDesktopReleaseGate(ctx, release, targets, artifacts) == nil {
		t.Fatal("gate accepted changed source")
	}
	invoke(`{"status":"idle"}`)
}
