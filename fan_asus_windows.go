package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"syscall"
	"time"
	"unsafe"
)

const (
	atkControlCode = 0x0022240C
	atkGet         = 0x53545344 // DSTS
	atkSet         = 0x53564544 // DEVS

	atkCPUFan          = 0x00110013
	atkGPUFan          = 0x00110014
	atkMidFan          = 0x00110031
	atkPerformanceMode = 0x00120075
	atkCPUCurve        = 0x00110024
	atkGPUCurve        = 0x00110025

	atkPresent = 0x00010000 // of a value that a get returns
	atkFanMax  = 120        // in hundreds of RPM: more is not a speed

	balancedMode = 0
)

var procDeviceIoControl = kernel32.NewProc("DeviceIoControl")

// asusModes are the modes to choose from, of the values that the firmware takes.
// Others stay out: 3 is full speed, and on the G512LI's firmware no branch handles
// it.
var asusModes = []fanMode{{"조용함", 2}, {"균형", balancedMode}, {"터보", 1}}

// asusControl talks to the firmware of ASUS laptops through the ASUS System
// Control Interface driver, which needs no administrator rights. ASUS does not
// document it. It is nil on other PCs.
type asusControl struct {
	handle   syscall.Handle
	curvesOn bool // our fan curves are on, in place of the firmware's
	// hasModes and curveDevices tell whether the firmware has the performance modes
	// and the fan curves. Some models have curves without modes that ACPI shows.
	hasModes, curveDevices bool
	// fake answers the calls in place of the driver, in tests.
	fake func(method, device uint32, params []byte) (uint32, bool)
}

func newASUSControl() *asusControl {
	name, _ := syscall.UTF16PtrFromString(`\\.\ATKACPI`)
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil
	}
	a := &asusControl{handle: h}
	a.hasModes = a.present(atkPerformanceMode)
	a.curveDevices = a.present(atkCPUCurve) && a.present(atkGPUCurve)
	return a
}

// present tells whether a get finds a device. A get of a curve returns its first
// temperatures, which are above the presence bit.
func (a *asusControl) present(device uint32) bool {
	raw, ok := a.callValue(atkGet, device, 0)
	return ok && atkHasValue(raw)
}

func (a *asusControl) call(method, device uint32, params []byte) (uint32, bool) {
	if a.fake != nil {
		return a.fake(method, device, params)
	}
	in := make([]byte, 12+len(params))
	binary.LittleEndian.PutUint32(in[0:], method)
	binary.LittleEndian.PutUint32(in[4:], uint32(4+len(params))) // the size of what follows
	binary.LittleEndian.PutUint32(in[8:], device)
	copy(in[12:], params)
	var out [16]byte
	var n uint32
	r, _, _ := procDeviceIoControl.Call(uintptr(a.handle), atkControlCode,
		uintptr(unsafe.Pointer(&in[0])), uintptr(len(in)), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)),
		uintptr(unsafe.Pointer(&n)), 0)
	return binary.LittleEndian.Uint32(out[:]), r != 0
}

// callValue calls with one number.
func (a *asusControl) callValue(method, device, value uint32) (uint32, bool) {
	return a.call(method, device, binary.LittleEndian.AppendUint32(nil, value))
}

// atkHasValue tells whether a get found the device. A missing one returns 0 or
// a negative error.
func atkHasValue(raw uint32) bool {
	return int32(raw) >= atkPresent
}

// fanRPM returns the speed of a fan from what a get returned.
func fanRPM(raw uint32) (int, bool) {
	fan := raw & 0xFFFF
	if !atkHasValue(raw) || fan > atkFanMax {
		return 0, false
	}
	return int(fan) * 100, true
}

func (a *asusControl) name() string { return "asus" }

// read adds the speeds of the fans.
func (a *asusControl) read(out *Snapshot) {
	if raw, ok := a.callValue(atkGet, atkCPUFan, 0); ok {
		out.CPUFan, out.HasCPUFan = fanRPM(raw)
	}
	if raw, ok := a.callValue(atkGet, atkGPUFan, 0); ok {
		out.GPUFan, out.HasGPUFan = fanRPM(raw)
	}
}

