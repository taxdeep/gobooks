// Package version holds the application release string.
package version

const (
	// Major and Patch are decimal release components.
	Major = 0
	Patch = 0

	// ChannelCode is a fixed-width uppercase base36 channel marker.
	ChannelCode = "00"

	// BuildCode is a fixed-width uppercase base36 build counter.
	BuildCode = "001J"

	// Version format: M.CC.PPP.BBBB
	// M/PPP are decimal; CC/BBBB are uppercase base36-style codes.
	Version = "0.00.000.001J"
)
