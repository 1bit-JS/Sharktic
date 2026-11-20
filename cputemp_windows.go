package main

import (
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The PawnIO modules, signed builds of PawnIO.Modules 0.2.12 (LGPL-2.1, see
// pawnio/COPYING.modules and https://github.com/namazso/PawnIO.Modules).
var (
	//go:embed pawnio/IntelMSR.bin
	intelMSRModule []byte
	//go:embed pawnio/AMDFamily17.bin
	amdFamily17Module []byte
)

const (
	msrTemperatureTarget = 0x1A2 // TjMax in bits 23:16
	msrPackageThermal    = 0x1B1 // degrees below TjMax in bits 22:16

	// smnThmTconCurTmp is the AMD Family 17h and later temperature register,
	// Tctl, in eighths of a degree in bits 31:21.
	smnThmTconCurTmp = 0x00059800
	amdTempRangeSel  = 1 << 19 // the value reads 49 °C higher
)

// cpuThermal reads the CPU package temperature from the CPU's own registers,
// which is the one the CPU throttles by. Desktop boards rarely tell it through
// ACPI. Run by the service only.
type cpuThermal struct {
	module *pawnIOModule
	read   func() (float64, error)
	tjMax  float64 // Intel
	pciMu  windows.Handle
}

// cpuIdentity is the vendor and family of the first CPU.
func cpuIdentity() (vendor string, family int) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err != nil {
		return "", 0
	}
	defer k.Close()
	vendor, _, _ = k.GetStringValue("VendorIdentifier")
	id, _, _ := k.GetStringValue("Identifier")
	return vendor, parseCPUFamily(id)
}

var familyPattern = regexp.MustCompile(`Family (\d+)`)

// parseCPUFamily reads "Intel64 Family 6 Model 165 Stepping 2".
func parseCPUFamily(id string) int {
	m := familyPattern.FindStringSubmatch(id)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func openCPUThermal() (*cpuThermal, error) {
	vendor, family := cpuIdentity()
	switch {
	case vendor == "GenuineIntel":
		m, err := openPawnIOModule(intelMSRModule)
		if err != nil {
			return nil, err
		}
		t := &cpuThermal{module: m}
		target, err := t.readMSR(msrTemperatureTarget)
		tj := float64(target >> 16 & 0xFF)
		if err != nil || tj < 70 || tj > 125 {
			m.close()
			return nil, fmt.Errorf("no TjMax (%v, %v)", tj, err)
		}
		t.tjMax = tj
		t.read = t.readIntel
		return t, nil
	case vendor == "AuthenticAMD" && family >= 0x17:
		// Not tried on a real AMD CPU yet: the register and its scale are those
		// that Linux k10temp and LibreHardwareMonitor read.
		m, err := openPawnIOModule(amdFamily17Module)
		if err != nil {
			return nil, err
		}
		name, _ := windows.UTF16PtrFromString(`Global\Access_PCI`)
		mu, err := windows.CreateMutex(nil, false, name)
		if err != nil {
			m.close()
			return nil, err
		}
		t := &cpuThermal{module: m, pciMu: mu}
		t.read = t.readAMD
		return t, nil
	}
	return nil, fmt.Errorf("CPU %q family %d is not supported", vendor, family)
}

func (t *cpuThermal) readMSR(msr uint64) (uint64, error) {
	out, err := t.module.call("ioctl_read_msr", []uint64{msr}, 1)
	if err != nil {
		return 0, err
	}
	if len(out) != 1 {
		return 0, errors.New("no value")
	}
	return out[0], nil
}

func (t *cpuThermal) readIntel() (float64, error) {
	v, err := t.readMSR(msrPackageThermal)
	if err != nil {
		return 0, err
	}
	return t.tjMax - float64(v>>16&0x7F), nil
}

func (t *cpuThermal) readAMD() (float64, error) {
	// Other programs that read the PCI configuration take this mutex too.
	if ev, err := windows.WaitForSingleObject(t.pciMu, 100); err != nil || ev == uint32(windows.WAIT_TIMEOUT) {
		return 0, errors.New("PCI access is busy")
	}
	out, err := t.module.call("ioctl_read_smn", []uint64{smnThmTconCurTmp}, 1)
	windows.ReleaseMutex(t.pciMu)
	if err != nil {
		return 0, err
	}
	if len(out) != 1 {
		return 0, errors.New("no value")
	}
	return amdTctl(uint32(out[0])), nil
}

// amdTctl reads THM_TCON_CUR_TMP.
func amdTctl(v uint32) float64 {
	c := float64(v>>21&0x7FF) / 8
	if v&amdTempRangeSel != 0 {
		c -= 49
	}
	return c
}

// temperature returns the package temperature in degrees Celsius.
func (t *cpuThermal) temperature() (float64, bool) {
	c, err := t.read()
	if err != nil || c <= 0 || c > 125 {
		return 0, false
	}
	return c, true
}

func (t *cpuThermal) close() {
	t.module.close()
	if t.pciMu != 0 {
		windows.CloseHandle(t.pciMu)
	}
}
