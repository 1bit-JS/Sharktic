package main

import (
	"math"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")

	pdh                = systemDLL("pdh.dll")
	procPdhOpenQuery   = pdh.NewProc("PdhOpenQueryW")
	procPdhAddCounter  = pdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectData = pdh.NewProc("PdhCollectQueryData")
	procPdhValue       = pdh.NewProc("PdhGetFormattedCounterValue")
	procPdhArray       = pdh.NewProc("PdhGetFormattedCounterArrayW")
)

// systemDLL loads a DLL from System32 only, never from the app's folder.
func systemDLL(name string) *syscall.LazyDLL {
	var buf [syscall.MAX_PATH]uint16
	n, _, _ := kernel32.NewProc("GetSystemDirectoryW").Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.NewLazyDLL(filepath.Join(syscall.UTF16ToString(buf[:n]), name))
}

// readMemory returns the physical memory in use and in total, in bytes.
func readMemory() (used, total uint64, ok bool) {
	var m struct {
		length, memoryLoad           uint32
		totalPhys, availPhys         uint64
		totalPageFile, availPageFile uint64
		totalVirtual, availVirtual   uint64
		availExtendedVirtual         uint64
	}
	m.length = uint32(unsafe.Sizeof(m))
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return 0, 0, false
	}
	return m.totalPhys - m.availPhys, m.totalPhys, true
}

const (
	pdhFmtDouble       = 0x00000200
	pdhFmtNoScale      = 0x00001000
	pdhMoreData        = 0x800007D2
	pdhCStatusValid    = 0
	pdhCStatusNewData  = 1
	pdhFormattedDouble = pdhFmtDouble | pdhFmtNoScale
)

// counterItem is one value of a counter, of an instance if it has several.
type counterItem struct {
	name   string
	status uint32
	value  float64
}

// valid tells whether the value can be used: a call that succeeds can still
// return a counter without data.
func (it counterItem) valid() bool {
	return (it.status == pdhCStatusValid || it.status == pdhCStatusNewData) &&
		it.value >= 0 && !math.IsInf(it.value, 0) // false for NaN too
}

// pdhQuery is an open query of performance counters.
type pdhQuery uintptr

func openQuery() (pdhQuery, bool) {
	if pdh.Load() != nil {
		return 0, false
	}
	var q pdhQuery
	r, _, _ := procPdhOpenQuery.Call(0, 0, uintptr(unsafe.Pointer(&q)))
	return q, r == 0
}

// add adds a counter by its English name, as counter names are localized. It
// returns 0 if this Windows does not have the counter.
func (q pdhQuery) add(path string) uintptr {
	p, _ := syscall.UTF16PtrFromString(path)
	var counter uintptr
	if r, _, _ := procPdhAddCounter.Call(uintptr(q), uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&counter))); r != 0 {
		return 0
	}
	return counter
}

func (q pdhQuery) collect() bool {
	r, _, _ := procPdhCollectData.Call(uintptr(q))
	return r == 0
}

// counterValue returns the value of a counter without instances.
func counterValue(counter uintptr) counterItem {
	var v struct {
		status uint32
		_      uint32
		value  float64
	}
	if counter == 0 {
		return counterItem{status: math.MaxUint32}
	}
	if r, _, _ := procPdhValue.Call(counter, pdhFormattedDouble, 0, uintptr(unsafe.Pointer(&v))); r != 0 {
		return counterItem{status: math.MaxUint32}
	}
	return counterItem{status: v.status, value: v.value}
}

// counterArray returns the values of the instances of a counter.
func counterArray(counter uintptr) []counterItem {
	if counter == 0 {
		return nil
	}
	var size, count uint32
	r, _, _ := procPdhArray.Call(counter, pdhFormattedDouble, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), 0)
	if uint32(r) != pdhMoreData || size == 0 {
		return nil
	}
	buf := make([]byte, size)
	r, _, _ = procPdhArray.Call(counter, pdhFormattedDouble, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&buf[0])))
	if r != 0 {
		return nil
	}
	type item struct {
		name   *uint16
		status uint32
		_      uint32
		value  float64
	}
	items := make([]counterItem, 0, count)
	for _, it := range unsafe.Slice((*item)(unsafe.Pointer(&buf[0])), count) {
		items = append(items, counterItem{windowsString(it.name), it.status, it.value})
	}
	return items
}

