//go:build windows

// Command fan-probe gathers, on another laptop, what tells whether the app
// can control its fans or what support it would need. It only reads: it sets no
// firmware value, calls no WMI method, and needs no administrator rights. It
// writes a folder and a zip of it next to itself, for the person to send back.
package main

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

const version = "2"

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procDeviceIoControl    = kernel32.NewProc("DeviceIoControl")
	procEnumFirmwareTables = kernel32.NewProc("EnumSystemFirmwareTables")
	procGetFirmwareTable   = kernel32.NewProc("GetSystemFirmwareTable")
	procSetConsoleOutputCP = kernel32.NewProc("SetConsoleOutputCP")
	procIsUserAnAdmin      = syscall.NewLazyDLL("shell32.dll").NewProc("IsUserAnAdmin")
	hklm                   = syscall.Handle(syscall.HKEY_LOCAL_MACHINE)
)

func main() {
	procSetConsoleOutputCP.Call(65001)
	fmt.Println("팬 제어 기기 조사 도구 (버전 " + version + ")")
	fmt.Println("읽기만 하고, 팬이나 펌웨어 설정은 바꾸지 않습니다.")
	fmt.Println()

	r := gather()
	dir, err := save(r)
	if err != nil {
		fmt.Println("저장하지 못했습니다:", err)
		wait()
		return
	}
	fmt.Println()
	fmt.Println("판정:")
	for _, l := range r.verdict() {
		fmt.Println("  " + l)
	}
	fmt.Println()
	zipPath := dir + ".zip"
	if err := zipDir(dir, zipPath); err != nil {
		fmt.Println("압축하지 못했습니다:", err)
		fmt.Println("이 폴더를 그대로 보내 주세요:", dir)
	} else {
		fmt.Println("이 파일을 보내 주세요:")
		fmt.Println("  " + zipPath)
		if !slices.Contains(os.Args[1:], "-no-open") {
			exec.Command("explorer", "/select,"+zipPath).Start()
		}
	}
	wait()
}

func wait() {
	fmt.Println()
	fmt.Print("Enter 키를 누르면 닫힙니다.")
	bufio.NewReader(os.Stdin).ReadString('\n')
}

// report is what the probe found.
type report struct {
	Time                                time.Time
	Manufacturer, Model, Family, Board  string
	BIOS, BIOSDate, Windows, CPU        string
	Admin                               bool
	GPUs                                []string
	ASUS                                bool // the ASUS driver opened
	ASUSLines                           []string
	ASUSModes, ASUSCurves, ASUSVivobook bool
	Tables                              []acpiTable
	TablesError                         string
	WMI                                 wmiResult
	WMIError                            string
	Services, ACPIDevices               []string
}

type acpiTable struct {
	Sig   string
	Size  int
	Count int // how many the firmware lists, when only the first could be read
	Data  []byte
}

func gather() *report {
	r := &report{Time: time.Now()}
	step := func(s string) { fmt.Println("- " + s) }

	step("기기 정보")
	const bios = `HARDWARE\DESCRIPTION\System\BIOS`
	r.Manufacturer = regString(bios, "SystemManufacturer")
	r.Model = regString(bios, "SystemProductName")
	r.Family = regString(bios, "SystemFamily")
	r.Board = regString(bios, "BaseBoardProduct")
	r.BIOS = regString(bios, "BIOSVersion")
	r.BIOSDate = regString(bios, "BIOSReleaseDate")
	r.CPU = strings.TrimSpace(regString(`HARDWARE\DESCRIPTION\System\CentralProcessor\0`, "ProcessorNameString"))
	r.Windows = windowsVersion()
	ok, _, _ := procIsUserAnAdmin.Call()
	r.Admin = ok != 0

	step("ASUS 드라이버")
	probeASUS(r)

	step("ACPI 표")
	r.Tables, r.TablesError = readACPITables()

	step("WMI 클래스 (조금 걸릴 수 있습니다)")
	r.WMI, r.WMIError = readWMI()
	r.GPUs = r.WMI.GPU

	step("제조사 서비스와 ACPI 장치")
	r.Services = vendorServices()
	r.ACPIDevices = acpiDevices()
	return r
}

// ---- ASUS: the same gets as the app's diagnosis (fan_asus_windows.go) ----

const (
	atkControlCode = 0x0022240C
	atkGet         = 0x53545344 // DSTS: a get, which only reads
	atkPresent     = 0x00010000
)

