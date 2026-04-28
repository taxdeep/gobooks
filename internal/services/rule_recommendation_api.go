// 遵循project_guide.md
package services

import (
	"fmt"
	"strings"

	"balanciz/internal/models"

	"gorm.io/gorm"
)

// AccountRecommendationRequest is the unified JSON body for POST /api/accounts/recommendations.
// Enhance=false (default): deterministic rules only. Enhance=true: rule engine then optional AI refinement (validated server-side).
type AccountRecommendationRequest struct {
	CompanyID           uint   `json:"company_id"`
	RootAccountType     string `json:"root_account_type"`
	DetailAccountType   string `json:"detail_account_type"`
	AccountName         string `json:"account_name"`
	ExistingAccountCode string `json:"existing_account_code"`
	ParentAccountID     uint   `json:"parent_account_id"`
	CodeLength          int    `json:"code_length"`
	Enhance             bool   `json:"enhance"`
}

// AccountRecommendationResponse is the unified response for all account recommendation APIs.
type AccountRecommendationResponse struct {
	SuggestedAccountName string `json:"suggested_account_name"`
	SuggestedAccountCode string `json:"suggested_account_code"`
	SuggestedGifiCode    string `json:"suggested_gifi_code"`
	GifiDescription      string `json:"gifi_description,omitempty"`
	Confidence           string `json:"confidence"`
	Reason               string `json:"reason"`
	Source               string `json:"source"` // rule | ai
	AIUnavailable        bool   `json:"ai_unavailable,omitempty"`
}

// RuleRecommendationRequest is an alias for backward compatibility (enhance omitted in JSON defaults to false).
type RuleRecommendationRequest = AccountRecommendationRequest

// RuleRecommendationResponse is an alias for backward compatibility.
type RuleRecommendationResponse = AccountRecommendationResponse

// RecommendAccount is the single entry point: rule-only or rule+optional AI.
func RecommendAccount(db *gorm.DB, companyID uint, req AccountRecommendationRequest) (AccountRecommendationResponse, error) {
	if !req.Enhance {
		return ComputeRuleBasedRecommendation(db, companyID, req)
	}
	return RecommendAccountPipeline(db, companyID, req)
}

// ComputeRuleBasedRecommendation runs the deterministic rule engine only (ignores Enhance).
func ComputeRuleBasedRecommendation(db *gorm.DB, companyID uint, req AccountRecommendationRequest) (AccountRecommendationResponse, error) {
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

	in := AccountRecommendationInput{
		RootAccountType:    string(root),
		DetailAccountType:  string(detail),
		AccountName:        strings.TrimSpace(req.AccountName),
		ParentAccountCode:  parentCode,
		CurrentAccountCode: "",
	}

	core, err := computeRuleRecommendationCore(db, companyID, codeLen, in, root, detail)
	if err != nil {
		return AccountRecommendationResponse{}, err
	}

	return AccountRecommendationResponse{
		SuggestedAccountName: core.Name,
		SuggestedAccountCode: core.Code,
		SuggestedGifiCode:    core.GifiCode,
		GifiDescription:      core.GifiDescription,
		Confidence:           core.Confidence,
		Source:               "rule",
		Reason:               core.Reason,
	}, nil
}

// ResolveParentAccountCode resolves optional parent account code for grouping (v1-safe).
func ResolveParentAccountCode(db *gorm.DB, companyID uint, parentAccountID uint, codeLen int, existingAccountCode string) string {
	parentCode := strings.TrimSpace(existingAccountCode)
	if parentCode != "" && len(parentCode) != codeLen {
		parentCode = ""
	}
	if parentAccountID != 0 {
		var pa models.Account
		if err := db.Where("id = ? AND company_id = ?", parentAccountID, companyID).First(&pa).Error; err == nil {
			if len(pa.Code) == codeLen {
				parentCode = pa.Code
			}
		}
	}
	return parentCode
}
