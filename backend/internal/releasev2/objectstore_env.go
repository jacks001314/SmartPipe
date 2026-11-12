package releasev2

import (
	"fmt"
	"os"
	"strings"
)

// ObjectStoreFromEnv selects S3/MinIO when SMARTPIPE_S3_BUCKET is configured
// and falls back to a local directory for development.
func ObjectStoreFromEnv(localDir string) (ObjectStore, string, error) {
	if bucket := strings.TrimSpace(os.Getenv("SMARTPIPE_S3_BUCKET")); bucket != "" {
		store, err := NewS3Store(
			os.Getenv("SMARTPIPE_S3_ENDPOINT"), os.Getenv("SMARTPIPE_S3_REGION"), bucket,
			os.Getenv("SMARTPIPE_S3_ACCESS_KEY"), os.Getenv("SMARTPIPE_S3_SECRET_KEY"),
			os.Getenv("SMARTPIPE_S3_SESSION_TOKEN"), os.Getenv("SMARTPIPE_S3_PREFIX"),
		)
		if err != nil {
			return nil, "", err
		}
		return store, "s3", nil
	}
	if strings.TrimSpace(localDir) == "" {
		localDir = "./data/releases"
	}
	store, err := NewLocalStore(localDir)
	if err != nil {
		return nil, "", fmt.Errorf("releasev2: local object store: %w", err)
	}
	store.PublicURL = strings.TrimSpace(os.Getenv("SMARTPIPE_RELEASE_OBJECTS_PUBLIC_URL"))
	return store, "local", nil
}
