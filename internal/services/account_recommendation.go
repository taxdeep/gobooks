// 遵循project_guide.md
package services

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"balanciz/internal/models"

	"gorm.io/gorm"
)

// AccountRecommendation is a rule-based (optionally AI-refined) suggestion; never persisted automatically.
type AccountRecommendation struct {
	SuggestedGifiCode    string  `json:"suggested_gifi_code"`
	SuggestedAccountName string  `json:"suggested_account_name"`
	SuggestedAccountCode string  `json:"suggested_account_code"`
	Confidence           float64 `json:"confidence"`
	Source               string  `json:"source"`
	Reason               string  `json:"reason"`
	AIEnhanced           bool    `json:"ai_enhanced"`
}

// AccountRecommendationInput is the JSON body for POST /accounts/suggestions.
type AccountRecommendationInput struct {
	RootAccountType    string `json:"root_account_type"`
	DetailAccountType  string `json:"detail_account_type"`
	AccountName        string `json:"account_name"`
	CurrentAccountCode string `json:"current_account_code"`
	ParentAccountCode  string `json:"parent_account_code"`
}

// RuleBasedAccountRecommendation computes deterministic suggestions from chart context.
func RuleBasedAccountRecommendation(db *gorm.DB, companyID uint, codeLen int, in AccountRecommendationInput) (AccountRecommendation, error) {
	root, err := models.ParseRootAccountType(in.RootAccountType)
	if err != nil {
		return AccountRecommendation{}, err
	}
	detail, err := models.ParseDetailAccountType(in.DetailAccountType)
	if err != nil {
		return AccountRecommendation{}, err
	}
	if err := models.ValidateRootDetail(root, detail); err != nil {
		return AccountRecommendation{}, err
	}

	core, err := computeRuleRecommendationCore(db, companyID, codeLen, in, root, detail)
	if err != nil {
		return AccountRecommendation{}, err
	}

	return AccountRecommendation{
		SuggestedGifiCode:    core.GifiCode,
		SuggestedAccountName: core.Name,
		SuggestedAccountCode: core.Code,
		Confidence:           confidenceTierToFloat(core.Confidence),
		Source:               "rule",
		Reason:               core.Reason,
		AIEnhanced:           false,
	}, nil
}

type ruleRecommendationCore struct {
	Name            string
	Code            string
	GifiCode        string
	GifiDescription string
	Confidence      string
	Reason          string
}

func computeRuleRecommendationCore(db *gorm.DB, companyID uint, codeLen int, in AccountRecommendationInput, root models.RootAccountType, detail models.DetailAccountType) (ruleRecommendationCore, error) {
	name, nameReason := suggestAccountNameNormalized(in.AccountName, detail)

	gifiCode, gifiDesc := ruleGifiLookup(root, detail)
	gifiReason := ""
	if gifiCode != "" {
		if gifiDesc != "" {
			gifiReason = "GIFI " + gifiCode + ": " + gifiDesc + "."
		} else {
			gifiReason = "CRA GIFI mapping for this classification."
		}
	}

	sameDetailCount, usedPrefixFallback, err := groupStats(db, companyID, codeLen, root, detail)
	if err != nil {
		return ruleRecommendationCore{}, err
	}

	code, codeReason, err := suggestNextAccountCode(db, companyID, codeLen, root, detail, strings.TrimSpace(in.ParentAccountCode))
	if err != nil {
		return ruleRecommendationCore{}, err
	}

	conf := ruleConfidenceString(gifiCode != "", sameDetailCount, usedPrefixFallback)

	out := ruleRecommendationCore{
		Name:            name,
		Code:            code,
		GifiCode:        gifiCode,
		GifiDescription: gifiDesc,
		Confidence:      conf,
		Reason:            joinReasonParts(nameReason, gifiReason, codeReason),
	}
	return out, nil
}

func confidenceTierToFloat(tier string) float64 {
	switch tier {
	case "high":
		return 0.92
	case "medium":
		return 0.72
	default:
		return 0.48
	}
}

