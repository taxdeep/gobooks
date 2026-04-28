// 遵循project_guide.md
package services

import (
	"strings"

	"github.com/shopspring/decimal"
	"balanciz/internal/models"
)

// HostedPageMeta carries runtime metadata for the customer-facing hosted invoice page.
// It is separate from InvoiceRenderData so that the hosted toolbar can reflect
// current invoice state without altering the core render pipeline.
//
// Capability flags:
//   - CanDownload: false in Batch 6. Reserved for a future /i/:token/download endpoint
//     that serves a server-side PDF. The toolbar already has a "Print / Save PDF"
//     button (via window.print()) that works today without a PDF engine.
//   - CanPay: wired in Batch 7 to EvaluateHostedPayability. When true, the toolbar
//     renders a real POST form to /i/:token/pay instead of a disabled placeholder.
//   - Token: the plaintext hosted token, used to build the pay action URL.
type HostedPageMeta struct {
	EffectiveStatus models.InvoiceStatus
	BalanceDue      decimal.Decimal
	Currency        string
	CanDownload     bool   // Batch 6: false. Future: true when /i/:token/download is wired.
	CanPay          bool   // Batch 7: wired to EvaluateHostedPayability.
	Token           string // Batch 7: plaintext token for building pay action URL.
}

// RenderInvoiceForHosted produces the customer-facing hosted invoice HTML.
//
// Pipeline:
//  1. BuildInvoiceRenderData (shared with internal preview — same template resolve chain)
//  2. RenderInvoiceToHTML (existing classic/modern templates)
//  3. Inject hosted toolbar CSS + HTML into the rendered document
//
// The toolbar is injected via string replacement so that the existing render
// functions are unchanged. The hosted page intentionally shares the internal
// preview render pipeline to guarantee visual and data consistency.
func RenderInvoiceForHosted(data InvoiceRenderData, meta HostedPageMeta) string {
	base := RenderInvoiceToHTML(data)

	// 1. Inject toolbar styles before </head>
	styles := buildHostedBarStyles()
	base = strings.Replace(base, "</head>", styles+"\n</head>", 1)

	// 2. Inject toolbar after <body>
	toolbar := buildHostedToolbar(data, meta)
	base = strings.Replace(base, "<body>", "<body>\n"+toolbar, 1)

	return base
}

// buildHostedBarStyles returns a <style> block that styles the fixed top toolbar.
// Uses @media print to hide the toolbar when the user prints/saves as PDF,
// and adds padding-top to avoid content being hidden behind the fixed bar.
func buildHostedBarStyles() string {
	return `<style>
.hb{position:fixed;top:0;left:0;right:0;z-index:1000;background:#fff;border-bottom:1px solid #e5e7eb;padding:10px 24px;display:flex;align-items:center;justify-content:space-between;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Arial,sans-serif;font-size:13px;box-shadow:0 1px 4px rgba(0,0,0,.08);}
.hb-left{display:flex;align-items:center;gap:12px;color:#374151;flex-wrap:wrap;}
.hb-co{font-weight:600;color:#111827;}
.hb-badge{display:inline-block;padding:2px 8px;border-radius:3px;font-size:11px;font-weight:700;text-transform:uppercase;}
.hb-paid{background:#f0fdf4;color:#15803d;border:1px solid #bbf7d0;}
.hb-partial{background:#fffbeb;color:#b45309;border:1px solid #fde68a;}
.hb-overdue{background:#fef2f2;color:#b91c1c;border:1px solid #fecaca;}
.hb-open{background:#eff6ff;color:#1d4ed8;border:1px solid #bfdbfe;}
.hb-voided{background:#f3f4f6;color:#6b7280;border:1px solid #d1d5db;text-decoration:line-through;}
.hb-bal{color:#6b7280;font-size:13px;}
.hb-bal strong{color:#374151;}
.hb-right{display:flex;align-items:center;gap:8px;flex-shrink:0;}
.hb-btn{display:inline-flex;align-items:center;gap:4px;padding:5px 12px;border-radius:4px;font-size:12px;font-weight:500;cursor:pointer;text-decoration:none;border:1px solid #d1d5db;background:#fff;color:#374151;transition:background .15s;}
.hb-btn:hover{background:#f9fafb;}
.hb-btn-dis{opacity:.45;cursor:not-allowed;pointer-events:none;border-style:dashed;}
.hb-pay{background:#1d4ed8;color:#fff !important;border-color:#1d4ed8;}
.hb-pay:hover{background:#1e40af;}
body{padding-top:56px !important;}
@media print{.hb{display:none !important;}body{padding-top:0 !important;}}
</style>`
}

