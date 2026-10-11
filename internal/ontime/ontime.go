// Package ontime defines what "on time" means for a bus arrival.
//
// Keeping the rule in one place matters: the SQL queries receive these
// thresholds as parameters, so the dashboard and any Go-side checks can never
// quietly disagree about what counts as late.
package ontime

import "time"

// Window is the range of lateness that still counts as on time.
//
// Early arrivals get a tighter limit than late ones because a bus that leaves a
// stop early strands riders who showed up on schedule, which is worse for them
// than a bus that's a few minutes late.
type Window struct {
	MaxEarly time.Duration // how early a bus may be and still count as on time
	MaxLate  time.Duration // how late a bus may be and still count as on time
}

// Default is a widely used convention for buses: no more than 1 minute early
// and no more than 5 minutes late.
var Default = Window{MaxEarly: 1 * time.Minute, MaxLate: 5 * time.Minute}

// Status is the verdict for a single arrival.
type Status int

const (
	OnTime Status = iota
	Early
	Late
)

func (s Status) String() string {
	switch s {
	case Early:
		return "early"
	case Late:
		return "late"
	default:
		return "on time"
	}
}

// Classify sorts a delay in seconds (+ late, - early) into early, on time or late.
func (w Window) Classify(delaySecs int) Status {
	switch {
	case delaySecs < -w.EarlySecs():
		return Early
	case delaySecs > w.LateSecs():
		return Late
	default:
		return OnTime
	}
}

// EarlySecs and LateSecs give the limits as whole seconds, the unit the
// database uses. SQL queries take these as parameters.
func (w Window) EarlySecs() int { return int(w.MaxEarly / time.Second) }
func (w Window) LateSecs() int  { return int(w.MaxLate / time.Second) }
