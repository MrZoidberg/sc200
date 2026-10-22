package control

import (
	"github.com/MrZoidberg/sc200/internal/protocol"
	"time"
)

type State string

const (
	Starting     State = "starting"
	Healthy      State = "healthy"
	Failsafe     State = "failsafe"
	Disconnected State = "disconnected"
	Stopping     State = "stopping"
)

// Operations and Errors use fixed fields so monitoring cannot grow label cardinality.
type Counters struct{ Open, Read, Command, Fallback, Shutdown uint64 }
type Snapshot struct {
	ID               string
	State            State
	Reason           string
	TransitionTime   time.Time
	Healthy          bool
	Fresh            bool
	Target           int
	CompletedCommand int
	LastTelemetry    time.Time
	LastCommand      time.Time
	Telemetry        protocol.Map
	Operations       Counters
	Errors           Counters
}

func cloneSnapshot(s Snapshot) Snapshot {
	s.Telemetry.Temperatures = append([]protocol.Temperature(nil), s.Telemetry.Temperatures...)
	s.Telemetry.Fans = append([]protocol.Fan(nil), s.Telemetry.Fans...)
	s.Telemetry.PowerSupplies = append([]protocol.Element(nil), s.Telemetry.PowerSupplies...)
	return s
}
