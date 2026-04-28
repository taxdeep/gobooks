// 遵循project_guide.md
package services

// company_features.go — Company-scoped feature enablement.
//
// Role in the stack
// -----------------
// This service sits one level above the Phase H capability rails
// (companies.tracking_enabled, companies.receipt_required,
// companies.gr_ir_clearing_account_id, etc.). Those rails govern
// inventory-module posting behaviour; THIS surface governs whether
// a product feature family (Inventory Alpha, Task, …) is offered to
// the company's UI and API at all.
//
//   ┌── product feature (this file) ──┐
//   │   e.g. "Inventory Alpha"        │
//   │   self-serve, owner-gated,      │
//   │   acknowledged, audited         │
//   └──────────────┬──────────────────┘
//                  │
//                  ▼
//   ┌── capability rails (Phase H) ───┐
//   │   receipt_required,             │
//   │   tracking_enabled,             │
//   │   gr_ir_clearing_account_id     │
//   └──────────────┬──────────────────┘
//                  │
//                  ▼
//   ┌── inventory module (Phase D–H)  ┐
//   │   ReceiveStock, IssueStock, …   │
//   └─────────────────────────────────┘
//
// Enablement flow
// ---------------
// Self-serve enable is intentionally a multi-step gesture:
//   1. Owner selects a reason_code (+ optional reason_note).
//   2. Owner reads risk copy (Alpha, no history rewrite, etc.).
//   3. Owner checks every required acknowledgement.
//   4. Owner types a verbatim confirmation string (e.g.
//      "ENABLE INVENTORY") that matches the feature definition.
// Only after all four hold does EnableCompanyFeature mutate state.
//
// Disable is a single deliberate action with a soft confirmation in
// the UI — no typed string — because disable never changes history,
// only future behaviour gating.

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Error sentinels ──────────────────────────────────────────────────────────

var (
	// ErrFeatureUnknown — the feature_key supplied is not in
	// AllCompanyFeatureDefinitions. Rejected before any DB touch.
	ErrFeatureUnknown = errors.New("company_features: unknown feature_key")

	// ErrFeatureNotSelfServe — the feature is registered but its
	// SelfServeEnable flag is false (e.g. Task "coming_soon"). The
	// feature is visible on the Features page but cannot be flipped
	// on by an owner without additional engineering work.
	ErrFeatureNotSelfServe = errors.New("company_features: feature is not available for self-serve enablement")

	// ErrFeatureOwnerRequired — enable / disable can only be
	// performed by a user whose CompanyMembership.Role is
	// CompanyRoleOwner. The UI hides the buttons for non-owners;
	// this error is the backend backstop.
	ErrFeatureOwnerRequired = errors.New("company_features: only the company owner can enable or disable features")

	// ErrFeatureTypedConfirmationMismatch — the typed confirmation
	// string did not match the feature's TypedConfirmText exactly
	// (case-sensitive, whitespace-sensitive).
	ErrFeatureTypedConfirmationMismatch = errors.New("company_features: typed confirmation does not match")

	// ErrFeatureAcknowledgementsIncomplete — one or more required
	// acknowledgements were not confirmed.
	ErrFeatureAcknowledgementsIncomplete = errors.New("company_features: all required acknowledgements must be confirmed")

	// ErrFeatureReasonCodeInvalid — reason_code was empty or not in
	// the enumerated AllReasonCodes set.
	ErrFeatureReasonCodeInvalid = errors.New("company_features: reason_code must be one of the enumerated values")

	// ErrInventoryAlphaNotEnabled — a backend entry point that
	// requires the Inventory Alpha feature to be enabled on the
	// company was called while the company's feature row is off (or
	// absent). Returned by RequireCompanyFeatureEnabled when the
	// guarded feature is inventory; analogous errors per feature
	// family can be added as future guarded entry points land.
	ErrInventoryAlphaNotEnabled = errors.New("company_features: inventory alpha is not enabled for this company")
)

// ── Value types (view / input) ───────────────────────────────────────────────

// FeatureView is the composed shape returned to the UI: static
// feature-definition metadata plus the company-specific state. Pure
// read model — no mutations go through this struct.
type FeatureView struct {
	Key              models.FeatureKey
	Label            string
	Maturity         models.FeatureMaturity
	Description      string
	FitDescription   string
	SelfServeEnable  bool
	TypedConfirmText string
	AckVersion       string
	RequiredAcks     []string

	Status           models.FeatureStatus
	EnabledAt        *time.Time
	EnabledByUserID  *uuid.UUID
	AcknowledgedAt   *time.Time
	AckVersionStored string
	ReasonCode       models.ReasonCode
	ReasonNote       string
}

