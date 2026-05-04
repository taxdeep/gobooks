// 遵循project_guide.md
package pages

import (
	"encoding/json"

	"balanciz/internal/models"
	"balanciz/internal/web/templates/ui"
)

// poShellVM maps PurchaseOrderDetailVM into the shared DocEditorShell
// wrapper used by the migrated PO editor.
func poShellVM(vm PurchaseOrderDetailVM) ui.DocEditorShellVM {
	title := "New Purchase Order"
	subtitle := "Create a new purchase order."
	if vm.PurchaseOrder.ID != 0 {
		title = "Purchase Order " + vm.PurchaseOrder.PONumber
		subtitle = "View and manage this purchase order."
	}
	return ui.DocEditorShellVM{
		Title:     title,
		Subtitle:  subtitle,
		BackURL:   "/purchase-orders",
		BackLabel: "Back to Purchase Orders",
		FormError: vm.FormError,
	}
}

// poFooterVM is the sticky bottom action bar for the PO editor.
//
// Layout — all action buttons consolidated into one row to remove the
// "two Cancels mean different things" confusion the duplicate
// poDraftActionButtons row used to cause:
//
//	Left:  Back to Purchase Orders (navigate, discards unsaved edits)
//	Right: [Cancel PO] [Save] [Confirm]   — only when the PO is a saved Draft
//
// New POs (ID == 0) only show Save — Confirm/Cancel-PO require an existing
// row to act on. Confirm + Cancel-PO use HTML5 `formaction` to override the
// editor form's POST URL; the handlers only read the URL :id, so the
// payload is harmless. Operator must click Save before Confirm to persist
// in-progress edits — matches the prior separate-form behaviour.
func poFooterVM(vm PurchaseOrderDetailVM) ui.DocEditorFooterVM {
	footer := ui.DocEditorFooterVM{
		Cancel: &ui.DocEditorFooterLink{
			Label: "Back to Purchase Orders",
			Href:  "/purchase-orders",
		},
	}

	// Draft + saved: cancel-PO (danger), save (primary), confirm (primary).
	// Order is "left = least desirable, right = main next step".
	if vm.PurchaseOrder.ID != 0 && vm.PurchaseOrder.Status == models.POStatusDraft {
		poURL := "/purchase-orders/" + Uitoa(vm.PurchaseOrder.ID)
		footer.Buttons = []ui.DocEditorFooterButton{
			{
				Label:      "Cancel PO",
				Variant:    ui.FooterBtnDanger,
				Type:       "submit",
				FormAction: poURL + "/cancel",
				OnClick:    "return confirm('Cancel this purchase order? This marks the order cancelled and cannot be undone.')",
			},
			{Label: "Save", Variant: ui.FooterBtnPrimary, Type: "submit"},
			{
				Label:      "Confirm",
				Variant:    ui.FooterBtnPrimary,
				Type:       "submit",
				FormAction: poURL + "/confirm",
				OnClick:    "return confirm('Confirm this purchase order? Save your edits first if you have unsaved changes.')",
			},
		}
		return footer
	}

	// New PO (ID == 0): only Save — confirm/cancel require an existing row.
	footer.Buttons = []ui.DocEditorFooterButton{
		{Label: "Save", Variant: ui.FooterBtnPrimary, Type: "submit"},
	}
	return footer
}

