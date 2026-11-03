package celx

import (
	"fmt"
	"regexp"
	"strings"

	resourceapi "k8s.io/api/resource/v1"
)

// fieldRef matches device.attributes["domain"].name and
// device.capacity["domain"].name, with either quote style and with the name
// either as a field or as an index.
var fieldRef = regexp.MustCompile(`device\.(attributes|capacity)\[\s*["']([^"']+)["']\s*\]\s*(?:\.\s*([A-Za-z_][A-Za-z0-9_]*)|\[\s*["']([^"']+)["']\s*\])`)

// Field is an attribute or capacity entry that an expression reads.
type Field struct {
	Capacity bool
	Domain   string
	Name     string
}

func (f Field) String() string {
	return f.Domain + "/" + f.Name
}

// ReferencedFields lists the attributes and capacities an expression reads,
// in order of first use. It is a textual scan meant for explanations, not a
// CEL parser: fields reached through variables or macros are missed.
func ReferencedFields(expr string) []Field {
	var fields []Field
	seen := make(map[Field]bool)
	for _, m := range fieldRef.FindAllStringSubmatch(expr, -1) {
		f := Field{Capacity: m[1] == "capacity", Domain: m[2], Name: m[3]}
		if f.Name == "" {
			f.Name = m[4]
		}
		if !seen[f] {
			seen[f] = true
			fields = append(fields, f)
		}
	}
	return fields
}

// Values renders the device's values for the fields an expression reads,
// such as `gpu.example.com/model="A100", gpu.example.com/memory=<missing>`.
// Names without a domain in the ResourceSlice belong to the driver's domain,
// as in CEL evaluation.
func Values(expr, driver string, device *resourceapi.Device) string {
	var parts []string
	for _, f := range ReferencedFields(expr) {
		parts = append(parts, f.String()+"="+lookup(f, driver, device))
	}
	return strings.Join(parts, ", ")
}

func lookup(f Field, driver string, device *resourceapi.Device) string {
	names := []resourceapi.QualifiedName{resourceapi.QualifiedName(f.Domain + "/" + f.Name)}
	if f.Domain == driver {
		names = append(names, resourceapi.QualifiedName(f.Name))
	}
	for _, n := range names {
		if f.Capacity {
			if c, ok := device.Capacity[n]; ok {
				return c.Value.String()
			}
			continue
		}
		if a, ok := device.Attributes[n]; ok {
			return formatAttribute(a)
		}
	}
	return "<missing>"
}

func formatAttribute(a resourceapi.DeviceAttribute) string {
	switch {
	case a.IntValue != nil:
		return fmt.Sprint(*a.IntValue)
	case a.BoolValue != nil:
		return fmt.Sprint(*a.BoolValue)
	case a.StringValue != nil:
		return fmt.Sprintf("%q", *a.StringValue)
	case a.VersionValue != nil:
		return "version " + *a.VersionValue
	case a.IntValues != nil:
		return fmt.Sprint(a.IntValues)
	case a.BoolValues != nil:
		return fmt.Sprint(a.BoolValues)
	case a.StringValues != nil:
		return fmt.Sprintf("%q", a.StringValues)
	case a.VersionValues != nil:
		return fmt.Sprint(a.VersionValues)
	default:
		return "<unknown type>"
	}
}