// IsEnabled is the one-liner the UI asks.
func (v FeatureView) IsEnabled() bool {
	return v.Status == models.FeatureStatusEnabled
}

// EnableCompanyFeatureInput is the full payload for the multi-step
// enable gesture. The UI assembles this from the enable modal form;
// the service validates every field before writing.
type EnableCompanyFeatureInput struct {
	CompanyID                uint
	FeatureKey               models.FeatureKey
	Actor                    string
	ActorUserID              *uuid.UUID
	ActorRole                models.CompanyRole
	ReasonCode               models.ReasonCode
	ReasonNote               string
	AckVersion               string // should match the feature's AckVersion; echoed for integrity
	TypedConfirmation        string
	ConfirmAcknowledgements  []bool // length + content validated against InventoryAlphaRequiredAcknowledgements
}

// DisableCompanyFeatureInput captures the lighter disable gesture.
// No typed confirmation, no acknowledgement re-prompt — the UI's
// soft modal explains that disable does not rewrite history and
// asks the owner to confirm.
type DisableCompanyFeatureInput struct {
	CompanyID   uint
	FeatureKey  models.FeatureKey
	Actor       string
	ActorUserID *uuid.UUID
	ActorRole   models.CompanyRole
}

// ── Read surface ─────────────────────────────────────────────────────────────

// GetCompanyFeatures returns one FeatureView per registered feature,
// in display order. Features with no row in company_features are
// composed with their default off state; rows in the DB override.
func GetCompanyFeatures(db *gorm.DB, companyID uint) ([]FeatureView, error) {
	if companyID == 0 {
		return nil, fmt.Errorf("GetCompanyFeatures: CompanyID required")
	}
	defs := models.AllCompanyFeatureDefinitions()

	var rows []models.CompanyFeature
	if err := db.Where("company_id = ?", companyID).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load company features: %w", err)
	}
	byKey := make(map[models.FeatureKey]models.CompanyFeature, len(rows))
	for _, r := range rows {
		byKey[r.FeatureKey] = r
	}

	out := make([]FeatureView, 0, len(defs))
	for _, d := range defs {
		view := FeatureView{
			Key:              d.Key,
			Label:            d.Label,
			Maturity:         d.Maturity,
			Description:      d.Description,
			FitDescription:   d.FitDescription,
			SelfServeEnable:  d.SelfServeEnable,
			TypedConfirmText: d.TypedConfirmText,
			AckVersion:       d.AckVersion,
			RequiredAcks:     requiredAcksFor(d.Key),
			Status:           models.FeatureStatusOff,
		}
		if row, ok := byKey[d.Key]; ok {
			view.Status = row.Status
			view.EnabledAt = row.EnabledAt
			view.EnabledByUserID = row.EnabledByUserID
			view.AcknowledgedAt = row.AcknowledgedAt
			view.AckVersionStored = row.AckVersion
			view.ReasonCode = row.ReasonCode
			view.ReasonNote = row.ReasonNote
		}
		out = append(out, view)
	}
	return out, nil
}

// GetCompanyFeature returns the single FeatureView for a given key,
// or ErrFeatureUnknown if the key is not registered.
func GetCompanyFeature(db *gorm.DB, companyID uint, key models.FeatureKey) (*FeatureView, error) {
	all, err := GetCompanyFeatures(db, companyID)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Key == key {
			return &all[i], nil
		}
	}
	return nil, ErrFeatureUnknown
}

// IsCompanyFeatureEnabled is the one-liner backend gate any feature-
// dependent entry point should call. Returns (false, nil) when the
// row is absent or status='off' and (true, nil) when enabled.
func IsCompanyFeatureEnabled(db *gorm.DB, companyID uint, key models.FeatureKey) (bool, error) {
	if companyID == 0 {
		return false, fmt.Errorf("IsCompanyFeatureEnabled: CompanyID required")
	}
	if models.LookupCompanyFeatureDefinition(key) == nil {
		return false, ErrFeatureUnknown
	}
	var row models.CompanyFeature
	err := db.Where("company_id = ? AND feature_key = ?", companyID, key).
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("load company feature: %w", err)
	}
	return row.Status == models.FeatureStatusEnabled, nil
}

