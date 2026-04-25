// 遵循project_guide.md
package pages

// depositLiabilityAccountIDValue formats a *uint id for the hidden form
// input on the deposit detail page. Returns "" when nil so the POST
// handler treats it as "no override — auto-resolve at post time".
func depositLiabilityAccountIDValue(id *uint) string {
	if id == nil || *id == 0 {
		return ""
	}
	return Uitoa(*id)
}