// suggestAccountNameNormalized trims and collapses whitespace; keeps user intent if non-empty.
func suggestAccountNameNormalized(raw string, detail models.DetailAccountType) (name string, reason string) {
	s := strings.TrimSpace(raw)
	s = strings.Join(strings.Fields(s), " ")
	if s != "" {
		return s, "Normalized user-provided name."
	}
	return models.DetailSnakeToLabel(string(detail)), "Default name based on detail type."
}

func joinReasonParts(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString(p)
	}
	return strings.TrimSpace(b.String())
}

func ruleConfidenceString(hasGifi bool, sameDetailCount int, usedPrefixFallback bool) string {
	switch {
	case hasGifi && sameDetailCount >= 2:
		return "high"
	case hasGifi && sameDetailCount >= 1:
		return "high"
	case sameDetailCount >= 2:
		return "high"
	case hasGifi:
		return "medium"
	case sameDetailCount >= 1:
		return "medium"
	case usedPrefixFallback:
		return "medium"
	default:
		return "low"
	}
}

func groupStats(db *gorm.DB, companyID uint, codeLen int, root models.RootAccountType, detail models.DetailAccountType) (sameDetailCount int, usedPrefixFallback bool, err error) {
	prefix, err := models.RootRequiredPrefixDigit(root)
	if err != nil {
		return 0, false, err
	}
	type accRow struct {
		Code   string
		Detail string
	}
	var rows []accRow
	if err := db.Model(&models.Account{}).
		Select("code", "detail_account_type AS detail").
		Where("company_id = ?", companyID).
		Scan(&rows).Error; err != nil {
		return 0, false, err
	}

	var sameDetail, samePrefix int
	detailStr := string(detail)
	for _, r := range rows {
		if len(r.Code) != codeLen || r.Code[0] != prefix {
			continue
		}
		samePrefix++
		if r.Detail == detailStr {
			sameDetail++
		}
	}
	usedPrefixFallback = sameDetail == 0 && samePrefix > 0
	return sameDetail, usedPrefixFallback, nil
}

type gifiKey struct {
	r models.RootAccountType
	d models.DetailAccountType
}

// ruleGifiLookup returns deterministic CRA-style GIFI mapping and a short description.
func ruleGifiLookup(root models.RootAccountType, detail models.DetailAccountType) (code string, description string) {
	m := map[gifiKey]struct {
		code string
		desc string
	}{
		// Assets
		{models.RootAsset, models.DetailBank}:                   {"1001", "Cash and deposits"},
		{models.RootAsset, models.DetailAccountsReceivable}:   {"1060", "Accounts receivable"},
		{models.RootAsset, models.DetailInventory}:            {"1120", "Inventory"},
		{models.RootAsset, models.DetailPrepaidExpense}:       {"1480", "Prepaid expenses"},
		{models.RootAsset, models.DetailOtherCurrentAsset}:    {"1480", "Other current assets"},
		{models.RootAsset, models.DetailFixedAsset}:           {"1600", "Capital assets"},
		{models.RootAsset, models.DetailOtherAsset}:           {"1600", "Other assets"},
		// Liabilities
		{models.RootLiability, models.DetailAccountsPayable}:       {"2620", "Accounts payable"},
		{models.RootLiability, models.DetailCreditCard}:            {"2105", "Credit card payable"},
		{models.RootLiability, models.DetailOtherCurrentLiability}: {"2620", "Other current liabilities"},
		{models.RootLiability, models.DetailLongTermLiability}:     {"2700", "Long-term debt"},
		{models.RootLiability, models.DetailSalesTaxPayable}:       {"2290", "Sales taxes payable"},
		{models.RootLiability, models.DetailPayrollLiability}:      {"2240", "Payroll deductions payable"},
		// Equity
		{models.RootEquity, models.DetailShareCapital}:      {"3500", "Share capital"},
		{models.RootEquity, models.DetailRetainedEarnings}:  {"3600", "Retained earnings"},
		{models.RootEquity, models.DetailOwnerContribution}: {"3500", "Owner contributions"},
		{models.RootEquity, models.DetailOwnerDrawings}:    {"3500", "Owner drawings / withdrawals"},
		{models.RootEquity, models.DetailOtherEquity}:      {"3500", "Other equity"},
		// Revenue
		{models.RootRevenue, models.DetailOperatingRevenue}: {"8000", "Operating revenue"},
		{models.RootRevenue, models.DetailServiceRevenue}:   {"8290", "Service revenue"},
		{models.RootRevenue, models.DetailSalesRevenue}:     {"8000", "Sales revenue"},
		{models.RootRevenue, models.DetailOtherIncome}:      {"8230", "Other income"},
		// Cost of sales
		{models.RootCostOfSales, models.DetailCostOfGoodsSold}: {"8518", "Cost of goods sold"},
		// Expenses
		{models.RootExpense, models.DetailOperatingExpense}:   {"8690", "Operating expenses"},
		{models.RootExpense, models.DetailOfficeExpense}:        {"8810", "Office expenses"},
		{models.RootExpense, models.DetailRentExpense}:          {"8910", "Rent"},
		{models.RootExpense, models.DetailUtilitiesExpense}:     {"8870", "Utilities"},
		{models.RootExpense, models.DetailPayrollExpense}:       {"8670", "Salaries and wages"},
		{models.RootExpense, models.DetailProfessionalFees}:     {"8860", "Professional fees"},
		{models.RootExpense, models.DetailBankCharges}:          {"8710", "Interest and bank charges"},
		{models.RootExpense, models.DetailAdvertisingExpense}:   {"8521", "Advertising"},
		{models.RootExpense, models.DetailInsuranceExpense}:     {"8690", "Insurance"},
		{models.RootExpense, models.DetailOtherExpense}:         {"9270", "Other expenses"},
	}
	if v, ok := m[gifiKey{root, detail}]; ok {
		return v.code, v.desc
	}
	return "", ""
}

