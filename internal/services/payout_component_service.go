// 遵循project_guide.md
package services

// payout_component_service.go — Batch 19: Payout component / composition truth.
//
// Provides:
//   - AddGatewayPayoutComponent:        add a composition element to an unmatched payout
//   - DeleteGatewayPayoutComponent:     remove a component from an unmatched payout
//   - ListGatewayPayoutComponents:      list all components for a payout
//   - ComputeGatewayPayoutExpectedNet:  compute the expected bank deposit after components
//
// ─── Design ───────────────────────────────────────────────────────────────────
//
// Components are reconciliation-side explanatory truth only.  No Journal Entry
// is created or modified.  The payout JE (Dr Bank / Dr Fee / Cr Clearing) posted
// at bridge creation is the accounting fact.  Components explain the composition
// of the expected bank deposit so that reconciliation matching can succeed even
// when the bank deposit differs from GatewayPayout.NetAmount.
//
// ─── ExpectedNet formula ──────────────────────────────────────────────────────
//
//   ExpectedNet = GatewayPayout.NetAmount
//               + Σ (amount of credit-direction components)
//               − Σ (amount of debit-direction components)
//
// This is the single canonical formula consumed by reconciliation matching and
// by the payout detail UI.  It is never persisted; callers always derive it live.
//
// ─── Modification guard ───────────────────────────────────────────────────────
// Adding or deleting components on an already-matched payout is rejected with
// ErrComponentPayoutAlreadyMatched.  This prevents the expected-net truth from
// drifting after a reconciliation record has been created against it.
//
// ─── Direction invariants per type ───────────────────────────────────────────
//   fee              → direction must be debit
//   reserve_hold     → direction must be debit
//   reserve_release  → direction must be credit
//   adjustment       → either direction (operator-specified)

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ── Sentinel errors ───────────────────────────────────────────────────────────

var (
	ErrComponentPayoutNotFound       = errors.New("gateway payout not found or does not belong to this company")
	ErrComponentPayoutAlreadyMatched = errors.New("payout is already matched; components cannot be modified")
	ErrComponentTypeInvalid          = errors.New("unsupported component type")
	ErrComponentDirectionInvalid     = errors.New("invalid component direction for this component type")
	ErrComponentAmountInvalid        = errors.New("component amount must be positive")
	ErrComponentDuplicate            = errors.New("duplicate payout component line")
	ErrComponentNotFound             = errors.New("component not found or does not belong to this payout")
)

// ── Input types ───────────────────────────────────────────────────────────────

// AddGatewayPayoutComponentInput carries the fields needed to create one component.
type AddGatewayPayoutComponentInput struct {
	CompanyID       uint
	GatewayPayoutID uint
	Actor           string

	// ComponentType must be one of the four supported types.
	ComponentType models.PayoutComponentType

	// Direction must be consistent with the component type constraints.
	Direction models.PayoutComponentDirection

	// Amount must be positive.  Direction carries the sign semantics.
	Amount decimal.Decimal

	// Description is an optional free-text note (processor reference, memo, etc.).
	Description string
}

// ── Core service functions ────────────────────────────────────────────────────

