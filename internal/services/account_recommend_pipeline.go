// 遵循project_guide.md
package services

import (
	"encoding/json"
	"fmt"
	"strings"

	"balanciz/internal/models"

	"gorm.io/gorm"
)

type aiAccountSuggestPayload struct {
	SuggestedAccountName string `json:"suggested_account_name"`
	SuggestedAccountCode string `json:"suggested_account_code"`
	SuggestedGifiCode    string `json:"suggested_gifi_code"`
	Confidence           string `json:"confidence"`
	Reason               string `json:"reason"`
}

// RecommendAccountPipeline runs rule-based recommendations, optionally asks AI to refine, then re-validates and falls back on any invalid AI output.
func RecommendAccountPipeline(db *gorm.DB, companyID uint, req AccountRecommendationRequest) (AccountRecommendationResponse, error) {
	codeLen := req.CodeLength
	if codeLen < models.AccountCodeLengthMin || codeLen > models.AccountCodeLengthMax {
		return AccountRecommendationResponse{}, fmt.Errorf("code_length must be between %d and %d", models.AccountCodeLengthMin, models.AccountCodeLengthMax)
	}

	root, err := models.ParseRootAccountType(req.RootAccountType)
	if err != nil {
		return AccountRecommendationResponse{}, err
	}
	detail, err := models.ParseDetailAccountType(req.DetailAccountType)
	if err != nil {
		return AccountRecommendationResponse{}, err
	}
	if err := models.ValidateRootDetail(root, detail); err != nil {
		return AccountRecommendationResponse{}, err
	}

	parentCode := ResolveParentAccountCode(db, companyID, req.ParentAccountID, codeLen, req.ExistingAccountCode)

	ruleIn := AccountRecommendationInput{
		RootAccountType:    string(root),
		DetailAccountType:  string(detail),
		AccountName:        strings.TrimSpace(req.AccountName),
		ParentAccountCode:  parentCode,
		CurrentAccountCode: "",
	}

	base, err := RuleBasedAccountRecommendation(db, companyID, codeLen, ruleIn)
	if err != nil {
		return AccountRecommendationResponse{}, err
	}

	out := AccountRecommendationResponse{
		SuggestedAccountName: base.SuggestedAccountName,
		SuggestedAccountCode: base.SuggestedAccountCode,
		SuggestedGifiCode:    base.SuggestedGifiCode,
		Confidence:           ruleConfidenceToAPI(base),
		Reason:               strings.TrimSpace(base.Reason),
		Source:               "rule",
	}
	if out.Reason == "" {
		out.Reason = "Rule-based recommendation."
	}
	fillAccountRecommendAPIGifiDescription(&out, root, detail)

	aiRow, err := LoadAIConnectionSettings(db, companyID)
	if err != nil {
		out.AIUnavailable = true
		return out, nil
	}
	if !aiRow.Enabled || strings.TrimSpace(aiRow.APIKey) == "" || strings.TrimSpace(aiRow.APIBaseURL) == "" {
		out.AIUnavailable = true
		return out, nil
	}

	similarJSON, _ := topSimilarAccountsJSON(db, companyID, root, 5)
	userMsg := buildAIUserMessage(req, codeLen, base, similarJSON)
	systemMsg := aiAccountSystemPrompt()

	aiRaw, err := OpenAICompatibleChatCompletion(aiRow, userMsg, systemMsg)
	if err != nil {
		out.AIUnavailable = true
		return out, nil
	}

	var aiParsed aiAccountSuggestPayload
	if err := json.Unmarshal([]byte(aiRaw), &aiParsed); err != nil {
		out.AIUnavailable = true
		return out, nil
	}

	merged := out
	merged.Source = "ai"
	merged.AIUnavailable = false
	if strings.TrimSpace(aiParsed.SuggestedAccountName) != "" {
		merged.SuggestedAccountName = strings.TrimSpace(aiParsed.SuggestedAccountName)
	}
	if strings.TrimSpace(aiParsed.SuggestedAccountCode) != "" {
		merged.SuggestedAccountCode = strings.TrimSpace(aiParsed.SuggestedAccountCode)
	}
	if strings.TrimSpace(aiParsed.SuggestedGifiCode) != "" {
		merged.SuggestedGifiCode = strings.TrimSpace(aiParsed.SuggestedGifiCode)
	}
	if strings.TrimSpace(aiParsed.Reason) != "" {
		merged.Reason = strings.TrimSpace(aiParsed.Reason) + " (AI suggestion; validated server-side.)"
	} else {
		merged.Reason = out.Reason + " AI refinement applied where valid."
	}
	if c := strings.ToLower(strings.TrimSpace(aiParsed.Confidence)); c == "high" || c == "medium" || c == "low" {
		merged.Confidence = c
	} else {
		merged.Confidence = "medium"
	}

	safe := sanitizeAccountRecommend(db, companyID, codeLen, root, merged, base)
	if strings.TrimSpace(safe.Reason) == "" {
		safe.Reason = "Validated recommendation."
	}
	fillAccountRecommendAPIGifiDescription(&safe, root, detail)
	return safe, nil
}

