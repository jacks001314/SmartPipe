package adminapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"smartpipe/backend/internal/store"
	"smartpipe/backend/internal/testlab"
	"strings"
	"sync/atomic"
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

func TestDesktopTypeScriptWorkerCancellation(t *testing.T) {
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
	const machine = "ts-cancel-machine"
	const run = "ts-cancel-run"
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
	var heartbeats, forbidden atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/test-runner/heartbeat" && heartbeats.Add(1) == 1 {
			if e := st.CancelDesktopTestRun(r.Context(), run); e != nil {
				http.Error(w, "cancel failed", 500)
				return
			}
		}
		if r.URL.Path == "/test-runner/report" || r.URL.Path == "/test-runner/release" || r.URL.Path == "/test-runner/evidence" {
			forbidden.Add(1)
		}
		app.ServeHTTP(w, r)
	}))
	defer server.Close()
	// Real compiled TS client talks HTTP to Iris and persists into PostgreSQL.
	// Only the scenario is a fixture: no product or desktop claim.
	script := `
 import {pathToFileURL} from 'node:url';
 const {RunnerClient}=await import(pathToFileURL(process.argv[1]).href);
 let cleaned=0, stopped=false;
 const client=new RunnerClient(process.argv[2],'ts-cancel-machine','t'.repeat(64));
 const scenario={capabilities:['desktop-observe@1'],
 async execute(a,signal){
  await new Promise((resolve)=>{
   if(signal.aborted)resolve();else signal.addEventListener('abort',resolve,{once:true});
  });
  stopped=true;
  // Even a late scenario PASS must never be submitted after server cancellation.
  return {source_sha:a.run.spec.source_sha,artifact_sha256:a.run.spec.artifact_sha256,
   scenario_version:'1',platform:'linux-amd64',checks:[{id:'desktop_observed',status:'PASS',evidence:''}]};
 },
 async cleanup(){if(!stopped)throw new Error('cleanup before stopped');cleaned++},
 async uploadEvidence(){throw new Error('upload after cancellation')}
 };
 let failure;
 try {await client.runOnce(scenario,undefined,20,5000)}catch(e){failure=e}
 if(!failure?.message.includes('Runner API heartbeat returned 409'))throw new Error('unexpected failure: '+failure);
 if(cleaned!==1)throw new Error('cleanup count '+cleaned);
 `
	cmd := exec.CommandContext(t.Context(), "node", "--input-type=module", "-e", script, worker, server.URL)
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("TS worker: %v: %s", e, out)
	}
	r, err := st.GetDesktopTestRun(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "CANCELLED" {
		t.Fatalf("status=%s", r.Status)
	}
	if heartbeats.Load() != 1 || forbidden.Load() != 0 {
		t.Fatalf("heartbeat=%d forbidden calls=%d", heartbeats.Load(), forbidden.Load())
	}
	var busy string
	if err = st.DB().QueryRow("SELECT run_id FROM desktop_test_machines WHERE id=$1", machine).Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if busy != run {
		t.Fatal("cancelled worker released machine without controlled recovery")
	}
}
