package output

import (
	"encoding/json"
	"io"

	"github.com/dgrieser/nickpit/internal/model"
)

type JSONFormatter struct {
	w io.Writer
}

func NewJSONFormatter(w io.Writer) *JSONFormatter {
	return &JSONFormatter{w: w}
}

func (f *JSONFormatter) FormatFindings(result *model.ReviewResult) error {
	enc := json.NewEncoder(f.w)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

// FormatWarnings emits only the warning list, under the same key the full
// result uses, so a consumer can read either shape with one code path. The
// list is always present, empty rather than absent, when a run had none.
func (f *JSONFormatter) FormatWarnings(result *model.ReviewResult) error {
	warnings := result.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	enc := json.NewEncoder(f.w)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Warnings []string `json:"warnings"`
	}{Warnings: warnings})
}
