// 遵循project_guide.md
package services

import (
	"fmt"
	"testing"

	"gobooks/internal/models"
)

// TestDefaultTemplateAccountsValidation verifies every account in the
// in-memory template passes code format, prefix, and root/detail rules.
// Also checks for duplicate codes. This test is DB-free and fast.
func TestDefaultTemplateAccountsValidation(t *testing.T) {
	const testLength = 4
	seen := map[string]bool{}

	for _, a := range defaultTemplateAccounts {
		name := fmt.Sprintf("%s (%s)", a.AccountCode, a.Name)

		// 1. Code must expand cleanly to the target length.
		expanded, err := models.ExpandAccountCodeToLength(a.AccountCode, testLength)
		if err != nil {
			t.Errorf("%s: ExpandAccountCodeToLength: %v", name, err)
			continue
		}

		// 2. Expanded code must pass strict format + prefix-for-root.
		if err := models.ValidateAccountCodeAndClassification(expanded, testLength, a.RootAccountType); err != nil {
			t.Errorf("%s: ValidateAccountCodeAndClassification: %v", name, err)
		}

		// 3. Root/detail pair must be a registered combination.
		if err := models.ValidateRootDetail(a.RootAccountType, a.DetailAccountType); err != nil {
			t.Errorf("%s: ValidateRootDetail: %v", name, err)
		}

		// 4. NormalBalance must be one of the two recognised values.
		if a.NormalBalance != models.NormalBalanceDebit && a.NormalBalance != models.NormalBalanceCredit {
			t.Errorf("%s: invalid NormalBalance %q", name, a.NormalBalance)
		}

		// 5. No duplicate 4-digit base codes within the template.
		if seen[a.AccountCode] {
			t.Errorf("%s: duplicate account code in template", name)
		}
		seen[a.AccountCode] = true
	}

	t.Logf("Validated %d template accounts (%d unique codes)", len(defaultTemplateAccounts), len(seen))
}

// TestDefaultTemplateAccountsCodeLength verifies expansion works for all
// supported company code lengths (4 through 12).
func TestDefaultTemplateAccountsCodeLength(t *testing.T) {
	for length := models.AccountCodeLengthMin; length <= models.AccountCodeLengthMax; length++ {
		for _, a := range defaultTemplateAccounts {
			expanded, err := models.ExpandAccountCodeToLength(a.AccountCode, length)
			if err != nil {
				t.Errorf("length %d, code %s: expand: %v", length, a.AccountCode, err)
				continue
			}
			if err := models.ValidateAccountCodeAndClassification(expanded, length, a.RootAccountType); err != nil {
				t.Errorf("length %d, code %s: validate: %v", length, a.AccountCode, err)
			}
		}
	}
}
