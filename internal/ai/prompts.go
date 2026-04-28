// 遵循project_guide.md
package ai

// Prompt key constants.
// Use these constants in all Platform.Complete() calls — never embed raw strings.
const (
	// PromptInvoiceMemoAssist generates a concise invoice memo from invoice context.
	PromptInvoiceMemoAssist = "balanciz.invoice.memo_assist"

	// PromptInvoiceEmailDraft drafts a polite invoice follow-up / reminder email.
	PromptInvoiceEmailDraft = "balanciz.invoice.email_draft"
)

// registry maps prompt key → promptDef.
// To add a new prompt: add a constant above and an entry here.
var registry = map[string]promptDef{
	PromptInvoiceMemoAssist: {
		system: `You are a concise, professional bookkeeper assistant for Balanciz accounting software.
Your task is to write a short invoice memo (1-2 sentences, under 120 characters) based on the provided context.
Return only the memo text — no commentary, no quotation marks, no labels.`,
		user: `Write an invoice memo for:
Customer: {{customer}}
Services rendered: {{services}}
Invoice total: {{total}}`,
	},

	PromptInvoiceEmailDraft: {
		system: `You are a professional, friendly bookkeeper assistant for Balanciz accounting software.
Draft a polite, brief (under 200 words) payment reminder or invoice follow-up email.
Return only the email body — no subject line, no JSON, no labels.`,
		user: `Draft a payment reminder email for:
Customer: {{customer}}
Invoice #: {{invoice_number}}
Amount due: {{amount}}
Due date: {{due_date}}
Overdue by: {{overdue_days}} days`,
	},
}
