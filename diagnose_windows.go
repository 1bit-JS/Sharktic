package main

import (
	"fmt"
	"strconv"
	"strings"
	"syscall"
)

const (
	biosKey    = `HARDWARE\DESCRIPTION\System\BIOS`
	windowsKey = `SOFTWARE\Microsoft\Windows NT\CurrentVersion`
)

// diagnose reads what a report tells: of the PC from the registry, which
// needs no administrator rights, and of the firmware by the backend, which only
// reads.
func diagnose(fans fanBackend, app string) diagnosis {
	hklm := syscall.Handle(syscall.HKEY_LOCAL_MACHINE)
	d := diagnosis{
		App:          app,
		Windows:      windowsVersion(),
		Manufacturer: regString(hklm, biosKey, "SystemManufacturer"),
		Model:        regString(hklm, biosKey, "SystemProductName"),
		Family:       regString(hklm, biosKey, "SystemFamily"),
		Board:        regString(hklm, biosKey, "BaseBoardProduct"),
		BIOS:         regString(hklm, biosKey, "BIOSVersion"),
		BIOSDate:     regString(hklm, biosKey, "BIOSReleaseDate"),
	}
	if fans != nil {
		d.FanBackend = fans.name()
		d.Modes = len(fans.modes()) > 0
		d.Curves = fans.hasCurves()
		d.Lines = fans.diagnosisLines()
	}
	return d
}

// windowsVersion tells the edition, the version and the build, as "Windows 10
// Pro 22H2 (19045.6456)".
func windowsVersion() string {
	hklm := syscall.Handle(syscall.HKEY_LOCAL_MACHINE)
	name := regString(hklm, windowsKey, "ProductName")
	build := regString(hklm, windowsKey, "CurrentBuild")
	// Windows 11 still calls itself Windows 10 there.
	if n, err := strconv.Atoi(build); err == nil && n >= 22000 {
		name = strings.Replace(name, "Windows 10", "Windows 11", 1)
	}
	if ubr, ok := regNumber(hklm, windowsKey, "UBR"); ok {
		build = fmt.Sprintf("%s.%d", build, ubr)
	}
	return strings.TrimSpace(fmt.Sprintf("%s %s (%s)", name, regString(hklm, windowsKey, "DisplayVersion"), build))
}
