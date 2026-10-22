package protocol

import (
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	b, e := os.ReadFile("testdata/map-12pack.txt")
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func small(temp, fan string) string {
	return "Type 03 : Cooling Fan\n00 00 00 00 00 00 00 00\n" + fan + " 00 00 00 00\nType 04 : Temp. Sensor\n00 00 00 00 00 00 00 00\n" + temp + " 55 55 12 12\n12Pack.Slot0.101.9 >\n"
}
func TestSample(t *testing.T) {
	for _, s := range []string{fixture(t), strings.ReplaceAll(fixture(t), "\n", "\r\n"), strings.ReplaceAll(fixture(t), " ", "\t"), strings.ReplaceAll(fixture(t), "\n", "\r")} {
		m, e := ParseMap(s)
		if e != nil {
			t.Fatal(e)
		}
		if len(m.Temperatures) != 4 || len(m.Fans) != 4 || len(m.PowerSupplies) != 2 {
			t.Fatalf("counts: %+v", m)
		}
		for i, want := range []int{30, 32, 31, 21} {
			v := m.Temperatures[i]
			if v.Celsius != want || v.Index != i || !v.Healthy() || v.Thresholds[0] == 0 {
				t.Fatalf("temperature %+v", v)
			}
		}
		for i, want := range []int{2680, 2750, 2700, 2730} {
			if m.Fans[i].RPM != want || !m.Fans[i].Healthy() {
				t.Fatalf("fan %+v", m.Fans[i])
			}
		}
		// The captured PSU status is noncritical (03), not SES Not installed (05).
		if m.PowerSupplies[1].Status != 3 || !m.PowerSupplies[1].Installed {
			t.Fatal("lost original PSU semantics")
		}
	}
}
func TestVariableCountsAndFlags(t *testing.T) {
	for _, tt := range []struct {
		name, temp, fan string
		tempOK, fanOK   bool
		rpm             int
	}{
		{"normal", "01 00 32 00", "01 01 0C 20", true, true, 2680},
		{"zero", "01 00 00 00", "01 00 00 20", false, false, 0},
		{"status", "02 00 32 00", "07 01 0C 20", false, false, 2680},
		{"predicted", "41 00 32 00", "41 01 0C 20", false, false, 2680},
		{"disabled", "21 00 32 00", "21 01 0C 20", false, false, 2680},
		{"fault", "01 40 32 00", "01 01 0C 60", false, false, 2680},
		{"hot warning", "01 00 32 04", "01 01 0C 30", false, false, 2680},
		{"hot fail", "01 00 32 08", "01 01 0C 20", false, true, 2680},
		{"cold", "01 00 32 03", "01 01 0C 20", false, true, 2680},
		{"absent", "05 00 32 00", "05 01 0C 20", false, false, 2680},
		{"factor1", "01 00 32 00", "01 08 00 20", true, true, 20480},
		{"factor1max", "01 00 32 00", "01 0F FF 20", true, true, 40950},
		{"factor2max", "01 00 32 00", "01 17 FF 20", true, true, 204700},
		{"factor3", "01 00 32 00", "01 18 01 20", true, false, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, e := ParseMap(small(tt.temp, tt.fan))
			if e != nil {
				t.Fatal(e)
			}
			if len(m.Temperatures) != 1 || len(m.Fans) != 1 || m.Temperatures[0].Healthy() != tt.tempOK || m.Fans[0].Healthy() != tt.fanOK || m.Fans[0].RPM != tt.rpm {
				t.Fatalf("%+v", m)
			}
		})
	}
	s := "Type 02 : Power Supply\n00 00 00 00 00 00 00 00\n05 00 00 33 00 00 00 00\n" + small("01 00 32 00", "01 01 0C 20")
	m, e := ParseMap(s)
	if e != nil || m.PowerSupplies[0].Installed {
		t.Fatalf("absent PSU: %+v %v", m, e)
	}
}
func TestRejectMalformed(t *testing.T) {
	good := small("01 00 32 00", "01 01 0C 20")
	unsupported, e := os.ReadFile("testdata/unsupported-temp-rd.txt")
	if e != nil {
		t.Fatal(e)
	}
	for name, s := range map[string]string{
		"empty": "", "unsupported": string(unsupported), "too large": strings.Repeat("x", 65537),
		"no prompt": strings.Split(good, "12Pack")[0], "prompt only": "12Pack.Slot0.101.9 >",
		"missing temp":   strings.Split(good, "Type 04")[0] + "12Pack.Slot0.101.9 >",
		"missing fan":    good[strings.Index(good, "Type 04"):],
		"duplicate":      strings.Replace(good, "Type 04", "Type 03", 1),
		"aggregate only": strings.Replace(good, "01 01 0C 20 00 00 00 00\n", "", 1),
		"truncated":      strings.Replace(good, "01 01 0C 20", "01 01", 1),
		"hex":            strings.Replace(good, "0C", "GG", 1),
		"wide":           strings.Replace(good, "0C", "000C", 1),
		"header":         strings.Replace(good, "Type 04", "Type ZZ", 1),
		"extra":          strings.Replace(good, "01 01 0C 20", "01 01 0C 20 00", 1),
		"suffix":         strings.Replace(good, "01 01 0C 20", "01 01 0C 20 -", 1),
		"after":          good + "unexpected",
		"arbitrary":      "random hex 01 01 01 01\n" + good,
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := ParseMap(s); e == nil {
				t.Fatal("accepted malformed map")
			}
		})
	}
}
func FuzzParseMap(f *testing.F) {
	b, e := os.ReadFile("testdata/map-12pack.txt")
	if e != nil {
		f.Fatal(e)
	}
	f.Add(string(b))
	f.Add(small("01 00 32 00", "01 01 0C 20"))
	f.Add("Type 04 : incomplete")
	f.Fuzz(func(t *testing.T, s string) {
		m, e := ParseMap(s)
		if e == nil && (len(m.Temperatures) == 0 || len(m.Fans) == 0) {
			t.Fatal("accepted empty telemetry")
		}
	})
}