// poProductsJSON serialises the product/service catalogue for the
// docTransactionEditor's Alpine factory (auto-fills description / price
// when the operator picks an item via balancizItemPicker).
func poProductsJSON(products []models.ProductService) string {
	type row struct {
		ID               uint   `json:"id"`
		Name             string `json:"name"`
		ItemCode         string `json:"item_code"`
		Description      string `json:"description"`
		DefaultPrice     string `json:"default_price"`
		DefaultTaxCodeID *uint  `json:"default_tax_code_id"`
		ExpenseAccountID string `json:"expense_account_id"`
		AccountCode      string `json:"account_code"`
		AccountName      string `json:"account_name"`
	}
	out := make([]row, 0, len(products))
	for _, p := range products {
		accountID, accountCode, accountName := poProductPurchaseAccount(p)
		out = append(out, row{
			ID:               p.ID,
			Name:             p.Name,
			ItemCode:         p.SKU,
			Description:      p.Description,
			DefaultPrice:     p.DefaultPrice.StringFixed(2),
			DefaultTaxCodeID: p.DefaultTaxCodeID,
			ExpenseAccountID: accountID,
			AccountCode:      accountCode,
			AccountName:      accountName,
		})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func poProductPurchaseAccount(p models.ProductService) (string, string, string) {
	if p.InventoryAccountID != nil && *p.InventoryAccountID != 0 {
		return Uitoa(*p.InventoryAccountID), p.InventoryAccount.Code, p.InventoryAccount.Name
	}
	if p.COGSAccountID != nil && *p.COGSAccountID != 0 {
		return Uitoa(*p.COGSAccountID), p.COGSAccount.Code, p.COGSAccount.Name
	}
	return "", "", ""
}

// poTaxCodesJSON serialises tax codes. PO doesn't currently expose a per-line
// tax column in its editor, but the shared docTransactionEditor expects the
// dataset to be present (and it stays 0 when no code is bound), so we emit
// the catalogue for forward-compat.
func poTaxCodesJSON(codes []models.TaxCode) string {
	type row struct {
		ID   uint   `json:"id"`
		Code string `json:"code"`
		Rate string `json:"rate"`
	}
	out := make([]row, 0, len(codes))
	for _, tc := range codes {
		out = append(out, row{ID: tc.ID, Code: tc.Code, Rate: tc.Rate.String()})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// poInitialLinesJSON converts existing PurchaseOrderLines into the shape
// the docTransactionEditor's Alpine factory expects on edit-page hydration.
func poInitialLinesJSON(lines []models.PurchaseOrderLine) string {
	type row struct {
		ProductServiceID    string `json:"product_service_id"`
		ProductServiceLabel string `json:"product_service_label"`
		ProductServiceCode  string `json:"product_service_code"`
		ExpenseAccountID    string `json:"expense_account_id"`
		AccountCode         string `json:"account_code"`
		AccountName         string `json:"account_name"`
		AccountLabel        string `json:"account_label"`
		Description         string `json:"description"`
		Qty                 string `json:"qty"`
		UnitPrice           string `json:"unit_price"`
		TaxCodeID           string `json:"tax_code_id"`
		LineTotal           string `json:"line_total"`
	}
	out := make([]row, 0, len(lines))
	for _, l := range lines {
		r := row{
			Description: l.Description,
			Qty:         l.Qty.StringFixed(2),
			UnitPrice:   l.UnitPrice.StringFixed(2),
			LineTotal:   l.LineNet.StringFixed(2),
		}
		if l.ProductServiceID != nil {
			r.ProductServiceID = Uitoa(*l.ProductServiceID)
			if l.ProductService != nil {
				r.ProductServiceLabel = l.ProductService.Name
				r.ProductServiceCode = l.ProductService.SKU
				if accountID, accountCode, accountName := poProductPurchaseAccount(*l.ProductService); accountID != "" {
					r.ExpenseAccountID = accountID
					r.AccountCode = accountCode
					r.AccountName = accountName
				}
			}
		}
		if l.ExpenseAccountID != nil {
			r.ExpenseAccountID = Uitoa(*l.ExpenseAccountID)
			if l.ExpenseAccount != nil {
				r.AccountCode = l.ExpenseAccount.Code
				r.AccountName = l.ExpenseAccount.Name
			}
		}
		r.AccountLabel = poAccountLabel(r.AccountCode, r.AccountName)
		out = append(out, r)
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func poAccountLabel(code, name string) string {
	if code != "" && name != "" {
		return code + " " + name
	}
	if code != "" {
		return code
	}
	return name
}