// AddGatewayPayoutComponent creates a new component for an unmatched payout.
//
// Enforces:
//   - payout exists and belongs to companyID
//   - payout is not already matched (PayoutReconciliation record must not exist)
//   - component type is in the supported set
//   - direction is consistent with component type constraints
//   - amount is positive
//
// No JE is created or modified.
func AddGatewayPayoutComponent(db *gorm.DB, input AddGatewayPayoutComponentInput) (*models.GatewayPayoutComponent, error) {
	// Validate amount first (cheap).
	if !input.Amount.IsPositive() {
		return nil, ErrComponentAmountInvalid
	}

	// Validate component type.
	if !isSupportedComponentType(input.ComponentType) {
		return nil, ErrComponentTypeInvalid
	}

	// Validate direction per type.
	if err := validateComponentDirection(input.ComponentType, input.Direction); err != nil {
		return nil, err
	}

	var comp *models.GatewayPayoutComponent
	actor := normalizePayoutComponentActor(input.Actor)

	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := ensurePayoutMutableForComponents(tx, input.CompanyID, input.GatewayPayoutID); err != nil {
			return err
		}

		comp = &models.GatewayPayoutComponent{
			CompanyID:       input.CompanyID,
			GatewayPayoutID: input.GatewayPayoutID,
			ComponentType:   input.ComponentType,
			Direction:       input.Direction,
			Amount:          input.Amount,
			Description:     input.Description,
		}
		if err := tx.Create(comp).Error; err != nil {
			if isUniqueConstraintError(err) {
				return ErrComponentDuplicate
			}
			return fmt.Errorf("create payout component: %w", err)
		}

		cid := input.CompanyID
		after := payoutComponentSnapshot(comp)
		if err := WriteAuditLogWithContextDetails(
			tx,
			"payout.component_added",
			"gateway_payout_component",
			comp.ID,
			actor,
			map[string]any{
				"company_id":        input.CompanyID,
				"gateway_payout_id": input.GatewayPayoutID,
			},
			&cid,
			nil,
			nil,
			after,
		); err != nil {
			return fmt.Errorf("audit log: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	slog.Info("payout component added",
		"payout_id", input.GatewayPayoutID,
		"type", input.ComponentType,
		"direction", input.Direction,
		"amount", input.Amount.StringFixed(2),
	)
	return comp, nil
}

// DeleteGatewayPayoutComponent removes a component from an unmatched payout.
//
// Enforces:
//   - component exists, belongs to this payout, and company is correct
//   - payout is not already matched
func DeleteGatewayPayoutComponent(db *gorm.DB, companyID, gatewayPayoutID, componentID uint) error {
	return DeleteGatewayPayoutComponentWithActor(db, companyID, gatewayPayoutID, componentID, "")
}

// DeleteGatewayPayoutComponentWithActor removes a component from an unmatched
// payout and records the deletion in the audit log.
func DeleteGatewayPayoutComponentWithActor(db *gorm.DB, companyID, gatewayPayoutID, componentID uint, actor string) error {
	actor = normalizePayoutComponentActor(actor)

	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := ensurePayoutMutableForComponents(tx, companyID, gatewayPayoutID); err != nil {
			return err
		}

		var comp models.GatewayPayoutComponent
		if err := tx.Where("id = ? AND company_id = ? AND gateway_payout_id = ?",
			componentID, companyID, gatewayPayoutID).
			First(&comp).Error; err != nil {
			return ErrComponentNotFound
		}

		before := payoutComponentSnapshot(&comp)
		if err := tx.Delete(&comp).Error; err != nil {
			return fmt.Errorf("delete payout component: %w", err)
		}

		cid := companyID
		if err := WriteAuditLogWithContextDetails(
			tx,
			"payout.component_deleted",
			"gateway_payout_component",
			comp.ID,
			actor,
			map[string]any{
				"company_id":        companyID,
				"gateway_payout_id": gatewayPayoutID,
			},
			&cid,
			nil,
			before,
			nil,
		); err != nil {
			return fmt.Errorf("audit log: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}

	slog.Info("payout component deleted",
		"component_id", componentID,
		"payout_id", gatewayPayoutID,
	)
	return nil
}

// ListGatewayPayoutComponents returns all components for a payout, ordered by
// creation time ascending (oldest first).
func ListGatewayPayoutComponents(db *gorm.DB, companyID, gatewayPayoutID uint) ([]models.GatewayPayoutComponent, error) {
	var comps []models.GatewayPayoutComponent
	err := db.
		Where("company_id = ? AND gateway_payout_id = ?", companyID, gatewayPayoutID).
		Order("id ASC").
		Find(&comps).Error
	return comps, err
}

// ComputeGatewayPayoutExpectedNet returns the expected bank deposit for a payout
// after applying all recorded components:
//
//	ExpectedNet = payout.NetAmount
//	            + Σ (amount of credit-direction components)
//	            − Σ (amount of debit-direction components)
//
// This is the authoritative formula used by both reconciliation matching and the
// UI.  It is derived live from the current component set and never persisted.
func ComputeGatewayPayoutExpectedNet(db *gorm.DB, companyID uint, payout *models.GatewayPayout) (decimal.Decimal, error) {
	comps, err := ListGatewayPayoutComponents(db, companyID, payout.ID)
	if err != nil {
		return decimal.Zero, fmt.Errorf("load components for expected net: %w", err)
	}

	net := payout.NetAmount
	for _, c := range comps {
		switch c.Direction {
		case models.PayoutComponentCredit:
			net = net.Add(c.Amount)
		case models.PayoutComponentDebit:
			net = net.Sub(c.Amount)
		}
	}
	return net, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// isSupportedComponentType returns true when the type is in the Batch 19 set.
func isSupportedComponentType(t models.PayoutComponentType) bool {
	for _, supported := range models.AllPayoutComponentTypes() {
		if t == supported {
			return true
		}
	}
	return false
}

// validateComponentDirection enforces the per-type direction constraints.
//
//	fee              → must be debit
//	reserve_hold     → must be debit
//	reserve_release  → must be credit
//	adjustment       → either direction is valid
func validateComponentDirection(t models.PayoutComponentType, d models.PayoutComponentDirection) error {
	switch t {
	case models.PayoutComponentFee:
		if d != models.PayoutComponentDebit {
			return fmt.Errorf("%w: fee component must have debit direction", ErrComponentDirectionInvalid)
		}
	case models.PayoutComponentReserveHold:
		if d != models.PayoutComponentDebit {
			return fmt.Errorf("%w: reserve_hold component must have debit direction", ErrComponentDirectionInvalid)
		}
	case models.PayoutComponentReserveRelease:
		if d != models.PayoutComponentCredit {
			return fmt.Errorf("%w: reserve_release component must have credit direction", ErrComponentDirectionInvalid)
		}
	case models.PayoutComponentAdjustment:
		if d != models.PayoutComponentDebit && d != models.PayoutComponentCredit {
			return fmt.Errorf("%w: adjustment direction must be debit or credit", ErrComponentDirectionInvalid)
		}
	default:
		return ErrComponentTypeInvalid
	}
	return nil
}

// isPayoutMatched returns true when a PayoutReconciliation record exists for
// the given payout.  Uses a COUNT query for speed (no full row load needed).
func isPayoutMatched(db *gorm.DB, companyID, gatewayPayoutID uint) bool {
	var count int64
	db.Model(&models.PayoutReconciliation{}).
		Where("company_id = ? AND gateway_payout_id = ?", companyID, gatewayPayoutID).
		Count(&count)
	return count > 0
}

func isPayoutMatchedTx(tx *gorm.DB, companyID, gatewayPayoutID uint) bool {
	var count int64
	tx.Model(&models.PayoutReconciliation{}).
		Where("company_id = ? AND gateway_payout_id = ?", companyID, gatewayPayoutID).
		Count(&count)
	return count > 0
}

func ensurePayoutMutableForComponents(tx *gorm.DB, companyID, gatewayPayoutID uint) error {
	var payout models.GatewayPayout
	if err := applyLockForUpdate(
		tx.Where("id = ? AND company_id = ?", gatewayPayoutID, companyID),
	).First(&payout).Error; err != nil {
		return ErrComponentPayoutNotFound
	}
	if isPayoutMatchedTx(tx, companyID, gatewayPayoutID) {
		return ErrComponentPayoutAlreadyMatched
	}
	return nil
}

func normalizePayoutComponentActor(actor string) string {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return "system"
	}
	return actor
}

func payoutComponentSnapshot(comp *models.GatewayPayoutComponent) map[string]any {
	if comp == nil {
		return nil
	}
	return map[string]any{
		"id":                comp.ID,
		"company_id":        comp.CompanyID,
		"gateway_payout_id": comp.GatewayPayoutID,
		"component_type":    string(comp.ComponentType),
		"direction":         string(comp.Direction),
		"amount":            comp.Amount.StringFixed(2),
		"description":       comp.Description,
	}
}
