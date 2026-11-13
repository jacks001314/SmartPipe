package store

import (
	"context"
	"encoding/json"
	"smartpipe/backend/internal/testlab"
)

func (s *Store) ListDesktopTestRuns(ctx context.Context, projectID string) ([]testlab.Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,spec,status,report,created_at,finished_at FROM desktop_test_runs WHERE ($1='' OR project_id=$1) ORDER BY created_at DESC LIMIT 100`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []testlab.Run{}
	for rows.Next() {
		var r testlab.Run
		var spec, report []byte
		if err = rows.Scan(&r.ID, &spec, &r.Status, &report, &r.CreatedAt, &r.FinishedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(spec, &r.Spec); err != nil {
			return nil, err
		}
		if len(report) > 0 {
			if err = json.Unmarshal(report, &r.Report); err != nil {
				return nil, err
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