var asusDevices = []struct {
	id   uint32
	name string
}{
	{0x00110013, "CPU 팬 속도"},
	{0x00110014, "GPU 팬 속도"},
	{0x00110031, "중간 팬 속도"},
	{0x00120075, "성능 모드"},
	{0x00110018, "팬 부스트 모드"},
	{0x00110019, "성능 모드 (Vivobook 방식)"},
	{0x00110024, "CPU 팬 곡선"},
	{0x00110025, "GPU 팬 곡선"},
	{0x00110032, "중간 팬 곡선"},
}

func probeASUS(r *report) {
	name, _ := syscall.UTF16PtrFromString(`\\.\ATKACPI`)
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return
	}
	defer syscall.CloseHandle(h)
	r.ASUS = true
	found := map[uint32]bool{}
	for _, d := range asusDevices {
		raw, ok := atkGetValue(h, d.id)
		value, meaning := "--------", "응답 없음"
		if ok {
			value = fmt.Sprintf("%08X", raw)
			meaning = "없음"
			if int32(raw) >= atkPresent {
				meaning = "있음"
				found[d.id] = true
				if fan := raw & 0xFFFF; d.id <= 0x00110014 || d.id == 0x00110031 {
					if fan <= 120 {
						meaning = fmt.Sprintf("있음 (%d rpm)", fan*100)
					}
				}
			}
		}
		r.ASUSLines = append(r.ASUSLines, fmt.Sprintf("0x%08X  %s  %s · %s", d.id, value, d.name, meaning))
	}
	r.ASUSModes = found[0x00120075]
	r.ASUSCurves = found[0x00110024] && found[0x00110025]
	r.ASUSVivobook = found[0x00110019]
}

func atkGetValue(h syscall.Handle, device uint32) (uint32, bool) {
	var in [16]byte
	binary.LittleEndian.PutUint32(in[0:], atkGet)
	binary.LittleEndian.PutUint32(in[4:], 8)
	binary.LittleEndian.PutUint32(in[8:], device)
	var out [16]byte
	var n uint32
	r, _, _ := procDeviceIoControl.Call(uintptr(h), atkControlCode,
		uintptr(unsafe.Pointer(&in[0])), uintptr(len(in)), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)),
		uintptr(unsafe.Pointer(&n)), 0)
	return binary.LittleEndian.Uint32(out[:]), r != 0
}

// ---- ACPI tables, for reading the firmware's fan code offline ----

// readACPITables reads the tables from the registry, where Windows keeps each,
// SSDTs too, and then those the registry lacks from the firmware table API,
// which gives only the first of a signature.
func readACPITables() ([]acpiTable, string) {
	tables := registryTables()
	have := map[string]bool{}
	for _, t := range tables {
		have[t.Sig[:4]] = true
	}
	api, err := firmwareTables()
	for _, t := range api {
		if !have[t.Sig] {
			tables = append(tables, t)
		}
	}
	if len(tables) > 0 {
		err = ""
	}
	return tables, err
}