// windowsString returns the text of a NUL-terminated string.
func windowsString(p *uint16) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*uint16)(unsafe.Add(unsafe.Pointer(p), n*2)) != 0 {
		n++
	}
	return syscall.UTF16ToString(unsafe.Slice(p, n))
}

// hottestZone returns the highest of the thermal zones, in degrees Celsius.
func hottestZone(items []counterItem) (float64, bool) {
	best, ok := 0.0, false
	for _, it := range items {
		// The counter is in tenths of a kelvin. Zones without a sensor report 0.
		if c := it.value/10 - 273.15; it.valid() && it.value > 0 && (!ok || c > best) {
			best, ok = c, true
		}
	}
	return best, ok
}

// busiestEngine returns the use of the busiest engine of all adapters, which
// is what Task Manager shows of an adapter. An engine has an instance for each
// process that uses it, named as
// pid_1_luid_0x0_0x1_phys_0_eng_0_engtype_3D.
func busiestEngine(items []counterItem) (float64, bool) {
	engines := make(map[string]float64)
	for _, it := range items {
		_, engine, found := strings.Cut(it.name, "_luid_")
		if found && it.valid() {
			engines[engine] += it.value
		}
	}
	best, ok := 0.0, false
	for _, use := range engines {
		best, ok = max(best, use), true
	}
	return min(best, 100), ok
}

// largestValue returns the highest of the values.
func largestValue(items []counterItem) (float64, bool) {
	best, ok := 0.0, false
	for _, it := range items {
		if it.valid() {
			best, ok = max(best, it.value), true
		}
	}
	return best, ok
}

// counterSensor reads the performance counters of CPU use and of the ACPI
// thermal zones. Unlike the MSAcpi_ThermalZoneTemperature WMI class, they need
// no administrator rights.
type counterSensor struct {
	query        pdhQuery
	cpu, thermal uintptr
}

func newCounterSensor() *counterSensor {
	q, ok := openQuery()
	if !ok {
		return nil
	}
	s := &counterSensor{
		query: q,
		// Task Manager's figure is the utility, which counts the clock speed
		// above the base frequency, not the % Processor Time that
		// GetSystemTimes gives.
		cpu:     q.add(`\Processor Information(_Total)\% Processor Utility`),
		thermal: q.add(`\Thermal Zone Information(*)\High Precision Temperature`),
	}
	s.collect() // the baseline of the first CPU measure
	return s
}

func (s *counterSensor) collect() bool {
	return s != nil && s.query.collect()
}

// cpuUsage returns the CPU use between the last two collects, in percent. It
// goes above 100 at high clock speeds, where Task Manager stops at 100 too.
func (s *counterSensor) cpuUsage() (float64, bool) {
	it := counterValue(s.cpu)
	if !it.valid() {
		return 0, false
	}
	return min(it.value, 100), true
}

// temperature returns the hottest thermal zone in degrees Celsius.
func (s *counterSensor) temperature() (float64, bool) {
	return hottestZone(counterArray(s.thermal))
}

// gpuCounters reads any GPU through the performance counters of Windows, for
// when there is no NVML. They tell neither the temperature nor the size of the
// memory, and of several GPUs they tell the busiest.
type gpuCounters struct {
	query          pdhQuery
	engines, bytes uintptr
}

func newGPUCounters() *gpuCounters {
	q, ok := openQuery()
	if !ok {
		return nil
	}
	s := &gpuCounters{
		query:   q,
		engines: q.add(`\GPU Engine(*)\Utilization Percentage`),
		bytes:   q.add(`\GPU Adapter Memory(*)\Dedicated Usage`),
	}
	if s.engines == 0 && s.bytes == 0 {
		return nil
	}
	return s
}

func (s *gpuCounters) read(out *Snapshot) {
	if s == nil || !s.query.collect() {
		return
	}
	out.GPUUsage, out.HasGPU = busiestEngine(counterArray(s.engines))
	if used, ok := largestValue(counterArray(s.bytes)); ok {
		out.VRAMUsed = uint64(used)
	}
}

// gpuSensor reads the first NVIDIA GPU through NVML, which comes with the
// driver. It is nil where there is no such GPU.
type gpuSensor struct {
	device                             uintptr
	temperature, use, memory, shutdown *syscall.LazyProc
}

