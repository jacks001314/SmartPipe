package testlab

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

type Machine struct {
	ID         string     `json:"id"`
	Platform   string     `json:"platform"`
	Enabled    bool       `json:"enabled"`
	Generation int64      `json:"generation"`
	RunID      string     `json:"run_id"`
	LeaseUntil *time.Time `json:"lease_until,omitempty"`
	LastSeen   *time.Time `json:"last_seen,omitempty"`
}
type Lease struct {
	MachineID  string    `json:"machine_id"`
	Generation int64     `json:"generation"`
	ExpiresAt  time.Time `json:"expires_at"`
	Run        *Run      `json:"run"`
}

func TokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
