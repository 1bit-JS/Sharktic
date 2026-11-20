package main

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

const (
	ioctlStorageQueryProperty = 0x2D1400

	storageDeviceProperty            = 0
	storageDeviceSeekPenaltyProperty = 7
	storageDeviceTemperatureProperty = 52

	busTypeSATA = 11
	busTypeNVMe = 17

	// maxDrives is how many \\.\PhysicalDriveN are tried.
	maxDrives = 16
	// diskReadEvery is how often the drives are asked: a drive's temperature
	// changes slowly, and each question can keep an SSD from its deepest
	// power saving.
	diskReadEvery = 10 * time.Second
	// diskScanEvery is how often the drives are looked up again, for one
	// added or one that stopped answering.
	diskScanEvery = 5 * time.Minute
)

// diskSensor reads the temperature of the internal SSDs, which every PC has
// and which needs no administrator rights. Hard disks are left out, as asking
// them could spin them up, and so are USB drives.
type diskSensor struct {
	drives         []syscall.Handle
	readAt, scanAt time.Time
	temp           float64
	ok             bool
}

// queryProperty asks a drive for a STORAGE_PROPERTY_QUERY of the standard
// kind, and returns the bytes of the answer.
func queryProperty(h syscall.Handle, property uint32, out []byte) ([]byte, bool) {
	query := [3]uint32{property, 0, 0} // PropertyId, QueryType, AdditionalParameters
	var n uint32
	err := syscall.DeviceIoControl(h, ioctlStorageQueryProperty, (*byte)(unsafe.Pointer(&query)), uint32(unsafe.Sizeof(query)),
		&out[0], uint32(len(out)), &n, nil)
	return out[:n], err == nil
}

// driveTemperature returns the temperature a drive tells, in degrees Celsius:
// that of its first sensor, which NVMe makes the composite of all.
func driveTemperature(h syscall.Handle) (float64, bool) {
	var buf [24 + 16*8]byte
	b, ok := queryProperty(h, storageDeviceTemperatureProperty, buf[:])
	return parseDriveTemperature(b, ok)
}

// parseDriveTemperature reads a STORAGE_TEMPERATURE_DATA_DESCRIPTOR.
func parseDriveTemperature(b []byte, ok bool) (float64, bool) {
	// The header is 24 bytes; the first STORAGE_TEMPERATURE_INFO follows, with
	// the temperature as a signed 16-bit value after its index.
	if !ok || len(b) < 24+16 || uint16(b[12])|uint16(b[13])<<8 == 0 {
		return 0, false
	}
	c := int16(uint16(b[26]) | uint16(b[27])<<8)
	if c <= 0 || c > 150 { // 0 is what some drives tell for no value
		return 0, false
	}
	return float64(c), true
}

// wantedDrive tells whether a drive is an internal SSD that may be asked its
// temperature.
func wantedDrive(h syscall.Handle) bool {
	var buf [1024]byte
	if b, ok := queryProperty(h, storageDeviceProperty, buf[:]); !ok || !wantedBus(b) {
		return false
	}
	b, ok := queryProperty(h, storageDeviceSeekPenaltyProperty, buf[:])
	return ok && len(b) >= 9 && b[8] == 0 // IncursSeekPenalty
}

// wantedBus reads a STORAGE_DEVICE_DESCRIPTOR and tells whether the drive is
// fixed and on a bus whose driver answers the temperature question. Only the
// buses of the drivers Windows ships are taken: Intel RST, whose drives tell
// the RAID bus, freezes the whole machine when asked, and other vendors'
// drivers are not known to be safe.
func wantedBus(b []byte) bool {
	if len(b) < 32 || b[10] != 0 { // RemovableMedia
		return false
	}
	bus := uint32(b[28]) | uint32(b[29])<<8 | uint32(b[30])<<16 | uint32(b[31])<<24
	return bus == busTypeNVMe || bus == busTypeSATA
}

func hasTemperature(h syscall.Handle) bool {
	_, ok := driveTemperature(h)
	return ok
}

func (d *diskSensor) scan() {
	d.close()
	for i := range maxDrives {
		name, _ := syscall.UTF16PtrFromString(fmt.Sprintf(`\\.\PhysicalDrive%d`, i))
		// No access rights are asked for, which needs no administrator.
		h, err := syscall.CreateFile(name, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
		if err != nil {
			continue
		}
		// The kind first: a hard disk is never asked its temperature.
		if wantedDrive(h) && hasTemperature(h) {
			d.drives = append(d.drives, h)
		} else {
			syscall.CloseHandle(h)
		}
	}
}

func (d *diskSensor) close() {
	for _, h := range d.drives {
		syscall.CloseHandle(h)
	}
	d.drives = nil
}

// read gives the hottest drive's temperature, asking the drives only every
// diskReadEvery.
func (d *diskSensor) read(out *Snapshot, now time.Time) {
	if !now.Before(d.readAt) {
		d.readAt = now.Add(diskReadEvery)
		if !now.Before(d.scanAt) {
			d.scanAt = now.Add(diskScanEvery)
			d.scan()
		}
		d.temp, d.ok = 0, false
		for _, h := range d.drives {
			if c, ok := driveTemperature(h); ok && (!d.ok || c > d.temp) {
				d.temp, d.ok = c, true
			}
		}
	}
	out.DiskTemp, out.HasDiskTemp = d.temp, d.ok
}