func newGPUSensor() *gpuSensor {
	nvml := systemDLL("nvml.dll")
	if nvml.Load() != nil {
		return nil
	}
	s := &gpuSensor{
		temperature: nvml.NewProc("nvmlDeviceGetTemperature"),
		use:         nvml.NewProc("nvmlDeviceGetUtilizationRates"),
		memory:      nvml.NewProc("nvmlDeviceGetMemoryInfo"),
		shutdown:    nvml.NewProc("nvmlShutdown"),
	}
	initialize, handle := nvml.NewProc("nvmlInit_v2"), nvml.NewProc("nvmlDeviceGetHandleByIndex_v2")
	// Calling a function that a driver lacks would panic.
	for _, p := range []*syscall.LazyProc{s.temperature, s.use, s.memory, s.shutdown, initialize, handle} {
		if p.Find() != nil {
			return nil
		}
	}
	if r, _, _ := initialize.Call(); r != 0 {
		return nil
	}
	if r, _, _ := handle.Call(0, uintptr(unsafe.Pointer(&s.device))); r != 0 {
		s.close()
		return nil
	}
	return s
}

func (s *gpuSensor) close() {
	s.shutdown.Call()
}

// read returns false if the GPU gave no value at all, as when its driver was
// restarted.
func (s *gpuSensor) read(out *Snapshot) bool {
	var temp uint32 // of NVML_TEMPERATURE_GPU, which is 0
	if r, _, _ := s.temperature.Call(s.device, 0, uintptr(unsafe.Pointer(&temp))); r == 0 {
		out.GPUTemp, out.HasGPUTemp = float64(temp), true
	}
	var use struct{ gpu, memory uint32 }
	if r, _, _ := s.use.Call(s.device, uintptr(unsafe.Pointer(&use))); r == 0 {
		out.GPUUsage, out.HasGPU = float64(use.gpu), true
	}
	var mem struct{ total, free, used uint64 }
	if r, _, _ := s.memory.Call(s.device, uintptr(unsafe.Pointer(&mem))); r == 0 {
		out.VRAMUsed, out.VRAMTotal = mem.used, mem.total
	}
	return out.HasGPUTemp || out.HasGPU || out.VRAMTotal > 0
}

// gpuRetryEvery is how often to look for NVML while it is not there, as a
// driver can come back or be installed while the app runs.
const gpuRetryEvery = 30 * time.Second

// Sampler holds the sensors, which are used from one goroutine at a time.
type Sampler struct {
	counters    *counterSensor
	gpu         *gpuSensor
	gpuRetryAt  time.Time
	gpuCounters *gpuCounters
	fans        fanBackend
	processes   processSensor
}

func NewSampler() *Sampler {
	s := &Sampler{counters: newCounterSensor()}
	s.processes.read() // the baseline of the first CPU measure
	return s
}

// readGPU reads NVML, or the performance counters while NVML is not there.
func (s *Sampler) readGPU(out *Snapshot) {
	if now := time.Now(); s.gpu == nil && !now.Before(s.gpuRetryAt) {
		s.gpuRetryAt = now.Add(gpuRetryEvery)
		s.gpu = newGPUSensor()
	}
	if s.gpu != nil {
		if s.gpu.read(out) {
			return
		}
		s.gpu.close()
		s.gpu = nil
	}
	if s.gpuCounters == nil {
		s.gpuCounters = newGPUCounters()
	}
	s.gpuCounters.read(out)
}

func (s *Sampler) Read() Snapshot {
	var out Snapshot
	if s.counters.collect() {
		out.CPUUsage, out.HasCPU = s.counters.cpuUsage()
		out.Temp, out.HasTemp = s.counters.temperature()
	}
	out.MemUsed, out.MemTotal, _ = readMemory()
	s.readGPU(&out)
	if s.fans != nil {
		s.fans.read(&out)
	}
	list := s.processes.read()
	if out.HasCPU {
		scaleToUsage(list, out.CPUUsage) // with the hidden ones, part of the total
	}
	list = visiblePrograms(list)
	out.TopCPU = top(list, topCount, func(u ProcessUse) float64 { return u.CPU })
	out.TopMemory = top(list, topCount, func(u ProcessUse) uint64 { return u.Memory })
	s.processes.describe(out.TopCPU)
	s.processes.describe(out.TopMemory)
	return out
}
