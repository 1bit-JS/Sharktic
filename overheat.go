package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
)

// The app tells, with a notification, when a part stays hotter than it
// should for a while, on any PC: the limits are above what parts run at under
// load by design, as a Ryzen at 95°C or a GPU at its 87°C target, so that a
// notification means something is wrong, as a stopped fan or dust. The user
// can turn them off.

const (
	// overheatAfter is how long a part must stay over its limit.
	overheatAfter = time.Minute
	// overheatCoolBy is how far under its limit a part must cool before it
	// can be told of again, so that one hovering at the limit is told once.
	overheatCoolBy = 5
)

// overheatSensor is a temperature the app watches.
type overheatSensor struct {
	name  msg
	limit float64 // told of above it
	read  func(Snapshot) (float64, bool)
}

var overheatSensors = []overheatSensor{
	{msg{"CPU", "CPU"}, 95, func(s Snapshot) (float64, bool) { return s.CPUTemp, s.HasCPUTemp }},
	// The hottest ACPI zone, which is often the CPU's: only where the CPU's
	// own is not read, so that one heat is not told twice.
	{msg{"PC", "PC"}, 90, func(s Snapshot) (float64, bool) { return s.Temp, s.HasTemp && !s.HasCPUTemp }},
	{msg{"GPU", "GPU"}, 87, func(s Snapshot) (float64, bool) { return s.GPUTemp, s.HasGPUTemp }},
	{msg{"SSD", "SSD"}, 75, func(s Snapshot) (float64, bool) { return s.DiskTemp, s.HasDiskTemp }},
}

// overheatWatch follows the sensors over the readings.
type overheatWatch struct {
	// since is when each sensor went over its limit, zero while it is not.
	since []time.Time
	// told is whether each was told of, until it cools.
	told []bool
}

func newOverheatWatch() *overheatWatch {
	return &overheatWatch{
		since: make([]time.Time, len(overheatSensors)),
		told:  make([]bool, len(overheatSensors)),
	}
}

// overheat is a sensor to tell of, with its temperature.
type overheat struct {
	sensor overheatSensor
	temp   float64
}

// check takes a reading made at now, and returns the sensors to tell of now.
func (w *overheatWatch) check(s Snapshot, now time.Time) []overheat {
	var hot []overheat
	for i, sensor := range overheatSensors {
		temp, ok := sensor.read(s)
		if !ok || temp <= sensor.limit {
			w.since[i] = time.Time{}
			if !ok || temp <= sensor.limit-overheatCoolBy {
				w.told[i] = false
			}
			continue
		}
		if w.since[i].IsZero() {
			w.since[i] = now
		}
		if !w.told[i] && now.Sub(w.since[i]) >= overheatAfter {
			w.told[i] = true
			hot = append(hot, overheat{sensor, temp})
		}
	}
	return hot
}

// notifyOverheat tells of a hot part, and of the program that uses the CPU
// the most, which is often why.
func notifyOverheat(h overheat, s Snapshot) {
	body := fmt.Sprintf(tr("Over %.0f°C for a minute. Check that the fans are spinning and the vents are clear.",
		"1분 넘게 %.0f°C를 넘었습니다. 팬이 돌고 있는지, 통풍구가 막히지 않았는지 확인해 주세요."), h.sensor.limit)
	if top := visiblePrograms(s.TopCPU); len(top) > 0 {
		body += "\n" + fmt.Sprintf(tr("Using the CPU the most: %s (%.0f%%)", "CPU를 가장 많이 쓰는 프로그램: %s (%.0f%%)"), top[0].Name, top[0].CPU)
	}
	n := mygo.NewNotification(mygo.NotificationOptions{
		ID:    "overheat-" + strings.ToLower(h.sensor.name.en),
		Title: fmt.Sprintf(tr("%s is hot: %.0f°C", "%s 온도가 높습니다: %.0f°C"), h.sensor.name, h.temp),
		Body:  body,
	})
	if err := n.Show(); err != nil {
		log.Println("notification:", err)
	}
}

// overheatAlertsPath is the file that keeps whether the alerts are on.
func overheatAlertsPath() string { return configPath("overheat-alerts") }

// loadOverheatAlerts tells whether the alerts are on: unless turned off.
func loadOverheatAlerts(path string) bool {
	data, err := os.ReadFile(path)
	return err != nil || strings.TrimSpace(string(data)) != "off"
}

func saveOverheatAlerts(path string, on bool) {
	if path == "" {
		return
	}
	value := "on\n"
	if !on {
		value = "off\n"
	}
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	if err == nil {
		err = os.WriteFile(path, []byte(value), 0o644)
	}
	if err != nil {
		log.Println("overheat alerts:", err)
	}
}
