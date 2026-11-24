package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestOverheatWatch(t *testing.T) {
	w := newOverheatWatch()
	start := time.Now()
	hot := Snapshot{GPUTemp: 90, HasGPUTemp: true}
	at := func(d time.Duration, s Snapshot) []overheat { return w.check(s, start.Add(d)) }

	if got := at(0, hot); len(got) != 0 {
		t.Fatalf("told at once: %v", got)
	}
	if got := at(overheatAfter-time.Second, hot); len(got) != 0 {
		t.Fatalf("told before a minute: %v", got)
	}
	got := at(overheatAfter, hot)
	if len(got) != 1 || got[0].sensor.name.en != "GPU" || got[0].temp != 90 {
		t.Fatalf("not told after a minute: %v", got)
	}
	if got := at(2*overheatAfter, hot); len(got) != 0 {
		t.Fatalf("told twice: %v", got)
	}
	// Just under the limit is not cool enough to tell again.
	at(2*overheatAfter+time.Second, Snapshot{GPUTemp: 85, HasGPUTemp: true})
	at(2*overheatAfter+2*time.Second, hot)
	if got := at(4*overheatAfter, hot); len(got) != 0 {
		t.Fatalf("told again without cooling: %v", got)
	}
	// Cooled, then hot for a minute again.
	at(5*overheatAfter, Snapshot{GPUTemp: 70, HasGPUTemp: true})
	at(6*overheatAfter, hot)
	if got := at(7*overheatAfter, hot); len(got) != 1 {
		t.Fatalf("not told after cooling: %v", got)
	}
}

func TestOverheatPCOnlyWithoutCPU(t *testing.T) {
	w := newOverheatWatch()
	start := time.Now()
	s := Snapshot{Temp: 99, HasTemp: true, CPUTemp: 80, HasCPUTemp: true}
	w.check(s, start)
	if got := w.check(s, start.Add(overheatAfter)); len(got) != 0 {
		t.Fatalf("PC told though the CPU is read: %v", got)
	}
	s.HasCPUTemp = false
	w.check(s, start)
	if got := w.check(s, start.Add(overheatAfter)); len(got) != 1 || got[0].sensor.name.en != "PC" {
		t.Fatalf("PC not told: %v", got)
	}
}

func TestOverheatAlertsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Sharktic", "overheat-alerts")
	if !loadOverheatAlerts(path) {
		t.Fatal("off without a file")
	}
	saveOverheatAlerts(path, false)
	if loadOverheatAlerts(path) {
		t.Fatal("on after turning off")
	}
	saveOverheatAlerts(path, true)
	if !loadOverheatAlerts(path) {
		t.Fatal("off after turning on")
	}
}