// fillAccountRecommendAPIGifiDescription sets gifi_description when the suggested code matches the rule map for this classification.
func fillAccountRecommendAPIGifiDescription(out *AccountRecommendationResponse, root models.RootAccountType, detail models.DetailAccountType) {
	c, d := ruleGifiLookup(root, detail)
	if out.SuggestedGifiCode != "" && c == out.SuggestedGifiCode && d != "" {
		out.GifiDescription = d
	} else {
		out.GifiDescription = ""
	}
}

func ruleConfidenceToAPI(base AccountRecommendation) string {
	if base.Confidence >= 0.88 {
		return "high"
	}
	if base.Confidence >= 0.55 {
		return "medium"
	}
	return "low"
}

func sanitizeAccountRecommend(db *gorm.DB, companyID uint, codeLen int, root models.RootAccountType, merged AccountRecommendationResponse, ruleFallback AccountRecommendation) AccountRecommendationResponse {
	out := merged

	if err := models.ValidateAccountCodeAndClassification(out.SuggestedAccountCode, codeLen, root); err != nil {
		out.SuggestedAccountCode = ruleFallback.SuggestedAccountCode
		out.Reason += " Account code reset to rule-based (AI code failed validation)."
		out.Confidence = "medium"
	} else if codeTaken(db, companyID, out.SuggestedAccountCode) {
		out.SuggestedAccountCode = ruleFallback.SuggestedAccountCode
		out.Reason += " Account code reset to rule-based (AI code not unique)."
		out.Confidence = "medium"
	}

	if err := models.ValidateGifiCode(out.SuggestedGifiCode); err != nil {
		out.SuggestedGifiCode = ruleFallback.SuggestedGifiCode
		out.Reason += " GIFI reset to rule-based (AI GIFI failed validation)."
	}

	if strings.TrimSpace(out.SuggestedAccountName) == "" {
		out.SuggestedAccountName = ruleFallback.SuggestedAccountName
	}

	if out.Confidence != "high" && out.Confidence != "medium" && out.Confidence != "low" {
		out.Confidence = "medium"
	}

	return out
}

func codeTaken(db *gorm.DB, companyID uint, code string) bool {
	if code == "" {
		return true
	}
	var n int64
	_ = db.Model(&models.Account{}).Where("company_id = ? AND code = ?", companyID, code).Count(&n).Error
	return n > 0
}

func topSimilarAccountsJSON(db *gorm.DB, companyID uint, root models.RootAccountType, limit int) (string, error) {
	var rows []models.Account
	if err := db.Where("company_id = ? AND root_account_type = ?", companyID, string(root)).
		Order("code asc").Limit(limit).Find(&rows).Error; err != nil {
		return "[]", err
	}
	type slim struct {
		Code   string `json:"code"`
		Name   string `json:"name"`
		Detail string `json:"detail_account_type"`
	}
	out := make([]slim, 0, len(rows))
	for _, a := range rows {
		out = append(out, slim{Code: a.Code, Name: a.Name, Detail: string(a.DetailAccountType)})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "[]", err
	}
	return string(b), nil
}

func buildAIUserMessage(req AccountRecommendationRequest, codeLen int, base AccountRecommendation, similarJSON string) string {
	b, err := json.Marshal(map[string]any{
		"account_name":          strings.TrimSpace(req.AccountName),
		"root_account_type":     req.RootAccountType,
		"detail_account_type":   req.DetailAccountType,
		"code_length":           codeLen,
		"existing_account_code": strings.TrimSpace(req.ExistingAccountCode),
		"parent_account_id":     req.ParentAccountID,
		"rule_based_suggestion": map[string]any{
			"suggested_account_name": base.SuggestedAccountName,
			"suggested_account_code": base.SuggestedAccountCode,
			"suggested_gifi_code":    base.SuggestedGifiCode,
		},
		"similar_accounts_sample": json.RawMessage(similarJSON),
	})
	if err != nil {
		return "{}"
	}
	return string(b)
}

func aiAccountSystemPrompt() string {
	return `You are assisting with chart-of-accounts setup for a Canadian company using CRA GIFI references.

Your task: refine the rule-based suggestion (names, numeric account code, 4-digit GIFI) when helpful.

Hard rules (must never be violated in your output):
- suggested_account_code must be numeric only, exactly the given code_length, first digit 1-6 matching root (1=asset,2=liability,3=equity,4=revenue,5=cost_of_sales,6=expense), no leading zero, positive integer as string.
- suggested_gifi_code must be empty or exactly 4 digits.
- suggested_account_name must be non-empty, concise.

Return JSON only with keys: suggested_account_name, suggested_account_code, suggested_gifi_code, confidence ("high"|"medium"|"low"), reason (short).`
}
