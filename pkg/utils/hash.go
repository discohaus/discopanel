package utils

import (
	"crypto/sha256"
	"encoding/hex"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Scopes a dismissal to the finding's current content
func FindingHash(f *v1.PerformanceFinding) string {
	sum := sha256.Sum256([]byte(f.GetId() + "\x00" + f.GetEpoch()))
	return hex.EncodeToString(sum[:])
}
