// 遵循project_guide.md
package services

// company_gr_ir_account.go — Phase H slice H.3 audited admin surface
// for companies.gr_ir_clearing_account_id.
//
// The GR/IR (Goods-Received / Invoice-Received) clearing account is
// a company-level liability that bridges the two halves of the
// purchase cycle under Phase H's receipt-first model:
//
//   Receipt post:  Dr Inventory     Cr GR/IR    (H.3 — this slice)
//   Bill post:     Dr GR/IR         Cr A/P      (H.5 — later slice)
//
// One account per company. No per-vendor / per-book override in H.3
// — the ARAPControlMapping pattern can be extended later if real
// multi-book / multi-currency need arises.
//
// Required before PostReceipt can run under `receipt_required=true`.
// If unset at post time, PostReceipt returns
// ErrGRIRAccountNotConfigured with a pointer at this configuration
// surface.

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ErrGRIRClearingAccountInvalid — the referenced account either does
// not exist, belongs to a different company, or is not a liability
// account (a checking/AR/inventory asset account would produce a
// nonsense posting).
var ErrGRIRClearingAccountInvalid = errors.New("gr_ir_clearing: account must exist, belong to the company, and be a liability")

// ChangeCompanyGRIRClearingAccountInput configures the clearing
// account via an audited admin surface. AccountID=nil clears the
// assignment (e.g. if the company wants to wind back out of the
// Receipt-first flow); AccountID=non-nil points at an existing
// liability account in the same company.
type ChangeCompanyGRIRClearingAccountInput struct {
	CompanyID   uint
	AccountID   *uint
	Actor       string
	ActorUserID *uuid.UUID
}

// ChangeCompanyGRIRClearingAccount sets or clears the company's GR/IR
// clearing account with audit. Validates that the assigned account
// (if any) exists, is company-scoped, and is a Liability by
// RootAccountType. Rejects non-liability accounts because posting
// `Cr {asset}` for an inbound inventory accrual would silently
// produce the wrong sign of a balance sheet effect.
//
// No-op when the target state already matches current state: no
// persistence, no audit row. Mirrors G.1 / H.1 idempotency.
func ChangeCompanyGRIRClearingAccount(db *gorm.DB, in ChangeCompanyGRIRClearingAccountInput) error {
	if in.CompanyID == 0 {
		return fmt.Errorf("services.ChangeCompanyGRIRClearingAccount: CompanyID required")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var company models.Company
		if err := tx.Where("id = ?", in.CompanyID).First(&company).Error; err != nil {
			return fmt.Errorf("load company: %w", err)
		}

		// Normalise before comparing: distinguish nil from *0.
		var target *uint
		if in.AccountID != nil && *in.AccountID != 0 {
			// Validate: exists, same company, liability root type.
			var acct models.Account
			if err := tx.Where("id = ?", *in.AccountID).First(&acct).Error; err != nil {
				return fmt.Errorf("%w: %s", ErrGRIRClearingAccountInvalid, err.Error())
			}
			if acct.CompanyID != in.CompanyID {
				return fmt.Errorf("%w: account belongs to company=%d, requested=%d",
					ErrGRIRClearingAccountInvalid, acct.CompanyID, in.CompanyID)
			}
			if acct.RootAccountType != models.RootLiability {
				return fmt.Errorf("%w: account root_type=%q, liability required",
					ErrGRIRClearingAccountInvalid, acct.RootAccountType)
			}
			target = in.AccountID
		}

		// Idempotency: compare current with target (both nilable).
		curr := company.GRIRClearingAccountID
		if equalNilableUint(curr, target) {
			return nil // no-op
		}

		// Persist (handles both set and clear via a map so nil is explicit).
		updates := map[string]any{"gr_ir_clearing_account_id": target}
		if err := tx.Model(&models.Company{}).
			Where("id = ?", in.CompanyID).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("persist gr_ir_clearing_account_id: %w", err)
		}

		cid := in.CompanyID
		action := "company.gr_ir_clearing_account.set"
		if target == nil {
			action = "company.gr_ir_clearing_account.cleared"
		}
		TryWriteAuditLogWithContextDetails(
			tx,
			action,
			"company",
			company.ID,
			actorOrSystem(in.Actor),
			map[string]any{"company_name": company.Name},
			&cid,
			in.ActorUserID,
			map[string]any{"gr_ir_clearing_account_id": nilableUintAsAny(curr)},
			map[string]any{"gr_ir_clearing_account_id": nilableUintAsAny(target)},
		)
		return nil
	})
}

func equalNilableUint(a, b *uint) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func nilableUintAsAny(p *uint) any {
	if p == nil {
		return nil
	}
	return *p
}
