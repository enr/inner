package executor

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// GenerateRunID creates a unique run identifier in the format YYYYMMDD-HHMMSS-xxxx.
func GenerateRunID() string {
	return generateRunID(time.Now())
}

// generateRunID is the testable core: accepts an explicit time.
func generateRunID(t time.Time) string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s", t.Format("20060102-150405"), hex.EncodeToString(b))
}
