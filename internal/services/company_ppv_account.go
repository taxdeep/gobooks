// 遵循project_guide.md
package services

// company_ppv_account.go — Phase H slice H.5 audited admin surface
// for companies.purchase_price_variance_account_id.
//
// PPV (Purchase Price Variance) absorbs the delta between Bill
// amount and matched Receipt amount on stock-backed bill lines
// posted under receipt_required=true. Single account, sign-based:
//
//   Bill > Receipt   →  Dr PPV  (unfavorable variance, expense up)
//   Bill < Receipt   →  Cr PPV  (favorable variance, expense down)
//
// One account per company. Root type must be P&L — either Expense
// or CostOfSales. Rejecting asset/liability/equity prevents silent
// balance-sheet distortion from mis-posted variance.
//
// Required before PostBill can complete a matched flow. If unset at
// post time with a stock line linked to a Receipt, PostBill fails
// loud with ErrPPVAccountNotConfigured.

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"balanciz/internal/models"
)

// ErrPPVAccountInvalid — the referenced account either does not
// exist, belongs to a different company, or is not a P&L root type
// (Expense or CostOfSales). Variance posted to a non-P&L account
// silently skews the balance sheet.
var ErrPPVAccountInvalid = errors.New("ppv: account must exist, belong to the company, and be Expense or CostOfSales")

// ChangeCompanyPPVAccountInput — analogous to the GR/IR setter.
// AccountID=nil clears the assignment; non-nil sets and validates.
type ChangeCompanyPPVAccountInput struct {
	CompanyID   uint
	AccountID   *uint
	Actor       string
	ActorUserID *uuid.UUID
}

// ChangeCompanyPPVAccount sets or clears companies.purchase_price_variance_account_id
// with audit. Mirrors ChangeCompanyGRIRClearingAccount's shape: tx
// wrapper, validation, idempotent no-op, audit row only on effective
// change.
func ChangeCompanyPPVAccount(db *gorm.DB, in ChangeCompanyPPVAccountInput) error {
	if in.CompanyID == 0 {
		return fmt.Errorf("services.ChangeCompanyPPVAccount: CompanyID required")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var company models.Company
		if err := tx.Where("id = ?", in.CompanyID).First(&company).Error; err != nil {
			return fmt.Errorf("load company: %w", err)
		}

		var target *uint
		if in.AccountID != nil && *in.AccountID != 0 {
			var acct models.Account
			if err := tx.Where("id = ?", *in.AccountID).First(&acct).Error; err != nil {
				return fmt.Errorf("%w: %s", ErrPPVAccountInvalid, err.Error())
			}
			if acct.CompanyID != in.CompanyID {
				return fmt.Errorf("%w: account belongs to company=%d, requested=%d",
					ErrPPVAccountInvalid, acct.CompanyID, in.CompanyID)
			}
			if acct.RootAccountType != models.RootExpense &&
				acct.RootAccountType != models.RootCostOfSales {
				return fmt.Errorf("%w: account root_type=%q, Expense or CostOfSales required",
					ErrPPVAccountInvalid, acct.RootAccountType)
			}
			target = in.AccountID
		}

		curr := company.PurchasePriceVarianceAccountID
		if equalNilableUint(curr, target) {
			return nil
		}

		updates := map[string]any{"purchase_price_variance_account_id": target}
		if err := tx.Model(&models.Company{}).
			Where("id = ?", in.CompanyID).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("persist purchase_price_variance_account_id: %w", err)
		}

		cid := in.CompanyID
		action := "company.ppv_account.set"
		if target == nil {
			action = "company.ppv_account.cleared"
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
			map[string]any{"purchase_price_variance_account_id": nilableUintAsAny(curr)},
			map[string]any{"purchase_price_variance_account_id": nilableUintAsAny(target)},
		)
		return nil
	})
}
