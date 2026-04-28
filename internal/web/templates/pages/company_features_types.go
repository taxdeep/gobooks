// 遵循project_guide.md
package pages

import (
	"time"

	"github.com/google/uuid"

	"balanciz/internal/models"
)

// CompanyFeaturesVM drives the Features page. One row per registered
// feature. Includes all data the page needs; the enable modal reads
// its copy from the corresponding FeatureCardVM fields.
type CompanyFeaturesVM struct {
	HasCompany bool
	CanManage  bool // true iff current user is company owner
	Breadcrumb []SettingsBreadcrumbPart
	Features   []FeatureCardVM
	Flash      CompanyFeaturesFlash
}

// FeatureCardVM is one row in the Features grid. Matches
// services.FeatureView plus handful of UI-ready derived fields.
type FeatureCardVM struct {
	Key              models.FeatureKey
	Label            string
	Maturity         models.FeatureMaturity
	Description      string
	FitDescription   string
	SelfServeEnable  bool
	TypedConfirmText string
	AckVersion       string
	RequiredAcks     []string

	Status           models.FeatureStatus
	EnabledAt        *time.Time
	EnabledByUserID  *uuid.UUID
	ReasonCode       models.ReasonCode
	ReasonNote       string

	ReasonCodeOptions []ReasonCodeOption
}

// ReasonCodeOption is a (value, label) pair for the reason-code
// dropdown inside the enable modal.
type ReasonCodeOption struct {
	Value string
	Label string
}

// CompanyFeaturesFlash carries transient messages from a POST
// redirect. Either a success message or an error; rendered as a
// toast banner at the top of the page.
type CompanyFeaturesFlash struct {
	Success string
	Error   string
}

// IsEnabled is the short-circuit used by the templ to choose between
// Off / Enabled / Unavailable badges and buttons.
func (f FeatureCardVM) IsEnabled() bool {
	return f.Status == models.FeatureStatusEnabled
}

// IsComingSoon renders a disabled "Coming soon" badge when true.
func (f FeatureCardVM) IsComingSoon() bool {
	return f.Maturity == models.FeatureMaturityComingSoon || !f.SelfServeEnable
}

// MaturityBadgeText is the small-caps label shown next to the name
// (e.g. "Alpha", "Coming soon").
func (f FeatureCardVM) MaturityBadgeText() string {
	switch f.Maturity {
	case models.FeatureMaturityAlpha:
		return "Alpha"
	case models.FeatureMaturityBeta:
		return "Beta"
	case models.FeatureMaturityGA:
		return "GA"
	case models.FeatureMaturityComingSoon:
		return "Coming soon"
	}
	return string(f.Maturity)
}

// StatusBadgeText shows the current effective state. "Unavailable"
// is reserved for coming_soon / non-self-serve features.
func (f FeatureCardVM) StatusBadgeText() string {
	if f.IsComingSoon() {
		return "Unavailable"
	}
	if f.IsEnabled() {
		return "Enabled"
	}
	return "Off"
}
