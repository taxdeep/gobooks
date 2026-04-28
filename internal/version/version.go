// Package version holds the application release string.
package version

const (
	// Major, Patch, and Revision are fixed-width decimal release components.
	Major    = 0
	Patch    = 0
	Revision = 0

	// ChannelCode, BuildCode, and VariantCode are fixed-width uppercase base36 components.
	ChannelCode = "00"
	BuildCode   = "0000"
	VariantCode = "0"

	// Version format: X.YY.XXX.YYYY.XX.Y
	// X/XXX/XX are decimal; YY/YYYY/Y are uppercase base36.
	Version = "0.00.000.0000.00.0"
)
