package layout

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// BinaryFile is the bucket file name of an asset's binary for an arch.
func BinaryFile(asset, arch string) string { return asset + "-linux-" + arch }

// UnitFile is the bucket file name of an asset's unit template.
func UnitFile(asset string) string { return asset + ".service" }

// EnvExampleFile is the bucket file name of an asset's example env file.
func EnvExampleFile(asset string) string { return asset + ".env.example" }

// UnitName picks the systemd unit name (without ".service") for a ref named
// name in service. The first ref of a service gets the service name itself;
// later ones get name, prefixed with "service-" unless it already is.
func UnitName(service, name string, existingUnits []string) (string, error) {
	var unit string
	switch {
	case len(existingUnits) == 0:
		unit = service
	case strings.HasPrefix(name, service+"-"):
		unit = name
	default:
		unit = service + "-" + name
	}
	if slices.Contains(existingUnits, unit) {
		return "", fmt.Errorf("unit name %q already exists in service %q", unit, service)
	}
	return unit, nil
}

// Placeholders are the values substituted into a unit template.
type Placeholders struct{ Service, Bin, BinDir, EnvFile, DataDir, RunDir, Unit string }

var placeholderRe = regexp.MustCompile(`\{\{[^}]*\}\}`)

func (p Placeholders) values() map[string]string {
	return map[string]string{
		"{{SERVICE}}":  p.Service,
		"{{USER}}":     p.Service,
		"{{BIN}}":      p.Bin,
		"{{BIN_DIR}}":  p.BinDir,
		"{{ENV_FILE}}": p.EnvFile,
		"{{DATA_DIR}}": p.DataDir,
		"{{RUN_DIR}}":  p.RunDir,
		"{{UNIT}}":     p.Unit,
	}
}

// RenderUnit substitutes placeholders; any unknown {{...}} is an error,
// except on a comment line (# or ;), where it is left as written.
func RenderUnit(tmpl string, p Placeholders) (string, error) {
	vals := p.values()
	var bad []string
	lines := strings.SplitAfter(tmpl, "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		comment := strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";")
		lines[i] = placeholderRe.ReplaceAllStringFunc(line, func(m string) string {
			v, ok := vals[m]
			switch {
			case ok:
				return v
			case comment:
				return m
			}
			bad = append(bad, m)
			return ""
		})
	}
	out := strings.Join(lines, "")
	if len(bad) > 0 {
		return "", fmt.Errorf("unit template has unknown placeholder %s", strings.Join(bad, ", "))
	}
	return out, nil
}

// CheckUnitTemplate verifies that tmpl only uses known placeholders.
func CheckUnitTemplate(tmpl string) error {
	_, err := RenderUnit(tmpl, Placeholders{})
	return err
}
