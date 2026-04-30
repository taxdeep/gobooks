// Package version holds the application release string.
package version

const (
	// Major and Patch are decimal release components.
	Major = 0
	Patch = 0

	// ChannelCode is a fixed-width uppercase base36 channel marker.
	ChannelCode = "00"

	// BuildCode is a fixed-width uppercase base36 build counter.
	BuildCode = "000V"

	// Revision is a two-digit decimal release revision.
	Revision = 0

	// VariantCode is a one-character uppercase base36 build variant.
	VariantCode = "0"

	// Version format: M.CC.PPP.BBBB.RR.V
	// M/PPP/RR are decimal; CC/BBBB/V are uppercase base36-style codes.
	Version = "0.00.000.000V.00.0"
)
