// Package version holds the application release string.
package version

const (
	// Major is the decimal major release component.
	Major = 0

	// ChannelCode is a fixed-width uppercase base36 release channel marker.
	ChannelCode = "00"

	// Patch is the decimal patch/build-train component.
	Patch = 0

	// BuildCode is a fixed-width uppercase base36 build counter.
	BuildCode = "0000"

	// Revision is the decimal release revision component.
	Revision = 0

	// VariantCode is a single-character uppercase base36 variant marker.
	VariantCode = "0"

	// Version format: X.YY.XXX.YYYY.XX.Y
	// X/XXX/XX are decimal; YY/YYYY/Y are uppercase base36.
	Version = "0.00.000.0000.00.0"
)
