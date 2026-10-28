package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/MrZoidberg/sc200/internal/config"
	"github.com/MrZoidberg/sc200/internal/discovery"
	"github.com/charmbracelet/x/ansi"
)

type setupLoaded struct {
	cfg      config.Config
	original []byte
	outcomes []discovery.Outcome
	err      error
}
type setupModel struct {
	load            tea.Cmd
	cfg             config.Config
	original        []byte
	outcomes        []discovery.Outcome
	phase           string
	cursor          int
	field           int
	input           string
	draft           config.Enclosure
	message         string
	err             error
	start, accepted bool
	width, height   int
}

func setup(ctx context.Context, path string, out, diag io.Writer, o Options) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	input := o.SetupInput
	if input == nil {
		tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if err != nil {
			_, _ = fmt.Fprintln(diag, "Setup requires an interactive terminal; run sudo sc200 --setup from a terminal.")
			return 1
		}
		defer func() { _ = tty.Close() }()
		input = tty
	}
	identity := o.DiscoveryIdentity
	if identity == nil {
		identity = discovery.Identity
	}
	scan := o.Discover
	if scan == nil {
		scan = func(ctx context.Context, p []string, baud int) (discovery.Result, error) {
			return discovery.Scan(ctx, p, baud, discovery.Options{})
		}
	}
	m := setupModel{phase: "scan", load: func() tea.Msg {
		original, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return setupLoaded{err: err}
		}
		r, err := scan(ctx, nil, 38400)
		if err != nil {
			return setupLoaded{err: fmt.Errorf("discovery: %w (stop a running sc200 service before setup)", err)}
		}
		data, err := config.MergeFile(path, r.Enclosures, false, identity)
		if err != nil {
			return setupLoaded{err: err}
		}
		if len(r.Enclosures) == 0 && len(data) == 0 {
			return setupLoaded{outcomes: r.Outcomes, err: errors.New("no supported devices found; configuration unchanged")}
		}
		cfg, err := config.Decode(strings.NewReader(string(data)))
		if err == nil {
			existing := map[string]bool{}
			if len(original) > 0 {
				old, e := config.Decode(strings.NewReader(string(original)))
				if e != nil {
					return setupLoaded{err: e}
				}
				for _, entry := range old.Enclosures {
					existing[entry.ID] = true
				}
			}
			for i := range cfg.Enclosures {
				if !existing[cfg.Enclosures[i].ID] {
					cfg.Enclosures[i].Enabled = false
				}
			}
		}
		return setupLoaded{cfg: cfg, original: original, outcomes: r.Outcomes, err: err}
	}}
	runUI := o.SetupUI
	if runUI == nil {
		runUI = func(model tea.Model) (tea.Model, error) {
			return tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(out)).Run()
		}
	}
	result, err := runUI(m)
	if err != nil {
		_, _ = fmt.Fprintln(diag, "Setup:", err)
		return 1
	}
	final := result.(setupModel)
	if final.err != nil {
		for _, o := range final.outcomes {
			if o.Err != nil {
				_, _ = fmt.Fprintf(diag, "%s: %v\n", o.Port, o.Err)
			}
		}
		_, _ = fmt.Fprintln(diag, "Setup:", final.err)
		return 1
	}
	if !final.accepted {
		_, _ = fmt.Fprintln(out, "Setup canceled; configuration unchanged.")
		return 0
	}
	if err := config.SaveSetupFile(path, final.cfg.Enclosures, final.original, identity); err != nil {
		_, _ = fmt.Fprintln(diag, err)
		return 1
	}
	_, _ = fmt.Fprintln(out, "Configuration saved:", path)
	if final.start {
		systemctl := o.Systemctl
		if systemctl == nil {
			systemctl = func(ctx context.Context, args ...string) error {
				c := exec.CommandContext(ctx, "systemctl", args...)
				c.Stdout = out
				c.Stderr = diag
				return c.Run()
			}
		}
		if err := systemctl(ctx, "enable", "--now", "sc200.service"); err != nil {
			_, _ = fmt.Fprintln(diag, "Configuration saved, but service start failed:", err)
			return 1
		}
		_, _ = fmt.Fprintln(out, "sc200.service enabled and started.")
	} else {
		_, _ = fmt.Fprintln(out, "Service left stopped. Start with: sudo systemctl enable --now sc200.service")
	}
	return 0
}

