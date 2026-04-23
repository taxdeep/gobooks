// 遵循project_guide.md
package pdf

import "encoding/base64"

// base64Encode is a tiny helper used by RenderPDF to wrap the rendered
// HTML in a data: URL. Kept as a named func so the engine.go file reads
// linearly without inline imports.
func base64Encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
