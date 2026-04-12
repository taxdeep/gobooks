// 遵循project_guide.md
package pages

import "encoding/json"

// expenseInitialLinesJSON serialises vm.Lines into a JSON array consumed by the
// gobooksExpenseForm Alpine component via data-initial-lines.
// Shape per item: {expense_account_id, description, amount, error}.
func expenseInitialLinesJSON(vm ExpenseFormVM) string {
	type lineJSON struct {
		ExpenseAccountID string `json:"expense_account_id"`
		Description      string `json:"description"`
		Amount           string `json:"amount"`
		Error            string `json:"error"`
	}
	items := make([]lineJSON, 0, len(vm.Lines))
	for _, l := range vm.Lines {
		items = append(items, lineJSON{
			ExpenseAccountID: l.ExpenseAccountID,
			Description:      l.Description,
			Amount:           l.Amount,
			Error:            l.Error,
		})
	}
	b, _ := json.Marshal(items)
	return string(b)
}
