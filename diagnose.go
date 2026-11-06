package main

import (
	"fmt"
	"strings"
)

// diagnosis is what a report tells.
type diagnosis struct {
	App, Windows                       string
	Manufacturer, Model, Family, Board string
	BIOS, BIOSDate                     string
	// FanBackend names the way to the fans, "" if there is none, and Lines are what
	// it read of the firmware.
	FanBackend string
	Lines      []string
	// Modes and Curves tell what Sharktic turned on.
	Modes, Curves bool
}

// text is the report, to paste in an issue or a message: plain lines, the
// same in every language but its notes.
func (d diagnosis) text() string {
	var b strings.Builder
	line := func(k, v string) {
		if v == "" {
			v = "-"
		}
		fmt.Fprintf(&b, "%-13s %s\n", k+":", v)
	}
	yes := func(v bool) string {
		if v {
			return "yes"
		}
		return "no"
	}
	b.WriteString("Sharktic 진단 정보\n")
	b.WriteString("펌웨어에서 읽기만 해서 만들었고, 팬 설정은 바꾸지 않았습니다.\n\n")
	line("app", d.App)
	line("windows", d.Windows)
	line("manufacturer", d.Manufacturer)
	line("model", d.Model)
	line("family", d.Family)
	line("board", d.Board)
	line("bios", strings.TrimSpace(d.BIOS+" "+d.BIOSDate))
	backend := d.FanBackend
	if backend == "" {
		backend = "none"
	}
	line("fan backend", backend)
	line("modes on", yes(d.Modes))
	line("curves on", yes(d.Curves))
	if len(d.Lines) > 0 {
		b.WriteString("\n")
	}
	for _, l := range d.Lines {
		b.WriteString(l + "\n")
	}
	return b.String()
}
