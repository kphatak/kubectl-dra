package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"sigs.k8s.io/yaml"
)

// printStructured writes v as indented JSON or as YAML, for -o json|yaml.
// The schema is the command's report struct, not a Kubernetes object.
func printStructured(w io.Writer, format string, v any) error {
	switch format {
	case outputJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	case outputYAML:
		data, err := yaml.Marshal(v)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	default:
		return fmt.Errorf("internal error: %q is not a structured format", format)
	}
}

// newTable returns a tabwriter with kubectl's column spacing.
func newTable(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 8, 3, ' ', 0)
}
