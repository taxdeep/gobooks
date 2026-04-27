// 遵循project_guide.md
package pages

import "fmt"

// vendorDetailsHref is the AP mirror of customerDetailsHref. Every
// "Add X" link in the vendor header points here so there's one place
// to adjust if the deep-link target ever moves. These links open edit
// mode because adding a missing field is an edit intent.
func vendorDetailsHref(vendorID uint) string {
	return fmt.Sprintf("/vendors/%d?tab=details&edit=1", vendorID)
}

// vendorCurrencyDisplay formats the vendor's currency stance for the
// header grid. Single-currency only on the AP side (no multi-currency
// policy yet), so the display is simpler than its Customer twin.
func vendorCurrencyDisplay(vm VendorDetailVM) string {
	if vm.Vendor.CurrencyCode == "" {
		if vm.BaseCurrencyCode == "" {
			return "Company base"
		}
		return fmt.Sprintf("%s (base)", vm.BaseCurrencyCode)
	}
	return vm.Vendor.CurrencyCode
}

func vendorStatusLabel(vm VendorDetailVM) string {
	if vm.Vendor.IsActive {
		return "Active"
	}
	return "Inactive"
}
