package scheduler

import (
	"context"

	"smartpipe/backend/internal/codexrunner"
)

func diffForTest(dir string) (string, error) {
	return codexrunner.Diff(context.Background(), dir, "git")
}