func (m setupModel) Init() tea.Cmd { return m.load }
func (m setupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = size.Width, size.Height
		return m, nil
	}
	if r, ok := msg.(setupLoaded); ok {
		m.cfg, m.original, m.outcomes, m.err = r.cfg, r.original, r.outcomes, r.err
		if r.err != nil {
			return m, tea.Quit
		}
		m.phase = "devices"
		return m, nil
	}
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	key := k.String()
	if key == "ctrl+c" || (key == "esc" && m.phase != "custom") {
		return m, tea.Quit
	}
	if m.phase == "scan" {
		return m, nil
	}
	if m.phase == "custom" {
		switch key {
		case "esc":
			m.phase = "devices"
			m.message = "Custom changes discarded."
		case "ctrl+u":
			m.input = ""
		case "backspace":
			r := []rune(m.input)
			if len(r) > 0 {
				m.input = string(r[:len(r)-1])
			}
		case "enter":
			if err := m.setCustom(); err != nil {
				m.message = err.Error()
				return m, nil
			}
			m.field++
			if m.field == 5 {
				if err := m.draft.Validate(); err != nil {
					m.field = 0
					m.message = err.Error()
					m.input = m.customValue()
					return m, nil
				}
				m.cfg.Enclosures[m.cursor] = m.draft
				m.phase = "devices"
				m.message = "Custom settings applied."
			} else {
				m.input = m.customValue()
				m.message = ""
			}
		default:
			if k.Text != "" {
				m.input += k.Text
			}
		}
		return m, nil
	}
	if key == "up" || key == "k" {
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	}
	if key == "down" || key == "j" {
		if m.cursor+1 < len(m.cfg.Enclosures) {
			m.cursor++
		}
		return m, nil
	}
	if m.phase == "review" {
		switch key {
		case "s", "space", " ":
			m.start = !m.start
		case "backspace":
			m.phase = "devices"
		case "enter":
			m.accepted = true
			return m, tea.Quit
		}
		return m, nil
	}
	n := len(m.cfg.Enclosures)
	switch key {
	case "space", " ":
		if n > 0 {
			m.cfg.Enclosures[m.cursor].Enabled = !m.cfg.Enclosures[m.cursor].Enabled
		}
	case "s", "q":
		if n > 0 {
			name := "safe"
			if key == "q" {
				name = "quiet"
			}
			p, _ := config.EnclosureProfile(name)
			e := &m.cfg.Enclosures[m.cursor]
			e.MinSpeed, e.SafeSpeed, e.CriticalTemperature, e.Hysteresis, e.Curve = p.MinSpeed, p.SafeSpeed, p.CriticalTemperature, p.Hysteresis, p.Curve
			m.message = "Applied " + name + " profile."
		}
	case "c":
		if n > 0 {
			m.draft = m.cfg.Enclosures[m.cursor]
			m.field = 0
			m.input = m.customValue()
			m.phase = "custom"
			m.message = ""
		}
	case "enter":
		if err := m.cfg.Validate(); err != nil {
			m.message = err.Error()
		} else {
			m.phase = "review"
			m.message = ""
		}
	}
	return m, nil
}
func (m setupModel) customValue() string {
	switch m.field {
	case 0:
		return strconv.Itoa(m.draft.MinSpeed)
	case 1:
		return strconv.Itoa(m.draft.SafeSpeed)
	case 2:
		return fmt.Sprint(m.draft.CriticalTemperature)
	case 3:
		return fmt.Sprint(m.draft.Hysteresis)
	}
	var parts []string
	for _, p := range m.draft.Curve {
		parts = append(parts, fmt.Sprintf("%g:%d", p.Temperature, p.Speed))
	}
	return strings.Join(parts, ",")
}
func (m *setupModel) setCustom() error {
	var err error
	switch m.field {
	case 0:
		m.draft.MinSpeed, err = strconv.Atoi(m.input)
	case 1:
		m.draft.SafeSpeed, err = strconv.Atoi(m.input)
	case 2:
		m.draft.CriticalTemperature, err = strconv.ParseFloat(m.input, 64)
	case 3:
		m.draft.Hysteresis, err = strconv.ParseFloat(m.input, 64)
	case 4:
		var curve []config.Point
		for _, part := range strings.Split(m.input, ",") {
			a, b, ok := strings.Cut(strings.TrimSpace(part), ":")
			if !ok {
				return errors.New("use temperature:speed pairs, separated by commas")
			}
			t, e := strconv.ParseFloat(a, 64)
			if e != nil {
				return e
			}
			s, e := strconv.Atoi(b)
			if e != nil {
				return e
			}
			curve = append(curve, config.Point{Temperature: t, Speed: s})
		}
		m.draft.Curve = curve
	}
	return err
}
func (m setupModel) View() tea.View {
	var b strings.Builder
	b.WriteString("sc200 setup\n\n")
	switch m.phase {
	case "scan":
		b.WriteString("Discovering supported enclosures… (read-only probes)\n")
	case "custom":
		labels := []string{"Minimum fan speed (%)", "Fallback fan speed (%)", "Critical temperature (°C)", "Hysteresis (°C)", "Fan curve (°C:%, comma separated)"}
		fmt.Fprintf(&b, "Custom settings for %s · %d/5\n\n%s\n> %s\n\nEnter: next · Ctrl+U: clear · Esc: discard\n", m.draft.ID, m.field+1, labels[m.field], m.input)
	default:
		if m.phase == "devices" {
			b.WriteString("Select devices to control and configure each one.\n\n")
		}
		height := m.height
		if height == 0 {
			height = 24
		}
		pageSize := max(1, (height-14)/3)
		first := m.cursor / pageSize * pageSize
		last := min(len(m.cfg.Enclosures), first+pageSize)
		fmt.Fprintf(&b, "Devices %d–%d of %d (↑/↓ to browse)\n", first+1, last, len(m.cfg.Enclosures))
		for i := first; i < last; i++ {
			e := m.cfg.Enclosures[i]
			cursor := " "
			if i == m.cursor {
				cursor = ">"
			}
			enabled := " "
			if e.Enabled {
				enabled = "x"
			}
			fmt.Fprintf(&b, "%s [%s] %s  %s\n    min %d%% · fallback %d%% · critical %g°C · hysteresis %g°C\n    curve: ", cursor, enabled, e.ID, e.Port, e.MinSpeed, e.SafeSpeed, e.CriticalTemperature, e.Hysteresis)
			for j, p := range e.Curve {
				if j > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%g°C:%d%%", p.Temperature, p.Speed)
			}
			b.WriteString("\n")
		}
		if m.phase == "review" {
			start := "no"
			if m.start {
				start = "yes"
			}
			fmt.Fprintf(&b, "\nSave these settings? Enable and start service: %s\n", start)
			if m.cfg.DryRun {
				b.WriteString("Dry-run remains enabled: no fan speed writes.\n")
			}
			b.WriteString("↑/↓: browse · S: toggle service start\nEnter: save · Backspace: edit · Esc: cancel\n")
		} else {
			skipped := 0
			for _, o := range m.outcomes {
				if o.Err != nil {
					skipped++
				}
			}
			if skipped > 0 {
				fmt.Fprintf(&b, "Skipped %d unavailable or unsupported ports.\nDetails: sc200 discover --verbose\n", skipped)
			}
			b.WriteString("\n↑/↓: device · Space: enable/disable\nS: safe · Q: quiet · C: custom · Enter: review · Esc: cancel\nQuiet fallback: 15%; critical temperature/fan faults: 100%.\n")
		}
	}
	if m.message != "" {
		fmt.Fprintf(&b, "\n%s\n", m.message)
	}
	width := m.width
	if width == 0 {
		width = 80
	}
	if m.height > 0 && (m.height < 16 || width < 60) {
		return tea.NewView("Terminal too small. Resize to at least 60×16.\nEsc: cancel")
	}
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}
