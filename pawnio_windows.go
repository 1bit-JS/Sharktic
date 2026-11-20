package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// PawnIO is the signed driver that runs small, signed modules for the hardware
// that Windows gives no way to: CPU registers, Super I/O chips and embedded
// controllers. It needs administrator rights, so only the service uses it.
// See https://pawnio.eu.

const pawnIOUninstallKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\PawnIO`

// pawnIOMinVersion is the oldest version taken: earlier ones crash some
// Windows 10 builds.
var pawnIOMinVersion = [3]int{2, 2, 0}

// pawnIOInstalled returns the installed version of PawnIO and its folder.
func pawnIOInstalled() (version [3]int, dir string, ok bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, pawnIOUninstallKey, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return version, "", false
	}
	defer k.Close()
	v, _, err := k.GetStringValue("DisplayVersion")
	if err != nil {
		return version, "", false
	}
	version, ok = parseVersion(v)
	dir, _, _ = k.GetStringValue("InstallLocation")
	if dir == "" {
		pf, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
		if err != nil {
			return version, "", false
		}
		dir = filepath.Join(pf, "PawnIO")
	}
	return version, dir, ok
}

// parseVersion reads "2.2.0.0" and the like, of which it takes three parts.
func parseVersion(s string) (v [3]int, ok bool) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) < 3 {
		return v, false
	}
	for i := range v {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func versionAtLeast(v, min [3]int) bool {
	for i := range v {
		if v[i] != min[i] {
			return v[i] > min[i]
		}
	}
	return true
}

// pawnIOReady tells whether a version of PawnIO that the service takes is
// installed.
func pawnIOReady() bool {
	v, _, ok := pawnIOInstalled()
	return ok && versionAtLeast(v, pawnIOMinVersion)
}

// pawnIOModule is one module loaded into its own PawnIO executor.
type pawnIOModule struct {
	handle  windows.Handle
	execute *windows.Proc
	close_  *windows.Proc
}

var pawnIOLib struct {
	dll *windows.DLL
	err error
}

func loadPawnIOLib() (*windows.DLL, error) {
	if pawnIOLib.dll != nil || pawnIOLib.err != nil {
		return pawnIOLib.dll, pawnIOLib.err
	}
	v, dir, ok := pawnIOInstalled()
	switch {
	case !ok:
		pawnIOLib.err = errors.New("PawnIO is not installed")
	case !versionAtLeast(v, pawnIOMinVersion):
		pawnIOLib.err = fmt.Errorf("PawnIO %d.%d.%d is older than %d.%d.%d", v[0], v[1], v[2],
			pawnIOMinVersion[0], pawnIOMinVersion[1], pawnIOMinVersion[2])
	default:
		// By its full path, from a folder only administrators can change.
		pawnIOLib.dll, pawnIOLib.err = windows.LoadDLL(filepath.Join(dir, "PawnIOLib.dll"))
	}
	return pawnIOLib.dll, pawnIOLib.err
}

// openPawnIOModule opens an executor and loads a module into it.
func openPawnIOModule(blob []byte) (*pawnIOModule, error) {
	dll, err := loadPawnIOLib()
	if err != nil {
		return nil, err
	}
	procs := map[string]*windows.Proc{}
	for _, name := range []string{"pawnio_open", "pawnio_load", "pawnio_execute", "pawnio_close"} {
		p, err := dll.FindProc(name)
		if err != nil {
			return nil, err
		}
		procs[name] = p
	}
	m := &pawnIOModule{execute: procs["pawnio_execute"], close_: procs["pawnio_close"]}
	if hr, _, _ := procs["pawnio_open"].Call(uintptr(unsafe.Pointer(&m.handle))); hr != 0 {
		return nil, fmt.Errorf("pawnio_open: %#08x", uint32(hr))
	}
	if hr, _, _ := procs["pawnio_load"].Call(uintptr(m.handle), uintptr(unsafe.Pointer(&blob[0])), uintptr(len(blob))); hr != 0 {
		m.close()
		return nil, fmt.Errorf("pawnio_load: %#08x", uint32(hr))
	}
	return m, nil
}

// call runs a function of the module, and returns what it wrote of out.
func (m *pawnIOModule) call(name string, in []uint64, outLen int) ([]uint64, error) {
	cname, err := windows.BytePtrFromString(name)
	if err != nil {
		return nil, err
	}
	out := make([]uint64, max(outLen, 1))
	var inPtr uintptr
	if len(in) > 0 {
		inPtr = uintptr(unsafe.Pointer(&in[0]))
	}
	var n uintptr
	hr, _, _ := m.execute.Call(uintptr(m.handle), uintptr(unsafe.Pointer(cname)), inPtr, uintptr(len(in)),
		uintptr(unsafe.Pointer(&out[0])), uintptr(outLen), uintptr(unsafe.Pointer(&n)))
	if hr != 0 {
		return nil, fmt.Errorf("%s: %#08x", name, uint32(hr))
	}
	return out[:min(int(n), outLen)], nil
}

func (m *pawnIOModule) close() {
	if m.handle != 0 {
		m.close_.Call(uintptr(m.handle))
		m.handle = 0
	}
}
