package main

import (
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	procNtQuerySystemInformation = syscall.NewLazyDLL("ntdll.dll").NewProc("NtQuerySystemInformation")

	versionDLL                 = systemDLL("version.dll")
	procGetFileVersionInfoSize = versionDLL.NewProc("GetFileVersionInfoSizeW")
	procGetFileVersionInfo     = versionDLL.NewProc("GetFileVersionInfoW")
	procVerQueryValue          = versionDLL.NewProc("VerQueryValueW")
)

const (
	systemProcessInformation   = 5
	systemProcessIDInformation = 88
	statusInfoLengthMismatch   = 0xC0000004
)

// processInfo is the start of SYSTEM_PROCESS_INFORMATION on 64-bit Windows.
type processInfo struct {
	nextEntryOffset, numberOfThreads uint32
	workingSetPrivate                int64
	hardFaultCount, threadsHigh      uint32
	cycleTime                        uint64
	createTime, userTime, kernelTime int64 // in 100 ns
	imageNameLength, imageNameMax    uint16
	_                                uint32
	imageName                        *uint16
	basePriority                     int32
	_                                uint32
	processID                        uintptr
}

type processKey struct {
	id      uintptr
	created int64 // process IDs are reused
}

// processSensor lists the processes through NtQuerySystemInformation, which,
// unlike opening each process, also reaches those of other users without
// administrator rights.
type processSensor struct {
	buf          []byte
	last         map[processKey]int64
	at           time.Time
	descriptions map[string]string // by process name
}

// read returns the use of the running programs since the last call, with the
// processes of a program added up, as a browser has many. Service hosts stay
// apart: they run unrelated services, and Task Manager lists them one by one.
func (s *processSensor) read() []ProcessUse {
	if s.buf == nil {
		s.buf = make([]byte, 1<<20)
	}
	for {
		r, _, _ := procNtQuerySystemInformation.Call(systemProcessInformation, uintptr(unsafe.Pointer(&s.buf[0])), uintptr(len(s.buf)), 0)
		if uint32(r) != statusInfoLengthMismatch {
			if r != 0 {
				return nil
			}
			break
		}
		s.buf = make([]byte, 2*len(s.buf))
	}
	now := time.Now()
	// The share of all CPUs that a time of 100 ns is, in percent.
	share := 0.0
	if !s.at.IsZero() {
		share = 100 * 100e-9 / (now.Sub(s.at).Seconds() * float64(runtime.NumCPU()))
	}
	s.at = now

	seen := make(map[processKey]int64, len(s.last))
	groups := make(map[string]*ProcessUse)
	for offset := uint32(0); ; {
		p := (*processInfo)(unsafe.Pointer(&s.buf[offset]))
		// The first entry, without a name, is the idle time.
		if p.imageName != nil {
			name := strings.TrimSuffix(syscall.UTF16ToString(unsafe.Slice(p.imageName, p.imageNameLength/2)), ".exe")
			key := processKey{p.processID, p.createTime}
			used := p.userTime + p.kernelTime
			seen[key] = used
			group := name
			if name == "svchost" {
				group = fmt.Sprint(name, p.processID)
			}
			u := groups[group]
			if u == nil {
				u = &ProcessUse{Name: name, id: p.processID}
				groups[group] = u
			}
			u.Count++
			u.Memory += uint64(p.workingSetPrivate)
			// A process new since the last call counts from its start.
			u.CPU += float64(used-s.last[key]) * share
		}
		if p.nextEntryOffset == 0 {
			break
		}
		offset += p.nextEntryOffset
	}
	s.last = seen
	list := make([]ProcessUse, 0, len(groups))
	for _, u := range groups {
		list = append(list, *u)
	}
	return list
}

// describe gives programs the names Task Manager shows, as "Google Chrome" for
// chrome, which come from their files. A program without one keeps its name.
func (s *processSensor) describe(list []ProcessUse) {
	if s.descriptions == nil {
		s.descriptions = make(map[string]string)
	}
	for i := range list {
		u := &list[i]
		d, ok := s.descriptions[u.Name]
		if !ok {
			path := imagePath(u.id)
			if path == "" {
				continue // the next one of this name may have a path
			}
			d = fileDescription(path)
			s.descriptions[u.Name] = d
		}
		if d != "" {
			u.Name = d
		}
	}
}

// imagePath returns the path of a process's file in a form that file functions
// accept. Unlike opening the process, it works for those of other users.
func imagePath(id uintptr) string {
	buf := make([]uint16, 1024)
	info := struct {
		id          uintptr
		length, max uint16
		_           uint32
		buffer      *uint16
	}{id: id, max: uint16(len(buf) * 2), buffer: &buf[0]}
	r, _, _ := procNtQuerySystemInformation.Call(systemProcessIDInformation, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), 0)
	path := syscall.UTF16ToString(buf[:info.length/2])
	if r != 0 || !strings.HasPrefix(path, `\Device\`) {
		return ""
	}
	return `\\?\GLOBALROOT` + path
}

// fileDescription returns the description in a file's version information,
// or "" if it has none.
func fileDescription(path string) string {
	if path == "" {
		return ""
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return ""
	}
	size, _, _ := procGetFileVersionInfoSize.Call(uintptr(unsafe.Pointer(p)), 0)
	if size == 0 {
		return ""
	}
	data := make([]byte, size)
	if r, _, _ := procGetFileVersionInfo.Call(uintptr(unsafe.Pointer(p)), 0, size, uintptr(unsafe.Pointer(&data[0]))); r == 0 {
		return ""
	}
	var n uint32
	query := func(sub string, out unsafe.Pointer) bool {
		q, _ := syscall.UTF16PtrFromString(sub)
		r, _, _ := procVerQueryValue.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(q)), uintptr(out), uintptr(unsafe.Pointer(&n)))
		return r != 0 && n > 0
	}
	var translation *[2]uint16
	if !query(`\VarFileInfo\Translation`, unsafe.Pointer(&translation)) {
		return ""
	}
	var text *uint16
	sub := fmt.Sprintf(`\StringFileInfo\%04x%04x\FileDescription`, translation[0], translation[1])
	if !query(sub, unsafe.Pointer(&text)) {
		return ""
	}
	return strings.TrimSpace(syscall.UTF16ToString(unsafe.Slice(text, n)))
}