func ruleGifiCode(root models.RootAccountType, detail models.DetailAccountType) string {
	c, _ := ruleGifiLookup(root, detail)
	return c
}

func suggestNextAccountCode(db *gorm.DB, companyID uint, codeLen int, root models.RootAccountType, detail models.DetailAccountType, parentCode string) (string, string, error) {
	prefix, err := models.RootRequiredPrefixDigit(root)
	if err != nil {
		return "", "", err
	}

	type accRow struct {
		Code   string
		Detail string
	}
	var rows []accRow
	if err := db.Model(&models.Account{}).
		Select("code", "detail_account_type AS detail").
		Where("company_id = ?", companyID).
		Scan(&rows).Error; err != nil {
		return "", "", err
	}

	var sameDetailNums []int64
	var samePrefixNums []int64
	detailStr := string(detail)
	for _, r := range rows {
		if len(r.Code) != codeLen || r.Code[0] != prefix {
			continue
		}
		n, err := strconv.ParseInt(r.Code, 10, 64)
		if err != nil {
			continue
		}
		samePrefixNums = append(samePrefixNums, n)
		if r.Detail == detailStr {
			sameDetailNums = append(sameDetailNums, n)
		}
	}

	use := sameDetailNums
	if len(use) == 0 {
		use = samePrefixNums
	}

	step := inferStep(use)
	if step < 1 {
		step = 1
	}

	occupied := make(map[int64]struct{})
	for _, r := range rows {
		if len(r.Code) != codeLen {
			continue
		}
		n, err := strconv.ParseInt(r.Code, 10, 64)
		if err != nil {
			continue
		}
		occupied[n] = struct{}{}
	}

	maxVal := pow10(codeLen) - 1
	minVal := pow10(codeLen-1) * int64(prefix-'0')

	// Parent account: next slot under that parent (step-aligned), scanning forward only.
	if parentCode != "" && len(parentCode) == codeLen {
		if parentN, err := strconv.ParseInt(parentCode, 10, 64); err == nil {
			if s, reason, ok := advanceFromCandidate(parentN+int64(step), step, prefix, codeLen, root, occupied, minVal, maxVal); ok {
				return s, reason, nil
			}
		}
	}

	if len(use) == 0 {
		seed := defaultSeedNumeric(prefix, codeLen)
		if s, reason, ok := advanceFromCandidate(seed, step, prefix, codeLen, root, occupied, minVal, maxVal); ok {
			return s, reason, nil
		}
		return "", "", fmt.Errorf("could not find an available account code")
	}

	// Prefer the first gap in the sorted grouping, then the next slot after the highest code.
	if s, reason, ok := firstAvailableInGrouping(use, step, prefix, codeLen, root, occupied, minVal, maxVal); ok {
		return s, reason, nil
	}

	return "", "", fmt.Errorf("could not find an available account code")
}

