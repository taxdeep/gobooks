// 遵循project_guide.md
package services

// invoice_pdf_filename_test.go — Unit tests for InvoicePDFSafeFilename (Batch 8.1).
//
// Coverage:
//   TestInvoicePDFSafeFilename_Normal            — clean alphanumeric input passes through unchanged
//   TestInvoicePDFSafeFilename_Slashes           — forward and back slashes → '-'
//   TestInvoicePDFSafeFilename_Quotes            — double quotes stripped (not left in header)
//   TestInvoicePDFSafeFilename_Semicolons        — semicolons stripped (Content-Disposition param separator)
//   TestInvoicePDFSafeFilename_ControlChars      — CR/LF and other control chars stripped
//   TestInvoicePDFSafeFilename_ColonsAngles      — colon, angle brackets, pipe stripped
//   TestInvoicePDFSafeFilename_ConsecutiveDashes — consecutive dashes collapsed
//   TestInvoicePDFSafeFilename_LeadingTrailing   — leading/trailing dashes trimmed
//   TestInvoicePDFSafeFilename_Empty             — empty input → Invoice-unknown.pdf
//   TestInvoicePDFSafeFilename_AllSpecial        — all-special input → Invoice-unknown.pdf
//   TestInvoicePDFSafeFilename_InjectionAttempt  — header injection string → safe output
//   TestInvoicePDFSafeFilename_UnicodeStripped   — non-ASCII bytes → dashes

import "testing"

func TestInvoicePDFSafeFilename_Normal(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"INV-001", "Invoice-INV-001.pdf"},
		{"INV001", "Invoice-INV001.pdf"},
		{"A-B-C_123", "Invoice-A-B-C_123.pdf"},
		{"2024.001", "Invoice-2024.001.pdf"},
	}
	for _, tc := range cases {
		got := InvoicePDFSafeFilename(tc.input)
		if got != tc.want {
			t.Errorf("InvoicePDFSafeFilename(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestInvoicePDFSafeFilename_Slashes(t *testing.T) {
	cases := []struct{ input, want string }{
		{"2024/001", "Invoice-2024-001.pdf"},
		{"2024\\001", "Invoice-2024-001.pdf"},
		{"A/B\\C", "Invoice-A-B-C.pdf"},
	}
	for _, tc := range cases {
		got := InvoicePDFSafeFilename(tc.input)
		if got != tc.want {
			t.Errorf("InvoicePDFSafeFilename(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestInvoicePDFSafeFilename_Quotes(t *testing.T) {
	// Double quotes inside Content-Disposition filename would close the quoted-string
	// and allow injecting additional header parameters.
	got := InvoicePDFSafeFilename(`INV"001`)
	want := "Invoice-INV-001.pdf"
	if got != want {
		t.Errorf("InvoicePDFSafeFilename with quote: got %q, want %q", got, want)
	}
}

func TestInvoicePDFSafeFilename_Semicolons(t *testing.T) {
	// Semicolons separate parameters in Content-Disposition, e.g.:
	//   attachment; filename="X"; other-param=y
	// A semicolon inside the filename value could inject additional params.
	got := InvoicePDFSafeFilename("INV;001")
	want := "Invoice-INV-001.pdf"
	if got != want {
		t.Errorf("InvoicePDFSafeFilename with semicolon: got %q, want %q", got, want)
	}
}

func TestInvoicePDFSafeFilename_ControlChars(t *testing.T) {
	// CR/LF in a header value can split the header into a fake new header (HTTP response
	// splitting). Control chars must not survive into Content-Disposition.
	cases := []struct{ input, want string }{
		{"INV\r001", "Invoice-INV-001.pdf"},
		{"INV\n001", "Invoice-INV-001.pdf"},
		{"INV\r\n001", "Invoice-INV-001.pdf"},
		{"INV\x00001", "Invoice-INV-001.pdf"},
		{"INV\x1f001", "Invoice-INV-001.pdf"},
	}
	for _, tc := range cases {
		got := InvoicePDFSafeFilename(tc.input)
		if got != tc.want {
			t.Errorf("InvoicePDFSafeFilename(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestInvoicePDFSafeFilename_ColonsAngles(t *testing.T) {
	got := InvoicePDFSafeFilename("INV:001<bad>|pipe")
	want := "Invoice-INV-001-bad-pipe.pdf"
	if got != want {
		t.Errorf("InvoicePDFSafeFilename colon/angle/pipe: got %q, want %q", got, want)
	}
}

func TestInvoicePDFSafeFilename_ConsecutiveDashes(t *testing.T) {
	// Multiple adjacent special chars each become '-', then collapsed.
	got := InvoicePDFSafeFilename("INV///001")
	want := "Invoice-INV-001.pdf"
	if got != want {
		t.Errorf("InvoicePDFSafeFilename consecutive dashes: got %q, want %q", got, want)
	}
}

func TestInvoicePDFSafeFilename_LeadingTrailing(t *testing.T) {
	got := InvoicePDFSafeFilename("/INV-001/")
	want := "Invoice-INV-001.pdf"
	if got != want {
		t.Errorf("InvoicePDFSafeFilename leading/trailing dash: got %q, want %q", got, want)
	}
}

func TestInvoicePDFSafeFilename_Empty(t *testing.T) {
	got := InvoicePDFSafeFilename("")
	want := "Invoice-unknown.pdf"
	if got != want {
		t.Errorf("InvoicePDFSafeFilename empty: got %q, want %q", got, want)
	}
}

func TestInvoicePDFSafeFilename_AllSpecial(t *testing.T) {
	// After stripping all non-whitelisted chars, the segment is empty → fallback.
	// Use actual CR/LF bytes and other non-letter special chars only.
	got := InvoicePDFSafeFilename("\";:<>|\r\n\x00\x1f")
	want := "Invoice-unknown.pdf"
	if got != want {
		t.Errorf("InvoicePDFSafeFilename all-special: got %q, want %q", got, want)
	}
}

func TestInvoicePDFSafeFilename_InjectionAttempt(t *testing.T) {
	// A crafted invoice number trying to inject a Content-Disposition parameter.
	// e.g. attacker-supplied: 'evil"; filename=malware.exe'
	// After sanitization this must produce a safe, single filename value.
	got := InvoicePDFSafeFilename(`evil"; filename=malware.exe`)
	// Expected: letters + dash-for-space, quotes/semicolons stripped.
	// 'evil' → kept, '"' → '-', ';' → '-', space → '-', 'filename=malware.exe' → 'filename-malware.exe'
	// Collapsed: evil-filename-malware.exe
	want := "Invoice-evil-filename-malware.exe.pdf"
	if got != want {
		t.Errorf("InvoicePDFSafeFilename injection attempt: got %q, want %q", got, want)
	}
}

func TestInvoicePDFSafeFilename_UnicodeStripped(t *testing.T) {
	// Non-ASCII bytes must not appear in the filename — they are replaced with '-'.
	// This keeps the filename ASCII-safe without requiring filename* encoding.
	got := InvoicePDFSafeFilename("INV-\u4e2d\u6587-001")
	// Chinese characters are multi-byte in UTF-8; each byte outside [A-Za-z0-9._-] → '-'
	// The exact byte count depends on encoding; what matters is the result is safe.
	// Chinese '中文' = 6 bytes in UTF-8; each → '-'; collapsed + trimmed.
	want := "Invoice-INV-001.pdf"
	if got != want {
		t.Errorf("InvoicePDFSafeFilename unicode: got %q, want %q", got, want)
	}
}
