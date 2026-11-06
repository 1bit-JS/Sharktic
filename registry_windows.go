package main

import (
	"encoding/binary"
	"syscall"
	"unsafe"
)

var procRegDeleteValue = syscall.NewLazyDLL("advapi32.dll").NewProc("RegDeleteValueW")

// regValue reads a value of a key, and tells whether it is there.
func regValue(root syscall.Handle, key, name string) ([]byte, bool) {
	k, err := openKey(root, key, syscall.KEY_QUERY_VALUE)
	if err != nil {
		return nil, false
	}
	defer syscall.RegCloseKey(k)
	n, _ := syscall.UTF16PtrFromString(name)
	var size uint32
	if syscall.RegQueryValueEx(k, n, nil, nil, nil, &size) != nil {
		return nil, false
	}
	data := make([]byte, size+1) // never empty, so that its address can be taken
	if syscall.RegQueryValueEx(k, n, nil, nil, &data[0], &size) != nil {
		return nil, false
	}
	return data[:size], true
}

// regString reads a text value of a key, or "" if it is not there.
func regString(root syscall.Handle, key, name string) string {
	data, ok := regValue(root, key, name)
	if !ok || len(data) < 2 {
		return ""
	}
	return syscall.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(&data[0])), len(data)/2))
}

// regNumber reads a number value of a key, and tells whether it is there.
func regNumber(root syscall.Handle, key, name string) (uint32, bool) {
	data, ok := regValue(root, key, name)
	if !ok || len(data) != 4 {
		return 0, false
	}
	return binary.LittleEndian.Uint32(data), true
}

// deleteRegValue deletes a value of a key of the user, if it is there.
func deleteRegValue(key, name string) {
	k, err := openKey(syscall.HKEY_CURRENT_USER, key, syscall.KEY_SET_VALUE)
	if err != nil {
		return
	}
	defer syscall.RegCloseKey(k)
	n, _ := syscall.UTF16PtrFromString(name)
	procRegDeleteValue.Call(uintptr(k), uintptr(unsafe.Pointer(n)))
}

func openKey(root syscall.Handle, key string, access uint32) (syscall.Handle, error) {
	p, _ := syscall.UTF16PtrFromString(key)
	var k syscall.Handle
	err := syscall.RegOpenKeyEx(root, p, 0, access, &k)
	return k, err
}
