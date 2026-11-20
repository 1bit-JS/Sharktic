package main

import (
	"syscall"
	"unsafe"
)

var (
	gdi32                  = systemDLL("gdi32.dll")
	procD3DKMTEnumAdapters = gdi32.NewProc("D3DKMTEnumAdapters2")
	procD3DKMTQueryAdapter = gdi32.NewProc("D3DKMTQueryAdapterInfo")
	procD3DKMTCloseAdapter = gdi32.NewProc("D3DKMTCloseAdapter")
)

// kmtqaiAdapterPerfData is KMTQAITYPE_ADAPTERPERFDATA, which Windows 10 1809
// and later answer for the drivers that give it.
const kmtqaiAdapterPerfData = 62

// adapterPerfData is D3DKMT_ADAPTER_PERFDATA.
type adapterPerfData struct {
	physicalAdapterIndex uint32
	memoryFrequency      uint64
	maxMemoryFrequency   uint64
	maxMemoryFrequencyOC uint64
	memoryBandwidth      uint64
	pcieBandwidth        uint64
	fanRPM               uint32
	power                uint32 // in tenths of a percent of the most
	temperature          uint32 // in tenths of a degree Celsius
	powerStateOverride   uint8
}

// kernelGPUs reads the temperature and the fan of every GPU through the
// graphics kernel, as Task Manager does. It needs no vendor library, so it
// covers the GPUs that NVML does not, such as AMD's and Intel's. A GPU whose
// driver does not tell them, as most integrated ones, reports 0.
type kernelGPUs struct {
	adapters []uint32 // D3DKMT_HANDLE
}

func newKernelGPUs() *kernelGPUs {
	for _, p := range []*syscall.LazyProc{procD3DKMTEnumAdapters, procD3DKMTQueryAdapter, procD3DKMTCloseAdapter} {
		if p.Find() != nil {
			return nil
		}
	}
	type adapterInfo struct { // D3DKMT_ADAPTERINFO, whose LUID is 4-byte aligned
		adapter uint32
		luid    [2]uint32
		sources uint32
		precise int32
	}
	var list [16]adapterInfo
	enum := struct {
		count    uint32
		adapters *adapterInfo
	}{uint32(len(list)), &list[0]}
	if r, _, _ := procD3DKMTEnumAdapters.Call(uintptr(unsafe.Pointer(&enum))); r != 0 {
		return nil
	}
	s := &kernelGPUs{}
	for _, a := range list[:min(enum.count, uint32(len(list)))] {
		s.adapters = append(s.adapters, a.adapter)
	}
	return s
}

func (s *kernelGPUs) close() {
	for _, h := range s.adapters {
		closeAdapter := struct{ adapter uint32 }{h}
		procD3DKMTCloseAdapter.Call(uintptr(unsafe.Pointer(&closeAdapter)))
	}
	s.adapters = nil
}

// read gives the hottest GPU's temperature and the fastest GPU fan, where they
// are not known yet: the fan is not taken from the hottest GPU alone, which may
// be an integrated one without a fan. It returns false if no GPU told a
// temperature.
func (s *kernelGPUs) read(out *Snapshot) bool {
	found := false
	var hottest adapterPerfData
	var fan uint32
	for _, h := range s.adapters {
		var p adapterPerfData
		q := struct { // D3DKMT_QUERYADAPTERINFO
			adapter, kind uint32
			data          unsafe.Pointer
			size          uint32
		}{h, kmtqaiAdapterPerfData, unsafe.Pointer(&p), uint32(unsafe.Sizeof(p))}
		if r, _, _ := procD3DKMTQueryAdapter.Call(uintptr(unsafe.Pointer(&q))); r != 0 || p.temperature == 0 {
			continue
		}
		fan = max(fan, p.fanRPM)
		if !found || p.temperature > hottest.temperature {
			hottest, found = p, true
		}
	}
	if !found {
		return false
	}
	if !out.HasGPUTemp {
		out.GPUTemp, out.HasGPUTemp = float64(hottest.temperature)/10, true
	}
	// 0 is also what a stopped fan reports, but more often a GPU without one.
	if !out.HasGPUFan && fan > 0 {
		out.GPUFan, out.HasGPUFan = int(fan), true
	}
	return true
}
