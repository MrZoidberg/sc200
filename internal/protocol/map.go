// Package protocol decodes SC200 console telemetry without performing I/O.
package protocol

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Element retains both SES status and threshold bytes. Index excludes the aggregate.
type Element struct {
	Index                                          int
	Raw                                            [4]byte
	Thresholds                                     [4]byte
	Status                                         byte
	PredictedFailure, Disabled, Swapped, Installed bool
}

func (e Element) Healthy() bool { return e.Status == 1 && !e.PredictedFailure && !e.Disabled }

type Temperature struct {
	Element
	Celsius                                              int
	Available, Failed, Overtemperature, Undertemperature bool
}

func (t Temperature) Healthy() bool {
	return t.Element.Healthy() && t.Available && !t.Failed && !t.Overtemperature && !t.Undertemperature
}

type Fan struct {
	Element
	RPM                             int
	SpeedFactor                     byte
	Available, Failed, Off, Stopped bool
}

func (f Fan) Healthy() bool {
	return f.Element.Healthy() && f.Available && !f.Failed && !f.Off && !f.Stopped
}

type Map struct {
	Temperatures  []Temperature
	Fans          []Fan
	PowerSupplies []Element
}

var sectionHeader = regexp.MustCompile(`^Type\s+([[:xdigit:]]{2})\s*:\s*\S.*$`)
var consolePrompt = regexp.MustCompile(`^12Pack[^\s>]*\s*>\s*(?:_map)?$`)

// ParseMap requires a complete prompt-terminated transaction. Each relevant
// section must have an aggregate followed by at least one element. Firmware does
// not report element counts, so missing whole rows must also be detected by the
// controller's remembered required sensor identities.
func ParseMap(text string) (Map, error) {
	var out Map
	if len(text) > 65536 {
		return out, fmt.Errorf("map exceeds 64 KiB")
	}
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n")
	seen := map[int]bool{}
	section, rows := -1, 0
	complete := false
	finish := func() error {
		if (section == 2 || section == 3 || section == 4) && rows < 2 {
			return fmt.Errorf("section %02x lacks aggregate or elements", section)
		}
		return nil
	}
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if consolePrompt.MatchString(line) {
			if strings.HasSuffix(line, ">") {
				if err := finish(); err != nil {
					return Map{}, err
				}
				complete = true
				section = -1
			}
			continue
		}
		if complete {
			return Map{}, fmt.Errorf("data after completion at line %d", i+1)
		}
		if line == "_map" || strings.HasPrefix(line, "Status") && section == -1 {
			continue
		}
		if m := sectionHeader.FindStringSubmatch(line); m != nil {
			if err := finish(); err != nil {
				return Map{}, err
			}
			n, _ := strconv.ParseInt(m[1], 16, 32)
			section = int(n)
			rows = 0
			if section == 2 || section == 3 || section == 4 {
				if seen[section] {
					return Map{}, fmt.Errorf("duplicate section %02x", section)
				}
				seen[section] = true
			}
			continue
		}
		if section == -1 || strings.HasPrefix(line, "Type") {
			return Map{}, fmt.Errorf("unexpected line %d", i+1)
		}
		fields := strings.Fields(line)
		if len(fields) < 8 {
			return Map{}, fmt.Errorf("truncated row at line %d", i+1)
		}
		var raw [8]byte
		for j := range raw {
			if len(fields[j]) != 2 {
				return Map{}, fmt.Errorf("invalid byte at line %d", i+1)
			}
			n, err := strconv.ParseUint(fields[j], 16, 8)
			if err != nil {
				return Map{}, fmt.Errorf("invalid hex at line %d", i+1)
			}
			raw[j] = byte(n)
		}
		if len(fields) > 8 && (fields[8] != "-" || len(fields) != 11) {
			return Map{}, fmt.Errorf("invalid row suffix at line %d", i+1)
		}
		if (section == 2 || section == 3 || section == 4) && len(fields) != 8 {
			return Map{}, fmt.Errorf("unexpected relevant row suffix")
		}
		rows++
		if rows == 1 {
			continue
		}
		e := Element{Index: rows - 2, Raw: [4]byte(raw[:4]), Thresholds: [4]byte(raw[4:]), Status: raw[0] & 15, PredictedFailure: raw[0]&64 != 0, Disabled: raw[0]&32 != 0, Swapped: raw[0]&16 != 0, Installed: raw[0]&15 != 5}
		switch section {
		case 2:
			out.PowerSupplies = append(out.PowerSupplies, e)
		case 4:
			out.Temperatures = append(out.Temperatures, Temperature{Element: e, Celsius: int(raw[2]) - 20, Available: e.Healthy() && raw[2] != 0, Failed: raw[1]&64 != 0, Overtemperature: raw[3]&12 != 0, Undertemperature: raw[3]&3 != 0})
		case 3:
			factor := (raw[1] >> 3) & 3
			speed := int(raw[1]&7)<<8 | int(raw[2])
			rpm := 0
			switch factor {
			case 0:
				rpm = speed * 10
			case 1:
				rpm = speed*10 + 20480
			case 2:
				rpm = speed * 100
			}
			out.Fans = append(out.Fans, Fan{Element: e, RPM: rpm, SpeedFactor: factor, Available: e.Healthy() && factor != 3, Failed: raw[3]&64 != 0, Off: raw[3]&16 != 0, Stopped: rpm == 0})
		}
	}
	if !complete || !seen[3] || !seen[4] {
		return Map{}, fmt.Errorf("incomplete map: need cooling, temperature and final prompt")
	}
	return out, nil
}
