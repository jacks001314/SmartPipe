package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"smartpipe/backend/internal/releasev2"
	"smartpipe/backend/internal/testlab"
	"strings"
)

type DesktopGatePolicy struct {
	Scenario       string   `json:"scenario"`
	Version        string   `json:"version"`
	Platform       string   `json:"platform"`
	ArtifactRole   string   `json:"artifact_role"`
	RequiredChecks []string `json:"required_checks"`
}

func init() {
	migrations = append(migrations, migration{version: 51, name: "desktop_gate_policy", sql: `
CREATE TABLE IF NOT EXISTS desktop_gate_policies(project_id TEXT PRIMARY KEY REFERENCES projects(id), policies JSONB NOT NULL);
`})
}
func (s *Store) SetDesktopGatePolicies(ctx context.Context, projectID string, policies []DesktopGatePolicy) error {
	if len(policies) > 32 {
		return testlab.ErrInvalid
	}
	for _, p := range policies {
		if p.Scenario == "" || p.Scenario == "desktop-observe" || p.Version == "" || p.Platform == "" || p.ArtifactRole == "" || len(p.RequiredChecks) == 0 {
			return fmt.Errorf("%w: full acceptance scenario required; observation alone is not a release gate", testlab.ErrInvalid)
		}
		seen := map[string]bool{}
		for _, id := range p.RequiredChecks {
			if id == "" || seen[id] {
				return testlab.ErrInvalid
			}
			seen[id] = true
		}
	}
	data, err := json.Marshal(policies)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO desktop_gate_policies(project_id,policies) VALUES($1,$2) ON CONFLICT(project_id) DO UPDATE SET policies=excluded.policies`, projectID, data)
	return err
}
func (s *Store) GetDesktopGatePolicies(ctx context.Context, projectID string) ([]DesktopGatePolicy, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT policies FROM desktop_gate_policies WHERE project_id=$1`, projectID).Scan(&data)
	if err == sql.ErrNoRows {
		return []DesktopGatePolicy{}, nil
	}
	if err != nil {
		return nil, err
	}
	var policies []DesktopGatePolicy
	err = json.Unmarshal(data, &policies)
	return policies, err
}

// CheckDesktopReleaseGate loads policies from the control database, not agent
// arguments. Only machine-completed runs with stored evidence can satisfy it.
func (s *Store) CheckDesktopReleaseGate(ctx context.Context, run *releasev2.Run, targets []releasev2.Target, artifacts []releasev2.Artifact) error {
	policies, err := s.GetDesktopGatePolicies(ctx, run.ProjectID)
	if err != nil {
		return err
	}
	platforms := map[string]string{}
	for _, target := range targets {
		platforms[target.TargetID] = target.OS + "-" + target.Arch
	}
	for _, policy := range policies {
		found := false
		for _, artifact := range artifacts {
			if artifact.Role != policy.ArtifactRole || platforms[artifact.TargetID] != policy.Platform {
				continue
			}
			found = true
			expected := testlab.Spec{ProjectID: run.ProjectID, SourceSHA: run.ResolvedCommit, ArtifactSHA256: desktopArtifactDigest(artifact.SHA256), Scenario: policy.Scenario, ScenarioVersion: policy.Version, Platform: policy.Platform, RequiredChecks: policy.RequiredChecks}
			if err = expected.Validate(); err != nil {
				return err
			}
			rows, qerr := s.db.QueryContext(ctx, `SELECT id FROM desktop_test_runs WHERE project_id=$1 AND status='PASS' AND machine_id<>'' AND spec->>'source_sha'=$2 AND spec->>'artifact_sha256'=$3 AND spec->>'scenario'=$4 AND spec->>'scenario_version'=$5 AND spec->>'platform'=$6 ORDER BY created_at DESC LIMIT 100`, run.ProjectID, expected.SourceSHA, expected.ArtifactSHA256, policy.Scenario, policy.Version, policy.Platform)
			if qerr != nil {
				return qerr
			}
			ids := []string{}
			for rows.Next() {
				var id string
				if qerr = rows.Scan(&id); qerr != nil {
					rows.Close()
					return qerr
				}
				ids = append(ids, id)
			}
			qerr = rows.Err()
			rows.Close()
			if qerr != nil {
				return qerr
			}
			passed := false
			for _, id := range ids {
				candidate, cerr := s.GetDesktopTestRun(ctx, id)
				if cerr != nil {
					return cerr
				}
				if testlab.MatchesPassedRun(*candidate, expected) != nil {
					continue
				}
				evidenceOK := true
				for _, check := range candidate.Report.Checks {
					if check.Status != "PASS" {
						continue
					}
					var exists bool
					if cerr = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM desktop_test_evidence WHERE run_id=$1 AND 'sha256:'||sha256=$2)`, id, check.Evidence).Scan(&exists); cerr != nil {
						return cerr
					}
					if !exists {
						evidenceOK = false
						break
					}
				}
				if evidenceOK {
					passed = true
					break
				}
			}
			if !passed {
				return fmt.Errorf("online acceptance missing: %s %s %s (%s)", policy.Scenario, policy.Platform, artifact.Name, artifact.SHA256)
			}
		}
		if !found {
			return fmt.Errorf("online acceptance artifact missing: %s/%s", policy.Platform, policy.ArtifactRole)
		}
	}
	return nil
}

// Release storage uses algorithm-prefixed digests; test specs use raw SHA256.
func desktopArtifactDigest(value string) string { return strings.TrimPrefix(value, "sha256:") }