// RequireCompanyFeatureEnabled is the composable error-returning form
// of IsCompanyFeatureEnabled. Use this at the entry of any code path
// that is gated on a feature: one call, returns nil when allowed or
// a sentinel when blocked. Currently only Inventory-family gating has
// a dedicated sentinel (ErrInventoryAlphaNotEnabled); additional
// feature families get their own sentinel as guarded entry points
// land. Unregistered keys return ErrFeatureUnknown.
//
// This is the "backend guard is not a shell" proof: a call to this
// function rejects when the feature is off regardless of what the UI
// hid or showed.
func RequireCompanyFeatureEnabled(db *gorm.DB, companyID uint, key models.FeatureKey) error {
	enabled, err := IsCompanyFeatureEnabled(db, companyID, key)
	if err != nil {
		return err
	}
	if enabled {
		return nil
	}
	switch key {
	case models.FeatureKeyInventory:
		return ErrInventoryAlphaNotEnabled
	default:
		return fmt.Errorf("company_features: feature %q is not enabled for this company", key)
	}
}

// ── Write surface ────────────────────────────────────────────────────────────

// EnableCompanyFeature flips a feature to enabled after all the gates
// hold. Validations run in this order (fail-fast):
//
//  1. Owner role on the membership (backend backstop to UI hiding).
//  2. Feature key is registered and SelfServeEnable=true.
//  3. ReasonCode is in the enumerated set.
//  4. ConfirmAcknowledgements length matches the feature's required
//     ack list, every entry is true.
//  5. TypedConfirmation exactly matches the feature's TypedConfirmText.
//  6. AckVersion on input matches the feature's current AckVersion
//     (prevents submitting an old-prompt ack against new wording).
//
// On success: UPSERT a row with status='enabled' and all audit
// fields populated, and write two audit log entries (one for ack,
// one for enable) inside the same transaction. Subsequent calls on
// an already-enabled row are treated as idempotent no-ops.
func EnableCompanyFeature(db *gorm.DB, in EnableCompanyFeatureInput) error {
	if in.CompanyID == 0 {
		return fmt.Errorf("EnableCompanyFeature: CompanyID required")
	}

	// 1. Owner gate.
	if in.ActorRole != models.CompanyRoleOwner {
		return ErrFeatureOwnerRequired
	}

	// 2. Feature registry gates.
	def := models.LookupCompanyFeatureDefinition(in.FeatureKey)
	if def == nil {
		return ErrFeatureUnknown
	}
	if !def.SelfServeEnable {
		return ErrFeatureNotSelfServe
	}

	// 3. Reason code.
	if !isKnownReasonCode(in.ReasonCode) {
		return ErrFeatureReasonCodeInvalid
	}

	// 4. Acknowledgements.
	required := requiredAcksFor(in.FeatureKey)
	if len(in.ConfirmAcknowledgements) != len(required) {
		return ErrFeatureAcknowledgementsIncomplete
	}
	for _, ok := range in.ConfirmAcknowledgements {
		if !ok {
			return ErrFeatureAcknowledgementsIncomplete
		}
	}

	// 5. Typed confirmation.
	if in.TypedConfirmation != def.TypedConfirmText {
		return ErrFeatureTypedConfirmationMismatch
	}

	// 6. Ack version match.
	if in.AckVersion != def.AckVersion {
		return ErrFeatureAcknowledgementsIncomplete
	}

	return db.Transaction(func(tx *gorm.DB) error {
		var existing models.CompanyFeature
		err := tx.Where("company_id = ? AND feature_key = ?", in.CompanyID, in.FeatureKey).
			First(&existing).Error
		switch {
		case err == nil:
			if existing.Status == models.FeatureStatusEnabled {
				return nil // idempotent no-op
			}
		case errors.Is(err, gorm.ErrRecordNotFound):
			// fall through, will insert
		default:
			return fmt.Errorf("load company feature: %w", err)
		}

		now := time.Now().UTC()
		beforeStatus := models.FeatureStatusOff
		if existing.ID != 0 {
			beforeStatus = existing.Status
		}

		row := models.CompanyFeature{
			ID:              existing.ID,
			CompanyID:       in.CompanyID,
			FeatureKey:      in.FeatureKey,
			Status:          models.FeatureStatusEnabled,
			Maturity:        def.Maturity,
			EnabledAt:       &now,
			EnabledByUserID: in.ActorUserID,
			AcknowledgedAt:  &now,
			AckVersion:      def.AckVersion,
			ReasonCode:      in.ReasonCode,
			ReasonNote:      in.ReasonNote,
		}
		if existing.ID == 0 {
			row.CreatedAt = now
		}
		row.UpdatedAt = now
		if err := tx.Save(&row).Error; err != nil {
			return fmt.Errorf("save company feature: %w", err)
		}

		cid := in.CompanyID
		auditDetails := map[string]any{
			"feature_key":   string(in.FeatureKey),
			"maturity":      string(def.Maturity),
			"reason_code":   string(in.ReasonCode),
			"reason_note":   in.ReasonNote,
			"ack_version":   def.AckVersion,
		}

		// Ack audit: captures the user's explicit acknowledgement of
		// the alpha warning + ack version at this moment.
		TryWriteAuditLogWithContextDetails(
			tx,
			auditActionAckFor(in.FeatureKey),
			"company_feature",
			row.ID,
			actorOrSystem(in.Actor),
			auditDetails,
			&cid,
			in.ActorUserID,
			nil,
			map[string]any{
				"ack_version":       def.AckVersion,
				"typed_confirm_ok":  true,
				"acknowledgements":  len(required),
			},
		)

		// Enable audit: captures the state flip itself.
		TryWriteAuditLogWithContextDetails(
			tx,
			auditActionEnableFor(in.FeatureKey),
			"company_feature",
			row.ID,
			actorOrSystem(in.Actor),
			auditDetails,
			&cid,
			in.ActorUserID,
			map[string]any{"status": string(beforeStatus)},
			map[string]any{"status": string(models.FeatureStatusEnabled)},
		)
		return nil
	})
}

