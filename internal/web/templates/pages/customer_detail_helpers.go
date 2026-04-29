// 遵循project_guide.md
package pages

import (
	"fmt"
	"strings"

	"balanciz/internal/models"
)

// ── Customer detail header helpers ─────────────────────────────────────────

// customerDetailsHref builds a link into the customer detail page's
// Details tab. The optional `focus` hint (email / billing / etc.) is
// reserved for a future "scroll to this field" enhancement — for now
// the link just opens the tab. Kept as one function so every "Add X"
// link on the page points at the same place. Because "Add X" is an edit
// intent, these links open the Profile drawer in edit mode.
func customerDetailsHref(customerID uint, _ string) string {
	return fmt.Sprintf("/customers/%d?tab=profile&edit=1", customerID)
}

// customerBillingLine renders the customer's billing address as a
// single display line for the compact header. Returns the joined
// address when at least one line is set; empty string otherwise (the
// header then shows an "Add billing address" link).
func customerBillingLine(c models.Customer) string {
	parts := []string{}
	if c.AddrStreet1 != "" {
		parts = append(parts, c.AddrStreet1)
	}
	city := strings.TrimSpace(strings.Join(nonEmpty([]string{c.AddrCity, c.AddrProvince, c.AddrPostalCode}), " "))
	if city != "" {
		parts = append(parts, city)
	}
	if c.AddrCountry != "" {
		parts = append(parts, c.AddrCountry)
	}
	return strings.Join(parts, ", ")
}

// customerCurrencyDisplay formats the customer's currency stance for
// the header grid. "USD" / "CAD (base)" / "Multi-currency (CAD base)".
// Never returns empty — currency always has a meaningful value.
func customerCurrencyDisplay(vm CustomerDetailVM) string {
	base := vm.BaseCurrencyCode
	if vm.Customer.CurrencyPolicy == models.CustomerCurrencyPolicyMultiAllowed {
		if base == "" {
			return "Multi-currency"
		}
		return fmt.Sprintf("Multi-currency (%s base)", base)
	}
	if vm.Customer.CurrencyCode == "" {
		if base == "" {
			return "Company base"
		}
		return fmt.Sprintf("%s (base)", base)
	}
	return vm.Customer.CurrencyCode
}

func customerDefaultTermsDisplay(vm CustomerDetailVM) string {
	if vm.DefaultPaymentTermLabel == "" {
		return "-"
	}
	return vm.DefaultPaymentTermLabel
}

// customerShippingSummary returns a short count sentence for the header
// grid. "No shipping addresses" / "1 address" / "3 addresses". Clicks
// on the link navigate to the Addresses tab via a nearby anchor; this
// helper only returns the text.
func customerShippingSummary(vm CustomerDetailVM) string {
	n := len(vm.ShippingAddresses)
	switch n {
	case 0:
		return "None"
	case 1:
		return "1 address"
	default:
		return fmt.Sprintf("%d addresses", n)
	}
}

func customerDefaultShippingLine(vm CustomerDetailVM) string {
	if len(vm.ShippingAddresses) == 0 {
		return ""
	}
	for _, addr := range vm.ShippingAddresses {
		if addr.IsDefault {
			return strings.ReplaceAll(addr.FormattedAddress(), "\n", ", ")
		}
	}
	return strings.ReplaceAll(vm.ShippingAddresses[0].FormattedAddress(), "\n", ", ")
}

func customerStatusLabel(c models.Customer) string {
	if c.IsActive {
		return "Active"
	}
	return "Inactive"
}

func customerDetailDrawerData(vm CustomerDetailVM) string {
	mode := strings.TrimSpace(vm.DrawerMode)
	open := "false"
	if mode != "" {
		open = "true"
	}
	if mode != "shipping" {
		mode = "edit"
	}
	return fmt.Sprintf(`{ drawerOpen: %s, drawerMode: %q, menuOpen: false }`, open, mode)
}

// nonEmpty drops empty / whitespace-only strings from a slice, used by
// customerBillingLine to build a "city province postal" run without
// stray gaps when one of the fields is blank.
func nonEmpty(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
