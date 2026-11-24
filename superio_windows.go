package main

import (
	_ "embed"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Desktop boards have a Super I/O chip whose hardware monitor counts the fans
// on their headers. The service reads it through PawnIO's LpcIO module, which
// lets it reach only the chip's configuration ports and the ports the chip
// itself maps. It only reads the fans; it writes nothing but what is needed to
// reach them: the configuration mode keys, the logical device select and the
// monitor's bank select, which it puts back.
//
// The chip IDs, logical devices and registers below are facts read from Linux's
// nct6775 and it87 drivers and the out-of-tree it87 driver
// (github.com/frankcrawford/it87); no code is taken from them. None of it has
// been tried on a real board yet.

//go:embed pawnio/LpcIO.bin
var lpcIOModule []byte

const (
	sioChipID     = 0x20 // two bytes
	sioLogicalDev = 0x07
	sioActivate   = 0x30
	sioBaseAddr   = 0x60 // two bytes
	// sioMonitorOffset is where a chip's monitor has its address and data
	// ports, from its base address.
	sioMonitorOffset = 5

	nuvotonMonitorDevice = 0x0B
	nuvotonBankSelect    = 0x4E
	nuvotonFanBank       = 4

	iteMonitorDevice = 0x04
	iteFanMainCtrl   = 0x13 // bits 4-6: fans 1-3 on
	iteFan16Bit      = 0x0C // bits 0-2: fans 1-3 count 16 bits; bits 4, 5: fans 4, 5 on

	// isaBusMutex is the mutex that programs reaching the LPC bus share, so
	// that two never talk to the chip at once.
	isaBusMutex = `Global\Access_ISABUS.HTP.Method`
)

// nuvotonChips are the NCT6779D and later, which give each fan's speed in RPM,
// by their ID masked with 0xFFF8.
var nuvotonChips = map[uint16]string{
	0xC560: "NCT6779D", 0xC800: "NCT6791D", 0xC910: "NCT6792D", 0xD120: "NCT6793D",
	0xD350: "NCT6795D", 0xD420: "NCT6796D", 0xD450: "NCT6797D", 0xD428: "NCT6798D",
	0xD800: "NCT6799D",
}

// nuvotonFanRegs are the fan speed registers, two bytes from the high, of the
// fan bank.
var nuvotonFanRegs = []byte{0xC0, 0xC2, 0xC4, 0xC6, 0xC8, 0xCA, 0xCE}

// iteChips are the ITE chips whose fans count in 16 bits without a setting
// changed first, by ID, with how many fans to read: fans 1 to 5 at most, as
// the sixth's enable bit means another thing on other chips.
var iteChips = map[uint16]struct {
	name string
	fans int
}{
	0x8603: {"IT8603E", 3}, 0x8606: {"IT8606E", 3}, 0x8607: {"IT8607E", 3},
	0x8613: {"IT8613E", 5}, 0x8620: {"IT8620E", 5}, 0x8622: {"IT8622E", 5},
	0x8625: {"IT8625E", 5}, 0x8628: {"IT8628E", 5}, 0x8655: {"IT8655E", 3},
	0x8665: {"IT8665E", 5}, 0x8686: {"IT8686E", 5}, 0x8688: {"IT8688E", 5},
	0x8689: {"IT8689E", 5}, 0x8695: {"IT87952E", 3}, 0x8696: {"IT8696E", 5},
	0x8698: {"IT8698E", 5}, 0x8728: {"IT8728F", 5}, 0x8732: {"IT8732F", 4},
	0x8733: {"IT8792E", 3}, 0x8736: {"IT8736F", 4}, 0x8738: {"IT8738E", 3},
	0x8771: {"IT8771E", 3}, 0x8772: {"IT8772E", 3}, 0x8785: {"IT8785E", 3},
	0x8786: {"IT8786E", 3}, 0x8790: {"IT8790E", 3},
}

var (
	iteFanRegs     = []byte{0x0D, 0x0E, 0x0F, 0x80, 0x82}
	iteFanHighRegs = []byte{0x18, 0x19, 0x1A, 0x81, 0x83}
)

// lpcPorts is what the Super I/O code needs of LpcIO, so that tests can stand
// in for a chip.
type lpcPorts interface {
	selectSlot(slot int) error
	findBars() error
	sioIn(reg byte) (byte, error)
	sioOut(reg, v byte) error
	in(port uint16) (byte, error)
	out(port uint16, v byte) error
}

func sioInWord(p lpcPorts, reg byte) (uint16, error) {
	hi, err := p.sioIn(reg)
	if err != nil {
		return 0, err
	}
	lo, err := p.sioIn(reg + 1)
	return uint16(hi)<<8 | uint16(lo), err
}

// sioSlotPorts are the configuration ports of slots 0 and 1.
var sioSlotPorts = [2]uint16{0x2E, 0x4E}

type superIOKind int

const (
	superIONuvoton superIOKind = iota + 1
	superIOITE
)

// superIOChip is a chip found, with the fans it may have.
type superIOChip struct {
	kind superIOKind
	name string
	addr uint16 // the monitor's address port; data is the next
	fans []int  // indexes into the kind's fan registers
}

// probeSuperIO looks for a known chip in each slot. It returns the chip, or
// what it saw, for a diagnosis.
func probeSuperIO(p lpcPorts) (*superIOChip, string, error) {
	var seen []string
	for slot, port := range sioSlotPorts {
		if err := p.selectSlot(slot); err != nil {
			return nil, "", err
		}
		chip, id, err := probeNuvoton(p, port)
		if err != nil || chip != nil {
			return chip, "", err
		}
		if id != 0xFFFF && id != 0 {
			seen = append(seen, fmt.Sprintf("0x%04X@%X", id, port))
		}
		chip, id, err = probeITE(p, port)
		if err != nil || chip != nil {
			return chip, "", err
		}
		if id != 0xFFFF && id != 0 {
			seen = append(seen, fmt.Sprintf("0x%04X@%X", id, port))
		}
	}
	if len(seen) == 0 {
		return nil, "no chip", nil
	}
	return nil, fmt.Sprintf("unsupported chip %v", seen), nil
}

func probeNuvoton(p lpcPorts, port uint16) (*superIOChip, uint16, error) {
	for _, key := range []byte{0x87, 0x87} {
		if err := p.out(port, key); err != nil {
			return nil, 0, err
		}
	}
	defer p.out(port, 0xAA) // leaves the configuration mode
	id, err := sioInWord(p, sioChipID)
	if err != nil {
		return nil, 0, err
	}
	name, ok := nuvotonChips[id&0xFFF8]
	if !ok {
		return nil, id, nil
	}
	addr, err := monitorAddress(p, nuvotonMonitorDevice)
	if err != nil || addr == 0 {
		return nil, id, err
	}
	chip := &superIOChip{kind: superIONuvoton, name: "Nuvoton " + name, addr: addr}
	for i := range nuvotonFanRegs {
		chip.fans = append(chip.fans, i)
	}
	return chip, id, nil
}

func probeITE(p lpcPorts, port uint16) (*superIOChip, uint16, error) {
	last := byte(0x55)
	if port == 0x4E {
		last = 0xAA
	}
	for _, key := range []byte{0x87, 0x01, 0x55, last} {
		if err := p.out(port, key); err != nil {
			return nil, 0, err
		}
	}
	defer p.sioOut(0x02, 0x02) // leaves the configuration mode
	id, err := sioInWord(p, sioChipID)
	if err != nil {
		return nil, 0, err
	}
	c, ok := iteChips[id]
	if !ok {
		return nil, id, nil
	}
	addr, err := monitorAddress(p, iteMonitorDevice)
	if err != nil || addr == 0 {
		return nil, id, err
	}
	chip := &superIOChip{kind: superIOITE, name: "ITE " + c.name, addr: addr}
	main, err := monitorRead(p, chip.addr, iteFanMainCtrl)
	if err != nil {
		return nil, id, err
	}
	bits, err := monitorRead(p, chip.addr, iteFan16Bit)
	if err != nil {
		return nil, id, err
	}
	chip.fans = iteFansOn(main, bits, c.fans)
	return chip, id, nil
}

// iteFansOn reads which fans are on, of the first n. Fans 1-3 are taken only
// where they count in 16 bits, which is not changed here.
func iteFansOn(mainCtrl, fan16 byte, n int) []int {
	var fans []int
	for i := range min(n, 3) {
		if mainCtrl&(0x10<<i) != 0 && fan16&(1<<i) != 0 {
			fans = append(fans, i)
		}
	}
	if n >= 4 && fan16&0x10 != 0 {
		fans = append(fans, 3)
	}
	if n >= 5 && fan16&0x20 != 0 {
		fans = append(fans, 4)
	}
	return fans
}

// monitorAddress finds the bars LpcIO may reach, then the monitor's base
// address, while in the configuration mode. It returns 0 for a monitor that
// is off: it is not turned on here.
func monitorAddress(p lpcPorts, device byte) (uint16, error) {
	if err := p.findBars(); err != nil {
		return 0, err
	}
	if err := p.sioOut(sioLogicalDev, device); err != nil {
		return 0, err
	}
	on, err := p.sioIn(sioActivate)
	if err != nil || on&1 == 0 {
		return 0, err
	}
	base, err := sioInWord(p, sioBaseAddr)
	if err != nil {
		return 0, err
	}
	base &^= 7
	if base < 0x100 {
		return 0, nil
	}
	return base + sioMonitorOffset, nil
}

func monitorRead(p lpcPorts, addr uint16, reg byte) (byte, error) {
	if err := p.out(addr, reg); err != nil {
		return 0, err
	}
	return p.in(addr + 1)
}

func monitorWrite(p lpcPorts, addr uint16, reg, v byte) error {
	if err := p.out(addr, reg); err != nil {
		return err
	}
	return p.out(addr+1, v)
}

// readFans returns the speed of each fan the chip may have, in RPM, 0 for one
// stopped or not there.
func (c *superIOChip) readFans(p lpcPorts) ([]int, error) {
	rpms := make([]int, len(c.fans))
	switch c.kind {
	case superIONuvoton:
		bank, err := monitorRead(p, c.addr, nuvotonBankSelect)
		if err != nil {
			return nil, err
		}
		if err := monitorWrite(p, c.addr, nuvotonBankSelect, nuvotonFanBank); err != nil {
			return nil, err
		}
		defer monitorWrite(p, c.addr, nuvotonBankSelect, bank)
		for i, f := range c.fans {
			hi, err := monitorRead(p, c.addr, nuvotonFanRegs[f])
			if err != nil {
				return nil, err
			}
			lo, err := monitorRead(p, c.addr, nuvotonFanRegs[f]+1)
			if err != nil {
				return nil, err
			}
			rpms[i] = saneRPM(int(hi)<<8 | int(lo))
		}
	case superIOITE:
		for i, f := range c.fans {
			lo, err := monitorRead(p, c.addr, iteFanRegs[f])
			if err != nil {
				return nil, err
			}
			hi, err := monitorRead(p, c.addr, iteFanHighRegs[f])
			if err != nil {
				return nil, err
			}
			rpms[i] = saneRPM(iteRPM(uint16(hi)<<8 | uint16(lo)))
		}
	}
	return rpms, nil
}

// iteRPM reads a 16-bit count, which is 0xFFFF for a stopped fan.
func iteRPM(count uint16) int {
	if count == 0 || count == 0xFFFF {
		return 0
	}
	return 1350000 / (int(count) * 2)
}

// saneRPM drops what no fan turns at, as a pin left floating may count.
func saneRPM(v int) int {
	if v < 0 || v > 20000 {
		return 0
	}
	return v
}

// superIO is the service's open chip.
type superIO struct {
	ports *pawnIOPorts
	chip  *superIOChip
	// seen are the fans that have turned since the service started, which are
	// shown from then on: a header without a fan reads 0 as a stopped one does.
	seen []bool
}

func openSuperIO() (*superIO, string, error) {
	if !systemHasNoBattery() {
		// A laptop's embedded controller may sit at the same ports, and is
		// left alone.
		return nil, "not probed on laptops", nil
	}
	ports, err := openPawnIOPorts()
	if err != nil {
		return nil, "", err
	}
	var chip *superIOChip
	var note string
	err = ports.locked(func() error {
		chip, note, err = probeSuperIO(ports)
		return err
	})
	if err != nil || chip == nil {
		ports.close()
		return nil, note, err
	}
	return &superIO{ports: ports, chip: chip, seen: make([]bool, len(chip.fans))}, chip.name, nil
}

// fans returns the fans that have turned, named by their header's number.
func (s *superIO) fans() ([]FanSpeed, error) {
	var rpms []int
	err := s.ports.locked(func() error {
		var err error
		rpms, err = s.chip.readFans(s.ports)
		return err
	})
	if err != nil {
		return nil, err
	}
	var out []FanSpeed
	for i, v := range rpms {
		s.seen[i] = s.seen[i] || v > 0
		if s.seen[i] {
			out = append(out, FanSpeed{Name: fmt.Sprintf("Fan %d", s.chip.fans[i]+1), RPM: v})
		}
	}
	return out, nil
}

func (s *superIO) close() { s.ports.close() }

// pawnIOPorts is LpcIO, with the ISA bus mutex.
type pawnIOPorts struct {
	module *pawnIOModule
	mu     windows.Handle
}

func openPawnIOPorts() (*pawnIOPorts, error) {
	m, err := openPawnIOModule(lpcIOModule)
	if err != nil {
		return nil, err
	}
	mu, err := openGlobalMutex(isaBusMutex)
	if err != nil {
		m.close()
		return nil, err
	}
	return &pawnIOPorts{module: m, mu: mu}, nil
}

// locked runs fn while holding the ISA bus mutex.
func (p *pawnIOPorts) locked(fn func() error) error {
	if err := withGlobalMutex(p.mu, 500*time.Millisecond, fn); err != nil {
		return fmt.Errorf("ISA bus: %w", err)
	}
	return nil
}

func (p *pawnIOPorts) call(name string, in ...uint64) (uint64, error) {
	out, err := p.module.call(name, in, 1)
	if err != nil || len(out) == 0 {
		return 0, err
	}
	return out[0], nil
}

func (p *pawnIOPorts) selectSlot(slot int) error {
	_, err := p.module.call("ioctl_select_slot", []uint64{uint64(slot)}, 0)
	return err
}

func (p *pawnIOPorts) findBars() error {
	_, err := p.module.call("ioctl_find_bars", nil, 0)
	return err
}

func (p *pawnIOPorts) sioIn(reg byte) (byte, error) {
	v, err := p.call("ioctl_superio_inb", uint64(reg))
	return byte(v), err
}

func (p *pawnIOPorts) sioOut(reg, v byte) error {
	_, err := p.module.call("ioctl_superio_outb", []uint64{uint64(reg), uint64(v)}, 0)
	return err
}

func (p *pawnIOPorts) in(port uint16) (byte, error) {
	v, err := p.call("ioctl_pio_inb", uint64(port))
	return byte(v), err
}

func (p *pawnIOPorts) out(port uint16, v byte) error {
	_, err := p.module.call("ioctl_pio_outb", []uint64{uint64(port), uint64(v)}, 0)
	return err
}

func (p *pawnIOPorts) close() {
	p.module.close()
	windows.CloseHandle(p.mu)
}

// systemHasNoBattery tells a desktop: Windows reports no system battery.
func systemHasNoBattery() bool {
	var st struct {
		acLineStatus, batteryFlag, batteryLifePercent, systemStatusFlag byte
		batteryLifeTime, batteryFullLifeTime                            uint32
	}
	r, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&st)))
	return r != 0 && st.batteryFlag != 255 && st.batteryFlag&128 != 0
}

var procGetSystemPowerStatus = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemPowerStatus")