// registryTables reads HKLM\HARDWARE\ACPI\<name>\<OEM>\<table>\<revision>.
// The names are mostly signatures, but FADT for FACP and SSD1… for more SSDTs.
func registryTables() []acpiTable {
	const root = `HARDWARE\ACPI`
	var tables []acpiTable
	for _, name := range subkeys(root) {
		for _, oem := range subkeys(root + `\` + name) {
			for _, id := range subkeys(root + `\` + name + `\` + oem) {
				for _, rev := range subkeys(root + `\` + name + `\` + oem + `\` + id) {
					data := regBinary(root+`\`+name+`\`+oem+`\`+id+`\`+rev, "00000000")
					if len(data) < 4 {
						continue
					}
					sig := printable(data[:4])
					if name != sig {
						sig += "-" + printable([]byte(name))
					}
					tables = append(tables, acpiTable{Sig: sig, Size: len(data), Count: 1, Data: data})
				}
			}
		}
	}
	return tables
}

func firmwareTables() ([]acpiTable, string) {
	const provider = 'A'<<24 | 'C'<<16 | 'P'<<8 | 'I'
	n, _, _ := procEnumFirmwareTables.Call(provider, 0, 0)
	if n == 0 {
		return nil, "목록을 읽지 못함"
	}
	ids := make([]byte, n)
	procEnumFirmwareTables.Call(provider, uintptr(unsafe.Pointer(&ids[0])), n)
	var tables []acpiTable
	index := map[uint32]int{}
	for i := 0; i+4 <= len(ids); i += 4 {
		id := binary.LittleEndian.Uint32(ids[i:])
		if j, ok := index[id]; ok {
			tables[j].Count++
			continue
		}
		size, _, _ := procGetFirmwareTable.Call(provider, uintptr(id), 0, 0)
		t := acpiTable{Sig: printable(ids[i : i+4]), Count: 1}
		if size > 0 {
			t.Data = make([]byte, size)
			got, _, _ := procGetFirmwareTable.Call(provider, uintptr(id), uintptr(unsafe.Pointer(&t.Data[0])), size)
			t.Data = t.Data[:min(int(got), int(size))]
			t.Size = len(t.Data)
		}
		index[id] = len(tables)
		tables = append(tables, t)
	}
	return tables, ""
}

func printable(b []byte) string {
	s := []byte{}
	for _, c := range b {
		if c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
			s = append(s, c)
		} else {
			s = append(s, '_')
		}
	}
	return string(s)
}

// ---- WMI: class definitions only, which need no administrator rights ----

type wmiClass struct {
	Name, Super, Guid string
	Props             []string
	Methods           []struct {
		Name, Qualifiers string
		Params           []string
	}
}

type wmiResult struct {
	All []struct {
		Name, Super string
	}
	Vendor []wmiClass
	GPU    []string
}

// wmiScript lists the classes of root\wmi, and details those that firmware or
// a vendor driver gives: those with a GUID, except Windows' own and trace ones.
const wmiScript = `
$ErrorActionPreference = 'SilentlyContinue'
[Console]::OutputEncoding = [Text.Encoding]::UTF8
$all = @(Get-CimClass -Namespace root/wmi)
$sup = @{}; foreach ($x in $all) { $sup[$x.CimClassName] = $x.CimSuperClassName }
function Root($n) { $r = $n; $i = 0; while ($sup[$r] -and $i -lt 20) { $r = $sup[$r]; $i++ }; $r }
function Q($qs) { @($qs | ForEach-Object { if ($null -ne $_.Value -and $_.Value -ne $true) { "$($_.Name)=$($_.Value)" } else { $_.Name } }) -join ', ' }
$skip = '^(MSNdis|MSNDIS|MSiSCSI|ISCSI|MSFC|MSTape|MSStorage|MSDisk|MSSerial|MSParallel|MSRedbook|MSKeyboard|MSMouse|MSPower|MSChanger|MSMCA|MSSmBios|MSVerifier|WmiMonitor|MSAcpi|HBA|MS_SM|MSWmi|MSPCI|MSMemory|Msft|MSNT|MSIde|Battery|SystemConfig|WMI_|PortCls|HDAud|Kernel|Processor|Idle|POWER_|MS_SystemInformation|IntelWiFi|TeeDriver|WHEA|RegisteredGuids)'
$vendor = @($all | Where-Object {
  ($_.CimClassQualifiers.Name -contains 'guid') -and ($_.CimClassName -notmatch $skip) -and
  ((Root $_.CimClassName) -notin @('EventTrace', 'MSNT_SystemTrace', 'MSNdis'))
} | ForEach-Object {
  $c = $_
  [pscustomobject]@{
    Name = $c.CimClassName
    Super = [string]$c.CimSuperClassName
    Guid = [string](($c.CimClassQualifiers | Where-Object Name -eq 'guid').Value)
    Props = @($c.CimClassProperties | ForEach-Object { "$($_.Name): $($_.CimType) [$(Q $_.Qualifiers)]" })
    Methods = @($c.CimClassMethods | ForEach-Object {
      $m = $_
      [pscustomobject]@{
        Name = $m.Name
        Qualifiers = [string](Q $m.Qualifiers)
        Params = @($m.Parameters | ForEach-Object { "$($_.Name): $($_.CimType) $($_.ReferenceClassName) [$(Q $_.Qualifiers)]" })
      }
    })
  }
})
$gpu = @(Get-CimInstance Win32_VideoController | ForEach-Object { [string]$_.Name })
[pscustomobject]@{
  All = @($all | ForEach-Object { [pscustomobject]@{ Name = $_.CimClassName; Super = [string]$_.CimSuperClassName } })
  Vendor = $vendor
  GPU = $gpu
} | ConvertTo-Json -Depth 6 -Compress
`

func readWMI() (wmiResult, string) {
	var res wmiResult
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	u := utf16.Encode([]rune(wmiScript))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[i*2:], c)
	}
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(b))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return res, err.Error()
	}
	out = []byte(strings.TrimPrefix(strings.TrimSpace(string(out)), string(rune(0xFEFF))))
	if err := json.Unmarshal(out, &res); err != nil {
		return res, "결과를 읽지 못함: " + err.Error()
	}
	return res, ""
}

// ---- services and ACPI devices, from the registry ----

var vendorWords = regexp.MustCompile(`(?i)asus|atk|armoury|\brog\b|lenovo|legion|vantage|thinkpad|dell|alienware|awcc|\bhp|hpq|omen|\bmsi\b|msi_|dragon|\bacer|predator|nitro|gigabyte|aorus|razer|samsung|huawei|xiaomi|clevo|tongfang|tuxedo|xmg|winring|inpout|dptf|esif|platform ?framework|innovation`)

func vendorServices() []string {
	const key = `SYSTEM\CurrentControlSet\Services`
	var out []string
	for _, name := range subkeys(key) {
		if strings.EqualFold(name, "msiserver") {
			continue
		}
		display := regString(key+`\`+name, "DisplayName")
		image := regString(key+`\`+name, "ImagePath")
		if vendorWords.MatchString(name) || vendorWords.MatchString(display) || vendorWords.MatchString(image) {
			out = append(out, fmt.Sprintf("%s | %s | %s", name, display, image))
		}
	}
	return out
}

func acpiDevices() []string {
	const key = `SYSTEM\CurrentControlSet\Enum\ACPI`
	var out []string
	for _, id := range subkeys(key) {
		desc := ""
		if inst := subkeys(key + `\` + id); len(inst) > 0 {
			desc = regString(key+`\`+id+`\`+inst[0], "DeviceDesc")
			if i := strings.LastIndex(desc, ";"); i >= 0 {
				desc = desc[i+1:]
			}
		}
		out = append(out, fmt.Sprintf("%-12s %s", id, desc))
	}
	return out
}

// ---- the verdict ----

// vendorPaths are the fan control ways known from other projects, by class
// name.
var vendorPaths = []struct {
	vendor  string
	pattern *regexp.Regexp
}{
	{"Lenovo Legion", regexp.MustCompile(`^LENOVO_(FAN_METHOD|GAMEZONE_DATA|OTHER_METHOD|FAN_TABLE_DATA)$`)},
	{"Lenovo (기타)", regexp.MustCompile(`(?i)^lenovo`)},
	{"Dell Alienware/G 시리즈", regexp.MustCompile(`^AWCCWmiMethodFunction$`)},
	{"HP", regexp.MustCompile(`(?i)^hpqB`)},
	{"MSI", regexp.MustCompile(`(?i)^msi_`)},
	{"Acer", regexp.MustCompile(`(?i)^(acer|predator)`)},
	{"Gigabyte", regexp.MustCompile(`^GB_`)},
}

// vendorFound names, for each known way, the classes this PC has.
func (r *report) vendorFound() map[string][]string {
	found := map[string][]string{}
	for _, c := range r.WMI.Vendor {
		for _, p := range vendorPaths {
			if p.pattern.MatchString(c.Name) {
				found[p.vendor] = append(found[p.vendor], c.Name)
				break
			}
		}
	}
	return found
}

func (r *report) verdict() []string {
	var v []string
	switch {
	case r.ASUS && (r.ASUSModes || r.ASUSCurves):
		s := "ASUS: 지금 앱으로 팬 제어가 될 가능성이 높습니다"
		switch {
		case r.ASUSModes && r.ASUSCurves:
			s += " (성능 모드와 고급 설정 곡선)."
		case r.ASUSModes:
			s += " (성능 모드만, 곡선 없음)."
		default:
			s += " (곡선만, 성능 모드 없음: 실기기 확인이 필요한 구성)."
		}
		v = append(v, s)
	case r.ASUS && r.ASUSVivobook:
		v = append(v, "ASUS: Vivobook 방식 성능 모드가 있습니다. 아직 앱이 지원하지 않습니다.")
	case r.ASUS:
		v = append(v, "ASUS: 드라이버는 있지만 성능 모드와 곡선이 보이지 않습니다. 팬 속도 표시만 될 수 있습니다.")
	}
	found := r.vendorFound()
	names := make([]string, 0, len(found))
	for k := range found {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		v = append(v, fmt.Sprintf("%s: 팬 제어 후보 경로가 있습니다 (%s). 아직 앱이 지원하지 않고, 지원하려면 관리자 권한 구성이 필요합니다.",
			k, strings.Join(found[k], ", ")))
	}
	if len(v) == 0 {
		v = append(v, "알려진 팬 제어 경로를 찾지 못했습니다. 함께 저장한 ACPI 표로 더 분석할 수 있습니다.")
	}
	return v
}

// ---- writing ----

func save(r *report) (string, error) {
	name := fmt.Sprintf("fan-probe-%s-%s", safeName(r.Model), r.Time.Format("20060102-150405"))
	var dir string
	var err error
	for _, base := range candidateDirs() {
		dir = filepath.Join(base, name)
		if err = os.MkdirAll(filepath.Join(dir, "acpi"), 0o755); err == nil {
			break
		}
	}
	if err != nil {
		return "", err
	}
	for _, t := range r.Tables {
		if len(t.Data) > 0 {
			if err := os.WriteFile(filepath.Join(dir, "acpi", t.Sig+".bin"), t.Data, 0o644); err != nil {
				return "", err
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "report.txt"), []byte(r.text()), 0o644); err != nil {
		return "", err
	}
	var all strings.Builder
	for _, c := range r.WMI.All {
		fmt.Fprintf(&all, "%s <- %s\n", c.Name, c.Super)
	}
	if err := os.WriteFile(filepath.Join(dir, "wmi-all.txt"), []byte(all.String()), 0o644); err != nil {
		return "", err
	}
	var vendor strings.Builder
	for _, c := range r.WMI.Vendor {
		fmt.Fprintf(&vendor, "class %s", c.Name)
		if c.Super != "" {
			fmt.Fprintf(&vendor, " : %s", c.Super)
		}
		fmt.Fprintf(&vendor, "  guid=%s\n", c.Guid)
		for _, p := range c.Props {
			fmt.Fprintf(&vendor, "  prop   %s\n", p)
		}
		for _, m := range c.Methods {
			fmt.Fprintf(&vendor, "  method %s [%s]\n", m.Name, m.Qualifiers)
			for _, p := range m.Params {
				fmt.Fprintf(&vendor, "         %s\n", p)
			}
		}
		vendor.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "wmi-vendor.txt"), []byte(vendor.String()), 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

// candidateDirs are where the folder may go: next to the probe, then the
// desktop, then the temporary folder.
func candidateDirs() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "Desktop"))
	}
	return append(dirs, os.TempDir())
}

func safeName(s string) string {
	var b strings.Builder
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '-', c == '_':
			b.WriteRune(c)
		case c == ' ' || c == '.':
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func (r *report) text() string {
	var b strings.Builder
	line := func(k, v string) {
		if v == "" {
			v = "-"
		}
		fmt.Fprintf(&b, "%-13s %s\n", k+":", v)
	}
	yes := func(v bool) string {
		if v {
			return "yes"
		}
		return "no"
	}
	section := func(s string) { fmt.Fprintf(&b, "\n[%s]\n", s) }
	b.WriteString("팬 제어 기기 조사 보고서\n")
	b.WriteString("읽기만 해서 만들었고, 팬이나 펌웨어 설정은 바꾸지 않았습니다.\n\n")
	line("probe", version)
	line("time", r.Time.Format(time.RFC3339))
	line("manufacturer", r.Manufacturer)
	line("model", r.Model)
	line("family", r.Family)
	line("board", r.Board)
	line("bios", strings.TrimSpace(r.BIOS+" "+r.BIOSDate))
	line("windows", r.Windows)
	line("cpu", r.CPU)
	line("gpu", strings.Join(r.GPUs, " / "))
	line("admin", yes(r.Admin))

	section("판정")
	for _, l := range r.verdict() {
		b.WriteString(l + "\n")
	}

	section("ASUS")
	line("driver", yes(r.ASUS))
	for _, l := range r.ASUSLines {
		b.WriteString(l + "\n")
	}

	section("ACPI 표 (acpi 폴더)")
	if r.TablesError != "" {
		b.WriteString(r.TablesError + "\n")
	}
	for _, t := range r.Tables {
		note := ""
		if t.Count > 1 {
			note = fmt.Sprintf("  (펌웨어에 %d개, 첫 번째만 저장)", t.Count)
		}
		fmt.Fprintf(&b, "%-5s %7d bytes%s\n", t.Sig, t.Size, note)
	}

	section("제조사 WMI 클래스 (자세한 내용은 wmi-vendor.txt)")
	if r.WMIError != "" {
		b.WriteString("오류: " + r.WMIError + "\n")
	}
	fmt.Fprintf(&b, "root\\wmi 클래스 %d개 중 %d개\n", len(r.WMI.All), len(r.WMI.Vendor))
	for _, c := range r.WMI.Vendor {
		ms := make([]string, len(c.Methods))
		for i, m := range c.Methods {
			ms[i] = m.Name
		}
		s := c.Name
		if len(ms) > 0 {
			s += "  methods: " + strings.Join(ms, ",")
		}
		b.WriteString(s + "\n")
	}

	section("제조사 관련 서비스·드라이버")
	for _, s := range r.Services {
		b.WriteString(s + "\n")
	}

	section("ACPI 장치")
	for _, s := range r.ACPIDevices {
		b.WriteString(s + "\n")
	}
	return b.String()
}

func zipDir(dir, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := zip.NewWriter(f)
	err = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(filepath.Dir(dir), p)
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		h.Method = zip.Deflate
		dst, err := w.CreateHeader(h)
		if err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(dst, src)
		return err
	})
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// ---- registry ----

func regString(key, name string) string {
	k, err := openKey(key, syscall.KEY_QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer syscall.RegCloseKey(k)
	n, _ := syscall.UTF16PtrFromString(name)
	var size, typ uint32
	if syscall.RegQueryValueEx(k, n, nil, &typ, nil, &size) != nil || size < 2 {
		return ""
	}
	data := make([]uint16, size/2+1)
	if syscall.RegQueryValueEx(k, n, nil, &typ, (*byte)(unsafe.Pointer(&data[0])), &size) != nil {
		return ""
	}
	if typ != syscall.REG_SZ && typ != syscall.REG_EXPAND_SZ {
		return ""
	}
	return syscall.UTF16ToString(data)
}

func regBinary(key, name string) []byte {
	k, err := openKey(key, syscall.KEY_QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer syscall.RegCloseKey(k)
	n, _ := syscall.UTF16PtrFromString(name)
	var size uint32
	if syscall.RegQueryValueEx(k, n, nil, nil, nil, &size) != nil || size == 0 {
		return nil
	}
	data := make([]byte, size)
	if syscall.RegQueryValueEx(k, n, nil, nil, &data[0], &size) != nil {
		return nil
	}
	return data[:size]
}

func regNumber(key, name string) (uint32, bool) {
	k, err := openKey(key, syscall.KEY_QUERY_VALUE)
	if err != nil {
		return 0, false
	}
	defer syscall.RegCloseKey(k)
	n, _ := syscall.UTF16PtrFromString(name)
	var v, size uint32 = 0, 4
	if syscall.RegQueryValueEx(k, n, nil, nil, (*byte)(unsafe.Pointer(&v)), &size) != nil {
		return 0, false
	}
	return v, true
}

func subkeys(key string) []string {
	k, err := openKey(key, syscall.KEY_ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	defer syscall.RegCloseKey(k)
	var out []string
	for i := uint32(0); ; i++ {
		var buf [256]uint16
		n := uint32(len(buf))
		if syscall.RegEnumKeyEx(k, i, &buf[0], &n, nil, nil, nil, nil) != nil {
			break
		}
		out = append(out, syscall.UTF16ToString(buf[:n]))
	}
	return out
}

func openKey(key string, access uint32) (syscall.Handle, error) {
	p, _ := syscall.UTF16PtrFromString(key)
	var k syscall.Handle
	err := syscall.RegOpenKeyEx(hklm, p, 0, access, &k)
	return k, err
}

func windowsVersion() string {
	const key = `SOFTWARE\Microsoft\Windows NT\CurrentVersion`
	name := regString(key, "ProductName")
	build := regString(key, "CurrentBuild")
	var n int
	fmt.Sscan(build, &n)
	if n >= 22000 {
		name = strings.Replace(name, "Windows 10", "Windows 11", 1)
	}
	if ubr, ok := regNumber(key, "UBR"); ok {
		build = fmt.Sprintf("%s.%d", build, ubr)
	}
	return strings.TrimSpace(fmt.Sprintf("%s %s (%s)", name, regString(key, "DisplayVersion"), build))
}
