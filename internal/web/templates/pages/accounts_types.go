// 遵循project_guide.md
package pages

import (
	"encoding/json"

	"balanciz/internal/models"
)

// AccountsVM is the view-model for the Chart of Accounts page.
type AccountsVM struct {
	HasCompany bool
	Active     string

	// Form fields
	Code         string
	Name         string
	Root         string
	Detail       string
	GifiCode     string
	CurrencyCode string

	// Form-level + field-level errors
	FormError     string
	CodeError     string
	NameError     string
	RootError     string
	DetailError   string
	GifiError     string
	CurrencyError string

	// Success banner
	Created    bool
	Updated    bool
	InactiveOK bool

	// DrawerMode is "create", "edit", or empty (drawer closed on first paint unless AccountDrawerOpen).
	DrawerMode string
	// EditingAccountID is set when DrawerMode == "edit".
	EditingAccountID uint

	// AccountDrawerOpen opens the account slide-over when true.
	AccountDrawerOpen bool

	// AccountCodeLength is the company's configured exact digit width (4–12) for new/edited account codes.
	AccountCodeLength int

	// ActiveCompanyID is the session company (for API calls from the account drawer).
	ActiveCompanyID uint

	// Multi-currency controls the conditional account currency selector.
	MultiCurrencyEnabled bool
	BaseCurrencyCode     string
	CurrencyOptions      []string
	CurrencyLocked       bool

	// Data to render the table
	Accounts []models.Account
}

// AccountsEffectiveCodeLen returns vm.AccountCodeLength when valid, else 4.
func AccountsEffectiveCodeLen(vm AccountsVM) int {
	n := vm.AccountCodeLength
	if n >= models.AccountCodeLengthMin && n <= models.AccountCodeLengthMax {
		return n
	}
	return models.AccountCodeLengthMin
}

// RootAccountSelectOptions returns root labels for the first dropdown.
func RootAccountSelectOptions() []models.RootAccountType {
	return models.AllRootAccountTypes()
}

// RootPrefixMapJSON maps root_account_type string → required first digit (for Alpine prefix check).
func RootPrefixMapJSON() string {
	m := make(map[string]string)
	for _, r := range models.AllRootAccountTypes() {
		d, err := models.RootRequiredPrefixDigit(r)
		if err != nil {
			continue
		}
		m[string(r)] = string(d)
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// DetailOptionsByRootJSON maps root → list of detail values for dependent select.
func DetailOptionsByRootJSON() string {
	out := make(map[string][]string)
	for _, r := range models.AllRootAccountTypes() {
		var ds []string
		for _, d := range models.DetailsForRoot(r) {
			ds = append(ds, string(d))
		}
		out[string(r)] = ds
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// CurrencyAccountDetailTypesJSON returns detail types that carry an account-level currency.
func CurrencyAccountDetailTypesJSON() string {
	values := []string{
		string(models.DetailBank),
		string(models.DetailOtherCurrentAsset),
		string(models.DetailCreditCard),
		string(models.DetailAccountsReceivable),
		string(models.DetailAccountsPayable),
	}
	b, _ := json.Marshal(values)
	return string(b)
}

func AccountCurrencyOptions(vm AccountsVM) []string {
	if len(vm.CurrencyOptions) > 0 {
		return vm.CurrencyOptions
	}
	if vm.BaseCurrencyCode != "" {
		return []string{vm.BaseCurrencyCode}
	}
	return []string{"CAD"}
}