// DisableCompanyFeature flips an enabled feature back to off. No-op
// when the feature was already off or absent (idempotent). Owner
// required, same as enable. Writes one audit entry on effective
// change.
func DisableCompanyFeature(db *gorm.DB, in DisableCompanyFeatureInput) error {
	if in.CompanyID == 0 {
		return fmt.Errorf("DisableCompanyFeature: CompanyID required")
	}
	if in.ActorRole != models.CompanyRoleOwner {
		return ErrFeatureOwnerRequired
	}
	def := models.LookupCompanyFeatureDefinition(in.FeatureKey)
	if def == nil {
		return ErrFeatureUnknown
	}

	return db.Transaction(func(tx *gorm.DB) error {
		var existing models.CompanyFeature
		err := tx.Where("company_id = ? AND feature_key = ?", in.CompanyID, in.FeatureKey).
			First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // no row = already off
		}
		if err != nil {
			return fmt.Errorf("load company feature: %w", err)
		}
		if existing.Status == models.FeatureStatusOff {
			return nil // already off, idempotent
		}

		now := time.Now().UTC()
		if err := tx.Model(&models.CompanyFeature{}).
			Where("id = ?", existing.ID).
			Updates(map[string]any{
				"status":     string(models.FeatureStatusOff),
				"updated_at": now,
			}).Error; err != nil {
			return fmt.Errorf("persist disable: %w", err)
		}

		cid := in.CompanyID
		TryWriteAuditLogWithContextDetails(
			tx,
			auditActionDisableFor(in.FeatureKey),
			"company_feature",
			existing.ID,
			actorOrSystem(in.Actor),
			map[string]any{
				"feature_key": string(in.FeatureKey),
				"maturity":    string(existing.Maturity),
			},
			&cid,
			in.ActorUserID,
			map[string]any{"status": string(models.FeatureStatusEnabled)},
			map[string]any{"status": string(models.FeatureStatusOff)},
		)
		return nil
	})
}

// ── helpers ──────────────────────────────────────────────────────────────────

func requiredAcksFor(key models.FeatureKey) []string {
	switch key {
	case models.FeatureKeyInventory:
		return models.InventoryAlphaRequiredAcknowledgements()
	}
	return nil
}

func isKnownReasonCode(c models.ReasonCode) bool {
	for _, candidate := range models.AllReasonCodes() {
		if candidate == c {
			return true
		}
	}
	return false
}

// Audit action names per feature. Centralised so that grep for
// "company.feature.inventory.enabled" (and its kin) finds all writers
// in exactly one place.
func auditActionAckFor(key models.FeatureKey) string {
	return "company.feature." + string(key) + ".warning_acknowledged"
}
func auditActionEnableFor(key models.FeatureKey) string {
	return "company.feature." + string(key) + ".enabled"
}
func auditActionDisableFor(key models.FeatureKey) string {
	return "company.feature." + string(key) + ".disabled"
}
