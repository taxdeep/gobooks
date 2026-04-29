// 遵循project_guide.md
package pages

type ForgotPasswordViewModel struct {
	Email       string
	ChallengeID string
	FormError   string
	FormSuccess string
	Step        string // "" = request, "reset" = verify code + new password
}
