// 遵循project_guide.md
package services

// inventory_idempotency.go — helper that picks the next idempotency-key
// version for movements scoped to (source_type, source_id).
//
// Why this exists
// ---------------
// Every stock movement carries a unique-per-company idempotency key so the
// new inventory API is replay-safe. The first time a bill is posted every
// line's key ends in ":v1". If that bill were later voided and re-posted,
// the naive key pattern would collide with the partial-unique index the
// schema enforces, and the second post would fail with
// ErrDuplicateIdempotency.
//
// nextIdempotencyVersion scans existing keys for the given (source_type,
// source_id) and returns the next free version. Call it once at the top
// of a post/reverse facade, then weave the returned integer into every
// movement key generated during that call.
//
// Defensive: no re-post flow exists today (voided bills cannot be reset to
// draft), but the cost of defending now is one table scan per post — in
// practice one index hit since source_id is selective — which is cheap
// compared to the cost of silently producing duplicate movements if the
// workflow ever changes.

import (
	"fmt"
	"regexp"
	"strconv"

	"gorm.io/gorm"

	"gobooks/internal/models"
)

// keyVersionSuffix pulls the trailing ":v<n>" off an idempotency key. Keys
// that predate versioning (or are hand-constructed without a version
// suffix) map to version 0.
var keyVersionSuffix = regexp.MustCompile(`:v(\d+)$`)

// nextIdempotencyVersion returns the first version number not yet in use
// for movements with the given (company, source_type, source_id) scope.
// First call = 1, second call after a full void/re-post = 2, and so on.
func nextIdempotencyVersion(db *gorm.DB, companyID uint, sourceType string, sourceID uint) (int, error) {
	pattern := fmt.Sprintf("%s:%d:%%", sourceType, sourceID)
	var keys []string
	err := db.Model(&models.InventoryMovement{}).
		Where("company_id = ? AND source_type = ? AND source_id = ? AND idempotency_key LIKE ?",
			companyID, sourceType, sourceID, pattern).
		Pluck("idempotency_key", &keys).Error
	if err != nil {
		return 0, fmt.Errorf("scan idempotency keys: %w", err)
	}

	maxVer := 0
	for _, k := range keys {
		m := keyVersionSuffix.FindStringSubmatch(k)
		if len(m) != 2 {
			continue
		}
		n, convErr := strconv.Atoi(m[1])
		if convErr != nil {
			continue
		}
		if n > maxVer {
			maxVer = n
		}
	}
	return maxVer + 1, nil
}