// buildHostedToolbar returns the HTML for the customer-facing action bar.
//
// Contains:
//   - Company name + invoice status badge + balance due (left)
//   - Print/Save PDF button (always enabled — uses browser window.print())
//   - Pay Now slot (disabled placeholder in Batch 6 for payable invoices)
func buildHostedToolbar(data InvoiceRenderData, meta HostedPageMeta) string {
	badgeClass, badgeLabel := hostedStatusBadge(meta.EffectiveStatus)

	var sb strings.Builder
	sb.WriteString(`<div class="hb">`)

	// Left: company name + status badge + balance due
	sb.WriteString(`<div class="hb-left">`)
	sb.WriteString(`<span class="hb-co">` + escapeHTML(data.CompanyName) + `</span>`)
	sb.WriteString(`<span class="hb-badge ` + badgeClass + `">` + badgeLabel + `</span>`)
	if meta.BalanceDue.GreaterThan(decimal.Zero) {
		sb.WriteString(`<span class="hb-bal">Balance due: <strong>` +
			escapeHTML(meta.Currency) + `&nbsp;` + meta.BalanceDue.StringFixed(2) +
			`</strong></span>`)
	}
	sb.WriteString(`</div>`)

	// Right: actions
	sb.WriteString(`<div class="hb-right">`)

	// Download PDF — server-side PDF via /i/:token/download (Batch 8+).
	// Only shown when wkhtmltopdf is available (CanDownload=true).
	// Falls back to browser print dialog when CanDownload is false.
	if meta.CanDownload && meta.Token != "" {
		dlURL := "/i/" + meta.Token + "/download"
		sb.WriteString(`<a class="hb-btn" href="` + dlURL + `" download title="Download as PDF">Download PDF</a>`)
	} else {
		sb.WriteString(`<a class="hb-btn" href="javascript:window.print()" title="Print or save as PDF">Print / Save PDF</a>`)
	}

	// Pay Now slot.
	// When CanPay is true (Batch 7+): real POST form to /i/:token/pay.
	// POST is intentional — pay intent creation is state-changing; GET would allow
	// browser prefetching to trigger accidental payment sessions.
	// When CanPay is false but status is payable: disabled placeholder.
	if meta.CanPay && meta.Token != "" {
		payURL := "/i/" + meta.Token + "/pay"
		sb.WriteString(`<form method="post" action="` + payURL + `" style="display:inline;">`)
		sb.WriteString(`<button type="submit" class="hb-btn hb-pay">Pay Now</button>`)
		sb.WriteString(`</form>`)
	} else if isPayableStatus(meta.EffectiveStatus) {
		// Invoice is payable but gateway not wired or not eligible — disabled placeholder.
		sb.WriteString(`<span class="hb-btn hb-pay hb-btn-dis" title="Online payment coming soon">Pay Now</span>`)
	}

	sb.WriteString(`</div>`)
	sb.WriteString(`</div>`)
	return sb.String()
}

// hostedStatusBadge maps an invoice status to (CSS class, display label) for the toolbar badge.
func hostedStatusBadge(status models.InvoiceStatus) (class, label string) {
	switch status {
	case models.InvoiceStatusPaid:
		return "hb-badge hb-paid", "Paid"
	case models.InvoiceStatusPartiallyPaid:
		return "hb-badge hb-partial", "Partially Paid"
	case models.InvoiceStatusOverdue:
		return "hb-badge hb-overdue", "Overdue"
	case models.InvoiceStatusVoided:
		return "hb-badge hb-voided", "Voided"
	default:
		return "hb-badge hb-open", "Open"
	}
}

// isPayableStatus returns true when the invoice is in a state where payment
// would be expected (i.e., the Pay Now placeholder is relevant).
func isPayableStatus(status models.InvoiceStatus) bool {
	switch status {
	case models.InvoiceStatusIssued,
		models.InvoiceStatusSent,
		models.InvoiceStatusPartiallyPaid,
		models.InvoiceStatusOverdue:
		return true
	}
	return false
}

// RenderHostedErrorPage returns a minimal HTML error page for invalid / expired /
// revoked tokens. This page deliberately reveals no information about whether
// the invoice or link ever existed.
func RenderHostedErrorPage() string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Link Not Available</title>
<style>
*{margin:0;padding:0;box-sizing:border-box;}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Arial,sans-serif;color:#374151;background:#f9fafb;display:flex;align-items:center;justify-content:center;min-height:100vh;padding:24px;}
.card{background:#fff;border:1px solid #e5e7eb;border-radius:8px;padding:40px 48px;max-width:480px;text-align:center;}
.icon{font-size:40px;margin-bottom:16px;}
h1{font-size:20px;font-weight:600;color:#111827;margin-bottom:8px;}
p{font-size:14px;color:#6b7280;line-height:1.6;}
</style>
</head>
<body>
<div class="card">
<div class="icon">🔒</div>
<h1>Link Not Available</h1>
<p>This invoice link is not available.<br>It may have been revoked, expired, or the link may be incorrect.</p>
</div>
</body>
</html>`
}
