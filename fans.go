package main

// minFanLevel is the lowest fan level of a curve, in percent.
const minFanLevel = 20

// fanMode is a performance mode to offer: its label, and the value its backend
// takes. Only the modes a backend lists are ever set.
type fanMode struct {
	label string
	value uint32
}

// fanBackend is one vendor's way to the fans. The panel, the curve window and the
// kept choice go through it, and show what it has. Except for read, which the
// sampler calls on its goroutine, it is called on the main thread.
type fanBackend interface {
	// name tells the backend in a diagnosis.
	name() string
	// read adds the speeds of the fans.
	read(out *Snapshot)
	// modes lists the performance modes, nil if there are none.
	modes() []fanMode
	// setMode sets a mode by its value, which also brings back the firmware's
	// curves if canLeaveCurves.
	setMode(value uint32) error
	// hasCurves tells whether our fan curves can be set.
	hasCurves() bool
	// setCurves sets our fan curves, in place of the firmware's.
	setCurves(cpu, gpu fanCurve) error
	// forgetCurves tells that the firmware may have dropped our curves, as during
	// a sleep, so that the next setCurves starts over.
	forgetCurves()
	// canLeaveCurves tells whether leaveCurves or a mode brings back the firmware's
	// curves. Without it, ours stay when the app has gone.
	canLeaveCurves() bool
	// leaveCurves brings back the firmware's curves if ours are on and it can.
	leaveCurves()
	// diagnosisLines reads what a report tells of the firmware. It must only read.
	diagnosisLines() []string
}

// detectFans returns the backend of this PC, or nil if it has none.
func detectFans() fanBackend {
	if a := newASUSControl(); a != nil {
		return a
	}
	return nil
}
