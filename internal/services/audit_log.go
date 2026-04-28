// 遵循project_guide.md
package services

import (
	"encoding/json"

	"balanciz/internal/logging"
	"github.com/google/uuid"

	"balanciz/internal/models"

	"gorm.io/gorm"
)

// WriteAuditLog saves one audit row (no company / actor user FK).
// details can be any small map/struct that can be marshaled to JSON.
func WriteAuditLog(tx *gorm.DB, action, entityType string, entityID uint, actor string, details any) error {
	return WriteAuditLogWithContext(tx, action, entityType, entityID, actor, details, nil, nil)
}

// TryWriteAuditLog writes one audit row and emits an error log if persistence fails.
func TryWriteAuditLog(tx *gorm.DB, action, entityType string, entityID uint, actor string, details any) {
	if err := WriteAuditLog(tx, action, entityType, entityID, actor, details); err != nil {
		logAuditWriteFailure(action, entityType, entityID, actor, err)
	}
}

// WriteAuditLogWithContext saves one audit row with optional company and actor user (multi-tenant).
func WriteAuditLogWithContext(tx *gorm.DB, action, entityType string, entityID uint, actor string, details any, companyID *uint, actorUserID *uuid.UUID) error {
	if actor == "" {
		actor = "system"
	}

	raw := "{}"
	if details != nil {
		if b, err := json.Marshal(details); err == nil {
			raw = string(b)
		}
	}

	row := models.AuditLog{
		Action:      action,
		EntityType:  entityType,
		EntityID:    entityID,
		Actor:       actor,
		CompanyID:   companyID,
		ActorUserID: actorUserID,
		DetailsJSON: raw,
	}
	return tx.Create(&row).Error
}

// TryWriteAuditLogWithContext writes one audit row and emits an error log if persistence fails.
func TryWriteAuditLogWithContext(tx *gorm.DB, action, entityType string, entityID uint, actor string, details any, companyID *uint, actorUserID *uuid.UUID) {
	if err := WriteAuditLogWithContext(tx, action, entityType, entityID, actor, details, companyID, actorUserID); err != nil {
		logAuditWriteFailure(action, entityType, entityID, actor, err)
	}
}

// mergeDetailsWithBeforeAfter merges optional before/after snapshots into details JSON.
// If details is a map[string]any, keys are copied; otherwise details is stored under "data".
func mergeDetailsWithBeforeAfter(details any, before, after any) any {
	if before == nil && after == nil {
		return details
	}
	m := map[string]any{}
	if details != nil {
		switch d := details.(type) {
		case map[string]any:
			for k, v := range d {
				m[k] = v
			}
		default:
			m["data"] = details
		}
	}
	if before != nil {
		m["before"] = before
	}
	if after != nil {
		m["after"] = after
	}
	return m
}

// WriteAuditLogWithContextDetails is like WriteAuditLogWithContext but embeds optional before/after payloads in details JSON.
func WriteAuditLogWithContextDetails(tx *gorm.DB, action, entityType string, entityID uint, actor string, details any, companyID *uint, actorUserID *uuid.UUID, before, after any) error {
	merged := mergeDetailsWithBeforeAfter(details, before, after)
	return WriteAuditLogWithContext(tx, action, entityType, entityID, actor, merged, companyID, actorUserID)
}

// TryWriteAuditLogWithContextDetails writes one audit row and emits an error log if persistence fails.
func TryWriteAuditLogWithContextDetails(tx *gorm.DB, action, entityType string, entityID uint, actor string, details any, companyID *uint, actorUserID *uuid.UUID, before, after any) {
	if err := WriteAuditLogWithContextDetails(tx, action, entityType, entityID, actor, details, companyID, actorUserID, before, after); err != nil {
		logAuditWriteFailure(action, entityType, entityID, actor, err)
	}
}

func logAuditWriteFailure(action, entityType string, entityID uint, actor string, err error) {
	logging.L().Error("audit log write failed",
		"action", action,
		"entity_type", entityType,
		"entity_id", entityID,
		"actor", actor,
		"err", err,
	)
}