// modes lists the performance modes. The firmware does not tell which one is on:
// a get returns the same in all of them.
func (a *asusControl) modes() []fanMode {
	if !a.hasModes {
		return nil
	}
	return asusModes
}

func (a *asusControl) hasCurves() bool { return a.curveDevices }

// setMode sets a mode, which also brings back the firmware's curves. It takes only
// the modes of asusModes, whatever the caller asks.
func (a *asusControl) setMode(mode uint32) error {
	if !slices.ContainsFunc(asusModes, func(m fanMode) bool { return m.value == mode }) {
		return fmt.Errorf("mode %d is not one to set", mode)
	}
	if r, ok := a.callValue(atkSet, atkPerformanceMode, mode); !ok || r != 1 {
		return errors.New("the firmware refused the performance mode")
	}
	a.curvesOn = false
	return nil
}

// setCurves sets our fan curves. Where there are modes, a curve applies to the
// current mode, so that the first one needs a known mode. If the firmware
// refuses them, it goes back to its own by a mode, where there is one.
func (a *asusControl) setCurves(cpu, gpu fanCurve) error {
	if !a.curvesOn && a.hasModes {
		if err := a.setMode(balancedMode); err != nil {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, c := range []struct {
		device uint32
		curve  []byte
	}{{atkCPUCurve, cpu.bytes()}, {atkGPUCurve, gpu.bytes()}} {
		if r, ok := a.call(atkSet, c.device, c.curve); !ok || r != 1 {
			if a.hasModes {
				a.setMode(balancedMode)
			}
			return errors.New("the firmware refused the fan curve")
		}
	}
	a.curvesOn = true
	return nil
}

func (a *asusControl) forgetCurves() { a.curvesOn = false }

// canLeaveCurves tells whether there is a mode to bring back the firmware's curves:
// without one there is no known way, and ours stay.
func (a *asusControl) canLeaveCurves() bool { return a.hasModes }

// leaveCurves brings back the firmware's curves if ours are on, by a mode.
func (a *asusControl) leaveCurves() {
	if a.curvesOn && a.hasModes {
		a.setMode(balancedMode)
	}
}

// asusDiagDevices are the firmware devices that a diagnosis reads, so that support
// on other models can be told from a user's report: those Sharktic uses, and those
// other ASUS models use for the same.
var asusDiagDevices = []struct {
	id   uint32
	name string
}{
	{atkCPUFan, "CPU 팬 속도"},
	{atkGPUFan, "GPU 팬 속도"},
	{atkMidFan, "중간 팬 속도"},
	{atkPerformanceMode, "성능 모드"},
	{0x00110018, "팬 부스트 모드"},
	{0x00110019, "성능 모드 (Vivobook 방식)"},
	{atkCPUCurve, "CPU 팬 곡선"},
	{atkGPUCurve, "GPU 팬 곡선"},
	{0x00110032, "중간 팬 곡선"},
}

// diagnosisLines reads the firmware with gets alone.
func (a *asusControl) diagnosisLines() []string {
	return asusDiagnosisLines(func(device uint32) (uint32, bool) { return a.callValue(atkGet, device, 0) })
}

// asusDiagnosisLines gets each device once, with get, which must only read, and
// tells what it returned. Dashes tell that the driver did not answer, as for a
// device the firmware does not know.
func asusDiagnosisLines(get func(device uint32) (uint32, bool)) []string {
	lines := make([]string, len(asusDiagDevices))
	for i, d := range asusDiagDevices {
		raw, ok := get(d.id)
		value, meaning := "--------", "응답 없음"
		if ok {
			value, meaning = fmt.Sprintf("%08X", raw), asusMeaning(d.id, raw)
		}
		lines[i] = fmt.Sprintf("0x%08X  %s  %s · %s", d.id, value, d.name, meaning)
	}
	return lines
}

// asusMeaning tells what a get returned, for a person.
func asusMeaning(device, raw uint32) string {
	if !atkHasValue(raw) {
		return "없음"
	}
	if device == atkCPUFan || device == atkGPUFan || device == atkMidFan {
		if v, ok := fanRPM(raw); ok {
			return fmt.Sprintf("있음 (%d rpm)", v)
		}
	}
	return "있음"
}
