// 遵循project_guide.md
package pages

import "encoding/json"

// expenseInitialLinesJSON serialises vm.Lines into a JSON array consumed by the
// balancizExpenseForm Alpine component via data-initial-lines.
// Shape per item: {expense_account_id, description, amount, tax_code_id, line_tax,
//                  line_total, task_id, is_billable, error}.
func expenseInitialLinesJSON(vm ExpenseFormVM) string {
	type lineJSON struct {
		ExpenseAccountID string `json:"expense_account_id"`
		ProductServiceID string `json:"product_service_id"`
		Description      string `json:"description"`
		Amount           string `json:"amount"`
		TaxCodeID        string `json:"tax_code_id"`
		LineTax          string `json:"line_tax"`
		LineTotal        string `json:"line_total"`
		TaskID           string `json:"task_id"`
		IsBillable       bool   `json:"is_billable"`
		Error            string `json:"error"`
	}
	items := make([]lineJSON, 0, len(vm.Lines))
	for _, l := range vm.Lines {
		lineTotal := l.LineTotal
		if lineTotal == "" {
			lineTotal = l.Amount // fallback: no tax means total = amount
		}
		items = append(items, lineJSON{
			ExpenseAccountID: l.ExpenseAccountID,
			ProductServiceID: l.ProductServiceID,
			Description:      l.Description,
			Amount:           l.Amount,
			TaxCodeID:        l.TaxCodeID,
			LineTax:          l.LineTax,
			LineTotal:        lineTotal,
			TaskID:           l.TaskID,
			IsBillable:       l.IsBillable,
			Error:            l.Error,
		})
	}
	b, _ := json.Marshal(items)
	return string(b)
}
