// 遵循project_guide.md
package ui

// FooterBtnVariant is the typed enum for DocEditorFooterButton.Variant.
// Using a named string type keeps call sites type-checked while still being
// a string under the hood (zero-cost conversion; works with templ string attrs).
type FooterBtnVariant string

const (
	FooterBtnPrimary   FooterBtnVariant = "primary"
	FooterBtnSecondary FooterBtnVariant = "secondary"
	FooterBtnDanger    FooterBtnVariant = "danger"
)

// FooterLinkVariant is the typed enum for DocEditorFooterLink.Variant.
type FooterLinkVariant string

const (
	// FooterLinkMuted is the default outlined look used for Cancel / Back links.
	FooterLinkMuted FooterLinkVariant = ""
	// FooterLinkDanger is a red-outlined link variant (rare; typically use
	// FooterBtnDanger instead since danger actions are usually submit buttons).
	FooterLinkDanger FooterLinkVariant = "danger"
	// FooterLinkSecondaryText is a plain text link in primary color used in
	// the center slot (Print, Make recurring, etc.).
	FooterLinkSecondaryText FooterLinkVariant = "secondary-text"
)

// docEditorBackLabel returns the back-link label with a safe default.
func docEditorBackLabel(vm DocEditorShellVM) string {
	if vm.BackLabel != "" {
		return vm.BackLabel
	}
	return "Back"
}

// docEditorButtonType returns the type attribute for a footer button,
// defaulting to "submit" so most form buttons behave correctly without config.
func docEditorButtonType(t string) string {
	if t == "button" || t == "reset" {
		return t
	}
	return "submit"
}

// docEditorFooterLinkClass returns the Tailwind classes for a footer link.
// Shares the button class helpers where variants overlap to keep a single
// source of truth across ui.Button* and the footer.
func docEditorFooterLinkClass(variant FooterLinkVariant) string {
	switch variant {
	case FooterLinkDanger:
		return "rounded-md border border-border-danger px-4 py-2 text-body font-semibold text-danger hover:bg-danger-soft"
	case FooterLinkSecondaryText:
		return "text-body font-medium text-primary hover:text-primary-hover"
	default:
		// Matches ButtonSecondary (outlined muted button), minus the disabled:
		// modifier since anchor links cannot be disabled.
		return ButtonSecondaryBaseClass()
	}
}

// docEditorFooterButtonClass returns the Tailwind classes for a footer
// primary/secondary/danger button, sharing the class string with ui.Button*
// so visual changes only need to happen in one place.
func docEditorFooterButtonClass(variant FooterBtnVariant) string {
	switch variant {
	case FooterBtnSecondary:
		return ButtonSecondaryBaseClass() + " " + buttonDisabledSuffix
	case FooterBtnDanger:
		return ButtonDangerBaseClass() + " " + buttonDisabledSuffix
	default:
		return ButtonPrimaryBaseClass() + " " + buttonDisabledSuffix
	}
}

// ButtonPrimaryBaseClass / ButtonSecondaryBaseClass / ButtonDangerBaseClass
// return the shared Tailwind class strings used by both ui.Button* templs and
// the DocEditor footer buttons. Centralising them here means a colour/spacing
// change only requires editing one place.
//
// The disabled modifier (buttonDisabledSuffix) is appended separately by
// callers that render real <button> elements; anchor-link variants omit it.

func ButtonPrimaryBaseClass() string {
	return "rounded-md bg-primary px-4 py-2 text-body font-semibold text-onPrimary hover:bg-primary-hover"
}

func ButtonSecondaryBaseClass() string {
	return "rounded-md border border-border-input px-4 py-2 text-body font-semibold text-text-muted3 hover:bg-background hover:text-text"
}

func ButtonDangerBaseClass() string {
	return "rounded-md bg-danger px-4 py-2 text-body font-semibold text-onPrimary hover:bg-danger-hover"
}

// buttonDisabledSuffix is appended to button class strings so disabled state
// renders consistently across ui.Button* and DocEditor footer buttons.
const buttonDisabledSuffix = "disabled:bg-disabled-bg disabled:text-disabled-text disabled:cursor-not-allowed"
