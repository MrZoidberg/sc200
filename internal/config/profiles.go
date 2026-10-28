package config

import "fmt"

// EnclosureProfile returns an independent generation template. Profiles are expanded
// into explicit YAML by discovery; they do not affect runtime configuration loading.
func EnclosureProfile(name string) (Enclosure, error) {
	e := DefaultEnclosure()
	switch name {
	case "safe":
	case "quiet":
		e.MinSpeed = 10
		e.SafeSpeed = 15
		e.Curve = []Point{{25, 10}, {30, 10}, {35, 12}, {40, 15}, {41, 20}, {45, 20}}
	default:
		return Enclosure{}, fmt.Errorf("unknown profile %q: choose safe or quiet", name)
	}
	return e, nil
}