// firstAvailableInGrouping picks the smallest step-aligned free code by scanning existing
// company accounts in the same group (same detail, else same root prefix): gaps between
// sorted codes first, then continuation after the maximum.
func firstAvailableInGrouping(
	use []int64,
	step int,
	prefix byte,
	codeLen int,
	root models.RootAccountType,
	occupied map[int64]struct{},
	minVal, maxVal int64,
) (string, string, bool) {
	if step < 1 {
		step = 1
	}
	sort.Slice(use, func(i, j int) bool { return use[i] < use[j] })
	for i := 0; i < len(use)-1; i++ {
		lo, hi := use[i], use[i+1]
		for n := lo + int64(step); n < hi; n += int64(step) {
			if s, ok := tryCode(n, prefix, codeLen, root, occupied, minVal, maxVal); ok {
				return s, fmt.Sprintf("Next available code in the %s range (filled gap in this group; step %d).", root, step), true
			}
		}
	}
	last := use[len(use)-1]
	return advanceFromCandidate(last+int64(step), step, prefix, codeLen, root, occupied, minVal, maxVal)
}

func tryCode(n int64, prefix byte, codeLen int, root models.RootAccountType, occupied map[int64]struct{}, minVal, maxVal int64) (string, bool) {
	if n < minVal || n > maxVal {
		return "", false
	}
	if _, taken := occupied[n]; taken {
		return "", false
	}
	s := fmt.Sprintf("%0*d", codeLen, n)
	if len(s) != codeLen || s[0] != prefix {
		return "", false
	}
	if err := models.ValidateAccountCodeAndClassification(s, codeLen, root); err != nil {
		return "", false
	}
	return s, true
}

// advanceFromCandidate walks forward by step until a valid free code is found (bounded attempts).
func advanceFromCandidate(
	start int64,
	step int,
	prefix byte,
	codeLen int,
	root models.RootAccountType,
	occupied map[int64]struct{},
	minVal, maxVal int64,
) (string, string, bool) {
	if step < 1 {
		step = 1
	}
	for tries, n := 0, start; tries < 10000; tries, n = tries+1, n+int64(step) {
		if n < minVal || n > maxVal {
			break
		}
		if s, ok := tryCode(n, prefix, codeLen, root, occupied, minVal, maxVal); ok {
			return s, fmt.Sprintf("Next available code in the %s range (step %d).", root, step), true
		}
	}
	return "", "", false
}

func pow10(n int) int64 {
	v := int64(1)
	for i := 0; i < n; i++ {
		v *= 10
	}
	return v
}

func inferStep(nums []int64) int {
	if len(nums) < 2 {
		return 100
	}
	sort.Slice(nums, func(i, j int) bool { return nums[i] < nums[j] })
	var diffs []int64
	for i := 1; i < len(nums); i++ {
		d := nums[i] - nums[i-1]
		if d > 0 {
			diffs = append(diffs, d)
		}
	}
	if len(diffs) == 0 {
		return 100
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i] < diffs[j] })
	return int(diffs[len(diffs)/2])
}

func defaultSeedNumeric(prefix byte, codeLen int) int64 {
	buf := strings.Builder{}
	buf.WriteByte(prefix)
	for i := 0; i < codeLen-1; i++ {
		buf.WriteByte('0')
	}
	n, _ := strconv.ParseInt(buf.String(), 10, 64)
	return n
}
