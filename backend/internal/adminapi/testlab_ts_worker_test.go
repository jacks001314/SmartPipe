package adminapi

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"smartpipe/backend/internal/store"
	"smartpipe/backend/internal/testlab"
	"strings"
	"testing"
)

func TestDesktopTypeScriptWorker(t *testing.T) {
	worker := os.Getenv("SMARTPIPE_WORKER_JS")
	if worker == "" {
		t.Skip("SMARTPIPE_WORKER_JS must point to built test-runner/dist/src/worker.js")
	}
	if !filepath.IsAbs(worker) {
		t.Fatal("worker module path must be absolute")
	}
	app, st := newTestAPI(t, "")
	p, err := st.CreateProject(t.Context(), &store.Project{Name: "ts-worker"})
	if err != nil {
		t.Fatal(err)
	}
	const machine = "ts-worker-machine"
	const run = "ts-worker-run"
	token := strings.Repeat("t", 64)
	if err = st.RegisterDesktopMachine(t.Context(), machine, "linux-amd64", testlab.TokenDigest(token)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.DB().Exec("DELETE FROM desktop_test_runs WHERE id=$1", run)
		_, _ = st.DB().Exec("DELETE FROM desktop_test_machines WHERE id=$1", machine)
	})
	spec := testlab.Spec{ProjectID: p.ID, SourceSHA: strings.Repeat("a", 40), ArtifactSHA256: strings.Repeat("b", 64), Scenario: "desktop-observe", ScenarioVersion: "1", Platform: "linux-amd64", RequiredChecks: []string{"desktop_observed"}}
	if _, err = st.CreateDesktopTestRun(t.Context(), run, spec); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app)
	defer server.Close()
	// Real compiled TS client talks HTTP to Iris and persists into PostgreSQL.
	// Only the scenario is a fixture: no product or desktop claim.
	script := `
 import {pathToFileURL} from 'node:url';
 const {RunnerClient}=await import(pathToFileURL(process.argv[1]).href);
 let report,cleaned=false;
 const client=new RunnerClient(process.argv[2],'ts-worker-machine','t'.repeat(64));
 const scenario={
 capabilities:['desktop-observe@1'],
 async execute(a){
 report={source_sha:a.run.spec.source_sha,artifact_sha256:a.run.spec.artifact_sha256,
 scenario_version:'1',platform:'linux-amd64',checks:[{id:'desktop_observed',status:'PASS',evidence:''}]};
 return report;
 },
 async cleanup(){cleaned=true},
 async uploadEvidence(upload){
 if(!cleaned)throw new Error('cleanup ordering');
 report.checks[0].evidence=await upload('text/plain',Buffer.from('real TS HTTP evidence'));
 }};
 if(!await client.runOnce(scenario))throw new Error('no assignment');
 if(await client.runOnce(scenario))throw new Error('duplicate assignment');
 `
	cmd := exec.CommandContext(t.Context(), "node", "--input-type=module", "-e", script, worker, server.URL)
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("TS worker: %v: %s", e, out)
	}
	r, err := st.GetDesktopTestRun(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "PASS" {
		t.Fatalf("status=%s", r.Status)
	}
	var busy string
	if err = st.DB().QueryRow("SELECT run_id FROM desktop_test_machines WHERE id=$1", machine).Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if busy != "" {
		t.Fatal("worker did not release machine")
	}
}
