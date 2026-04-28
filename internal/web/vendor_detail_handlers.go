// 遵循project_guide.md
package web

// vendor_detail_handlers.go — GET /vendors/:id — vendor profile page.
// AP mirror of customer detail. Display-only today — vendor editing still
// routes through the create form on /vendors; adding edit is a separate task.

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/shopspring/decimal"

	"balanciz/internal/models"
	"balanciz/internal/services"
	"balanciz/internal/web/templates/pages"
)

const vendorDetailBillCap = 25 // cap table rows to avoid unbounded rendering on noisy vendors

func (s *Server) handleVendorDetail(c *fiber.Ctx) error {
	companyID, ok := ActiveCompanyIDFromCtx(c)
	if !ok {
		return c.Redirect("/select-company", fiber.StatusSeeOther)
	}

	vendorID, err := parseVendorIDParam(c)
	if err != nil {
		return redirectErr(c, "/vendors", "invalid vendor ID")
	}

	var vendor models.Vendor
	if err := s.DB.Where("id = ? AND company_id = ?", vendorID, companyID).First(&vendor).Error; err != nil {
		return redirectErr(c, "/vendors", "vendor not found")
	}

	// Outstanding bills — posted / partially_paid with positive balance, soonest due first.
	var outstandingBills []models.Bill
	s.DB.Preload("Vendor").
		Where("company_id = ? AND vendor_id = ? AND status IN ? AND balance_due > 0",
			companyID, vendorID,
			[]models.BillStatus{models.BillStatusPosted, models.BillStatusPartiallyPaid}).
		Order("due_date asc NULLS LAST, bill_date asc").
		Limit(vendorDetailBillCap).
		Find(&outstandingBills)

	// Recent bills — any status, newest first. Separate query (not the same
	// set as outstanding) so the user sees what's been drafted / paid / voided
	// too when scrolling this page.
	var recentBills []models.Bill
	s.DB.Preload("Vendor").
		Where("company_id = ? AND vendor_id = ?", companyID, vendorID).
		Order("bill_date desc, id desc").
		Limit(vendorDetailBillCap).
		Find(&recentBills)

	// Recent purchase orders — any status, newest first. Capped at the same
	// limit as bills. POs don't generate journal entries so we don't aggregate
	// totals for them; the table is purely a recent-activity window.
	var recentPOs []models.PurchaseOrder
	s.DB.Where("company_id = ? AND vendor_id = ?", companyID, vendorID).
		Order("po_date desc, id desc").
		Limit(vendorDetailBillCap).
		Find(&recentPOs)

	// Aggregates — fresh queries so counts aren't capped by vendorDetailBillCap.
	openStatuses := []models.BillStatus{models.BillStatusPosted, models.BillStatusPartiallyPaid}

	var outstandingCount int64
	s.DB.Model(&models.Bill{}).
		Where("company_id = ? AND vendor_id = ? AND status IN ? AND balance_due > 0",
			companyID, vendorID, openStatuses).
		Count(&outstandingCount)

	var outstandingTotal decimal.Decimal
	var totalResult struct{ Total decimal.Decimal }
	s.DB.Model(&models.Bill{}).
		Select("COALESCE(SUM(balance_due), 0) AS total").
		Where("company_id = ? AND vendor_id = ? AND status IN ? AND balance_due > 0",
			companyID, vendorID, openStatuses).
		Scan(&totalResult)
	outstandingTotal = totalResult.Total

	var overdueCount int64
	today := time.Now().Format("2006-01-02")
	s.DB.Model(&models.Bill{}).
		Where("company_id = ? AND vendor_id = ? AND status IN ? AND balance_due > 0 AND due_date IS NOT NULL AND due_date < ?",
			companyID, vendorID, openStatuses, today).
		Count(&overdueCount)

	// Vendor credit remaining — identical logic to /vendors/:id/credits page.
	creditNotes, _ := services.ListVendorCreditNotes(s.DB, companyID, services.VendorCreditNoteListFilter{VendorID: vendorID})
	creditRemaining := decimal.Zero
	creditCount := 0
	for _, cn := range creditNotes {
		if cn.Status == models.VendorCreditNoteStatusPosted ||
			cn.Status == models.VendorCreditNoteStatusPartiallyApplied {
			creditRemaining = creditRemaining.Add(cn.RemainingAmount)
			if cn.RemainingAmount.IsPositive() {
				creditCount++
			}
		}
	}

	// Payment term label — look up the code in company-scoped payment_terms.
	var termLabel string
	if code := vendor.DefaultPaymentTermCode; code != "" {
		var term models.PaymentTerm
		if err := s.DB.Where("company_id = ? AND code = ?", companyID, code).First(&term).Error; err == nil {
			termLabel = term.Description
		}
	}

	// Lifecycle decision — delete vs deactivate depends on whether the
	// vendor is referenced by any AP document.
	hasRecords, _ := services.VendorHasRecords(s.DB, companyID, vendorID)

	// Legacy ?edit=1 URL → redirect into the Details tab so bookmarks +
	// external links keep working after the tab rewrite.
	editFlag := c.Query("edit") == "1"
	tab := normaliseVendorDetailTab(c.Query("tab"))
	if editFlag && tab == "" {
		tab = "details"
	}
	if tab == "" {
		tab = "transactions"
	}

	vm := pages.VendorDetailVM{
		HasCompany:              true,
		Tab:                     tab,
		Vendor:                  vendor,
		DefaultPaymentTermLabel: termLabel,
		OutstandingBills:        outstandingBills,
		RecentBills:             recentBills,
		RecentPOs:               recentPOs,
		OutstandingBillCount:    int(outstandingCount),
		OutstandingTotal:        outstandingTotal,
		OverdueBillCount:        int(overdueCount),
		CreditCount:             creditCount,
		CreditRemaining:         creditRemaining,
		Editing:                 tab == "details" && editFlag,
		Saved:                   c.Query("saved") == "1",
		HasRecords:              hasRecords,
		Deactivated:             c.Query("deactivated") == "1",
		Reactivated:             c.Query("reactivated") == "1",
		LifecycleErr:            strings.TrimSpace(c.Query("error")),
	}

	// Seed the Details-tab form from the current vendor values.
	if vm.Editing {
		s.loadVendorEditFormData(companyID, &vm)
		vm.FormName = vendor.Name
		vm.FormEmail = vendor.Email
		vm.FormPhone = vendor.Phone
		vm.FormAddress = vendor.Address
		vm.FormCurrencyCode = vendor.CurrencyCode
		vm.FormNotes = vendor.Notes
		vm.FormDefaultPaymentTermCode = vendor.DefaultPaymentTermCode
	}

	// Lazy-load the Transactions tab's unified list only when the tab
	// is active. Other tabs reuse the already-loaded bills / POs / credit
	// aggregates.
	if tab == "transactions" {
		typeFilter := strings.TrimSpace(c.Query("tx_type"))
		statusFilter := strings.TrimSpace(c.Query("tx_status"))
		fromStr := strings.TrimSpace(c.Query("tx_from"))
		toStr := strings.TrimSpace(c.Query("tx_to"))
		dateFrom, dateTo := parseListDateRange(fromStr, toStr)
		rows, _, err := services.ListPurchaseTransactions(s.DB, companyID, services.PurchaseTxFilter{
			Type:     typeFilter,
			Status:   statusFilter,
			DateFrom: dateFrom,
			DateTo:   dateTo,
			VendorID: vendorID,
		}, 1, 100)
		if err == nil {
			vm.Transactions = rows
		}
		vm.TxFilterType = typeFilter
		vm.TxFilterStatus = statusFilter
		vm.TxFilterFrom = fromStr
		vm.TxFilterTo = toStr
	}

	return pages.VendorDetail(vm).Render(c.Context(), c)
}

// normaliseVendorDetailTab collapses the `tab=X` query param to the
// canonical set. Empty / unknown values fall through to transactions
// (default) via the caller.
func normaliseVendorDetailTab(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "transactions", "purchase-orders", "details", "notes":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

// loadVendorEditFormData populates dropdown data for the inline edit form —
// active payment terms and (when multi-currency is enabled) the currency list.
// Same data the create form on /vendors uses.
func (s *Server) loadVendorEditFormData(companyID uint, vm *pages.VendorDetailVM) {
	_ = s.DB.Where("company_id = ? AND is_active = true", companyID).
		Order("sort_order asc, code asc").
		Find(&vm.PaymentTerms)
	vm.MultiCurrency, vm.BaseCurrencyCode, vm.Currencies = s.vendorCurrencyInfo(companyID)
}
