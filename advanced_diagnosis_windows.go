package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// serviceLogLines is how many of the service's last log lines a diagnosis
// tells.
const serviceLogLines = 8

// advancedDiagnosis tells of advanced fan control, for a report: whether it is
// on, what its service found, and the end of its log. It only reads.
func (a *app) advancedDiagnosis() []string {
	s := a.snap
	installed, pid := serviceStatus()
	var lines []string
	line := func(k, v string) {
		if v == "" {
			v = "-"
		}
		lines = append(lines, fmt.Sprintf("%-13s %s", k+":", v))
	}
	pawnIO := "not installed"
	if v, _, ok := pawnIOInstalled(); ok {
		pawnIO = fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
	}
	line("pawnio", pawnIO)
	switch {
	case !installed:
		line("service", "not installed")
		return lines
	case pid == 0:
		line("service", "stopped")
	case !s.ServiceRunning:
		line("service", "running, not answering")
	default:
		line("service", s.ServiceVersion)
		cpu := degrees(s.CPUTemp, s.HasCPUTemp)
		if s.CPUSource != "" { // an older service does not tell it
			cpu += " (" + s.CPUSource + ")"
		}
		line("cpu temp", cpu)
		line("board chip", s.SuperIO)
		line("fans", s.fansLine())
	}
	if path, err := serviceLogPath(); err == nil {
		for _, l := range lastLines(path, serviceLogLines) {
			lines = append(lines, "log: "+l)
		}
	}
	return lines
}

// lastLines returns up to n last lines of a file, reading only its end.
func lastLines(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	const tail = 4096
	cut := false
	if st, err := f.Stat(); err == nil && st.Size() > tail {
		if _, err := f.Seek(st.Size()-tail, io.SeekStart); err != nil {
			return nil
		}
		cut = true
	}
	b, _ := io.ReadAll(io.LimitReader(f, tail))
	text := strings.TrimRight(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	if text == "" {
		return nil
	}
	all := strings.Split(text, "\n")
	if cut && len(all) > 1 {
		all = all[1:] // the first was cut in the middle
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}
