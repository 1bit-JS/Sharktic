<p align="center"><img src="docs/banner.png" alt="Sharktic: PC monitoring &amp; fan control" width="100%"></p>

# Sharktic

A tray hardware monitor for Windows PCs and laptops. It shows CPU, GPU, memory and temperature as tray icons, and on supported laptops it lets you change the fan mode and temperature-based fan curves without entering the BIOS. The app runs without administrator rights; the optional advanced fan control adds a small service for hardware that needs them.

> The app's interface is currently in Korean only. Menu labels below are given in English with the original Korean in parentheses.

## Support by manufacturer

The hardware monitor works on any Windows 10 or 11 PC, regardless of manufacturer. The table below covers **fan control**.

| Manufacturer | Models | Status | Notes |
| --- | --- | --- | --- |
| ASUS | ROG, TUF | **Supported** | Performance modes (Silent, Balanced, Turbo) and fan curves. Verified on ROG Strix G512LI; other models should work based on firmware analysis |
| ASUS | Vivobook, Zenbook | Partial | Fan speed readout only. Performance modes are not supported because their values differ per model |
| Lenovo | Legion | In progress | Firmware analysis done. Performance modes on most models, fan curves on Gen 7 and 8. Hardware testing pending |
| Lenovo | ThinkPad | Investigated | No firmware interface; fans are set through the embedded controller, which needs advanced fan control. Development pending |
| Lenovo | LOQ, IdeaPad | Not investigated | |
| Dell | Alienware, G Series | Investigated | Control path identified, awaiting development |
| HP | OMEN, Victus | Investigated | Control path identified, awaiting development. Manual settings reset about every 2 minutes |
| MSI | Gaming laptops | On hold | Requires talking to the chip directly, which is risky, so it is low priority |
| Acer | Nitro, Predator | On hold | No usable control path found |
| Samsung | Galaxy Book | Not investigated | |
| LG | gram | Not investigated | |
| Microsoft | Surface | Not investigated | |
| Hansung | TFG, BossMonster, etc. | Not investigated | |
| Desktop boards | Nuvoton NCT67xx, ITE IT86xx chips | Testing | Fan speed readout through advanced fan control, not yet verified on a real board. Fan control comes next |

- **Supported:** works in the current version
- **Partial:** only some features work
- **In progress:** support is being developed
- **Investigated:** the control method is known; development starts once a user can test on real hardware
- **On hold:** no plans for now
- **Not investigated:** not looked at yet

Fan control on other laptops and on desktops needs administrator rights, which the optional advanced fan control provides (see below). Apple Macs are out of scope.

### Requesting support for your laptop

Run `fan-probe.exe` from [Releases](https://github.com/1bit-JS/Sharktic/releases); it creates a zip file next to itself. Upload that file to [Issues](https://github.com/1bit-JS/Sharktic/issues) along with your laptop's model name. The tool only reads information and never changes settings. The report includes the names of some installed programs, so you can check `report.txt` inside the zip before uploading.

## Installation

1. Download `Sharktic-<version>-windows-amd64.zip` from [Releases](https://github.com/1bit-JS/Sharktic/releases) and extract it to any folder.
2. Run `Sharktic.exe`. There is no installer; an icon appears in the tray.
3. The program is unsigned, so if Windows SmartScreen blocks it, click "More info → Run anyway".

Windows puts new icons inside the tray overflow (^); drag them out to keep them always visible.

**Uninstall:** if advanced fan control is on, turn it off first from Advanced fan control (고급 팬 제어…) in the right-click menu. Then turn off start at login, quit the app, and delete the folder and `%AppData%\Sharktic`.

## Features

**Monitor**
- Four tray icons: PC temperature, CPU, GPU and memory. Temperatures turn orange from 70°C and red from 85°C; usage turns red from 90%
- GPU temperature on NVIDIA, AMD and Intel GPUs whose drivers report it, and the temperature of internal NVMe and SATA SSDs
- Hover for a summary, left-click for a detailed panel (including the top programs by CPU and memory)
- Values that cannot be measured are shown as `--`

**Fan control** (supported laptops)
- **Performance modes:** pick one of the laptop firmware's own modes (Silent, Balanced, Turbo, etc.)
- **Curve presets:** laptops with fan curves but no firmware modes get app-made Silent, Balanced and Max curves
- **Advanced settings:** set fan speed by temperature for the CPU and GPU fans separately and see it as a graph. Warns when values go below the recommended ones
- Your choice is saved and reapplied when the app restarts and when the PC wakes from sleep. Turning on "Apply this fan setting at startup" (컴퓨터를 켤 때 이 팬 설정 적용) starts the app automatically at login

**Advanced fan control** (고급 팬 제어, optional): turn it on from the panel or the right-click menu. One administrator prompt installs a Sharktic service and the [PawnIO](https://pawnio.eu) driver, the signed open-source driver LibreHardwareMonitor and FanControl also use. The app itself keeps running without administrator rights.
- **Now:** the CPU temperature measured by the CPU itself (Intel; AMD Ryzen not yet verified), also on desktops where the PC temperature shows `--`; fan speeds of desktop boards with Nuvoton or ITE chips (testing)
- **Next:** temperature-based control of desktop fans, ThinkPad fans
- It can be turned off from the same window at any time, which removes the service. PawnIO is removed only if you choose so, as other programs may use it.

**Copy diagnostics** (진단 정보 복사): copies the model name, BIOS version and fan-related firmware values to the clipboard from the right-click menu, and with advanced fan control on, its service version, the board chip found and the end of the service log. Please paste it when reporting a problem.

## Caveats

- Fan control uses undocumented firmware interfaces. Running it alongside a vendor app such as Armoury Crate may cause the two to overwrite each other's settings.
- Setting fan curves too low can make the device run hot and lose performance.
- On devices whose firmware does not report the current mode, nothing is shown as selected until you choose one in the app, and switching with the Fn key may make the display out of date.
- Some online games' anti-cheat (such as FACEIT) may object to the PawnIO driver of advanced fan control. Turn advanced fan control off, with PawnIO, if that happens.
- Provided without warranty.

## Development

Requires Go 1.27.3 or later and Windows.

    go test ./...                       # includes tests against this PC's real sensors
    go test -short ./...                # hardware-independent tests only
    go tool mygo build -skip-installer  # build/windows-amd64/Sharktic.exe
    go build -o build/fan-probe.exe ./cmd/fan-probe

## License

MIT License (`LICENSE`). Licenses for the libraries used are in `THIRD_PARTY_NOTICES.txt`.

---

<details>
<summary><b>한국어</b></summary>

Windows PC와 노트북용 트레이 하드웨어 모니터입니다. CPU, GPU, 메모리, 온도를 트레이 아이콘으로 보여 주고, 지원하는 노트북에서는 BIOS에 들어가지 않고도 팬 모드와 온도별 팬 곡선을 바꿀 수 있습니다. 앱은 관리자 권한 없이 동작하고, 선택 기능인 고급 팬 제어를 켜면 관리자 권한이 필요한 하드웨어를 다루는 작은 서비스가 추가됩니다.

## 제조사별 지원 현황

하드웨어 모니터는 제조사와 관계없이 Windows 10, 11 PC에서 동작합니다. 아래는 **팬 제어** 지원 현황입니다.

| 제조사 | 대상 | 상태 | 내용 |
| --- | --- | --- | --- |
| ASUS | ROG, TUF | **지원** | 성능 모드(조용함, 균형, 터보)와 팬 곡선. ROG Strix G512LI에서 확인했고, 다른 모델은 펌웨어 분석상 가능 |
| ASUS | Vivobook, Zenbook | 일부 | 팬 속도 표시만. 성능 모드는 모델마다 값이 달라 미지원 |
| Lenovo | Legion | 개발 중 | 펌웨어 분석 완료. 성능 모드는 대부분, 팬 곡선은 7·8세대. 실기기 테스트 예정 |
| Lenovo | ThinkPad | 조사 완료 | 펌웨어 경로가 없고 EC로 팬을 제어하는 방식이라 고급 팬 제어가 필요. 개발 대기 |
| Lenovo | LOQ, IdeaPad | 미조사 | |
| Dell | Alienware, G 시리즈 | 조사 완료 | 제어 경로 확인, 개발 대기 |
| HP | OMEN, Victus | 조사 완료 | 제어 경로 확인, 개발 대기. 수동 설정이 약 2분마다 초기화되는 제약 있음 |
| MSI | 게이밍 노트북 | 보류 | 칩을 직접 다루는 방식이라 위험이 커서 후순위 |
| Acer | Nitro, Predator | 보류 | 쓸 수 있는 제어 경로를 찾지 못함 |
| 삼성 | Galaxy Book | 미조사 | |
| LG | gram | 미조사 | |
| Microsoft | Surface | 미조사 | |
| 한성컴퓨터 | TFG, BossMonster 등 | 미조사 | |
| 데스크톱 메인보드 | Nuvoton NCT67xx, ITE IT86xx 칩 | 시험 중 | 고급 팬 제어로 팬 속도 표시. 실제 보드에서는 아직 확인 전. 팬 제어는 다음 단계 |

- **지원:** 지금 버전에서 동작
- **일부:** 일부 기능만 동작
- **개발 중:** 지원 작업을 진행 중
- **조사 완료:** 제어 방법은 확인했고, 실기기로 테스트할 사용자가 있으면 개발
- **보류:** 당장은 지원 계획 없음
- **미조사:** 아직 살펴보지 않음

다른 노트북과 데스크톱의 팬 제어는 관리자 권한이 필요해서, 선택 기능인 고급 팬 제어(아래)로 지원합니다. Apple Mac은 대상이 아닙니다.

### 내 노트북 지원을 요청하려면

[Releases](https://github.com/1bit-JS/Sharktic/releases)의 `fan-probe.exe`를 실행하면 옆에 zip 파일이 생깁니다. [Issues](https://github.com/1bit-JS/Sharktic/issues)에 노트북 모델명과 함께 그 파일을 올려 주세요. 이 도구는 정보를 읽기만 하고 설정은 바꾸지 않습니다. 설치된 일부 프로그램 이름이 포함되므로, 올리기 전에 zip 안의 `report.txt`를 확인할 수 있습니다.

## 설치

1. [Releases](https://github.com/1bit-JS/Sharktic/releases)에서 `Sharktic-<버전>-windows-amd64.zip`을 받아 원하는 폴더에 풉니다.
2. `Sharktic.exe`를 실행합니다. 설치 과정은 없고, 트레이에 아이콘이 생깁니다.
3. 서명되지 않은 프로그램이라 Windows SmartScreen이 막으면 "추가 정보 → 실행"을 누릅니다.

새 아이콘은 Windows가 트레이 화살표(^) 안에 넣으므로, 밖으로 끌어내면 항상 보입니다.

**지우기:** 고급 팬 제어를 켰다면 오른쪽 클릭 메뉴의 "고급 팬 제어…"에서 먼저 끕니다. 그다음 로그인 시 자동 실행을 끄고, 앱을 종료한 뒤 폴더와 `%AppData%\Sharktic`을 지웁니다.

## 기능

**모니터**
- 트레이 아이콘 4개: PC 온도, CPU, GPU, 메모리. 온도는 70°C부터 주황, 85°C부터 빨강, 사용량은 90%부터 빨강
- 드라이버가 알려 주는 NVIDIA, AMD, Intel GPU의 온도와 내장 NVMe, SATA SSD의 온도
- 마우스를 올리면 요약, 왼쪽 클릭하면 자세한 패널(CPU·메모리 점유 상위 프로그램 포함)
- 측정할 수 없는 값은 `--`로 표시

**팬 제어** (지원 노트북)
- **성능 모드:** 노트북 펌웨어의 모드(조용함, 균형, 터보 등)를 그대로 고름
- **곡선 프리셋:** 펌웨어 모드가 없고 팬 곡선만 지원하는 노트북에는 앱이 만든 조용함, 균형, 최대 곡선을 제공
- **고급 설정:** CPU, GPU 팬별로 온도에 따른 팬 속도를 직접 정하고 그래프로 확인. 권장값보다 낮으면 경고
- 고른 설정은 저장되어, 앱을 다시 켤 때와 절전에서 깨어날 때 다시 적용. "컴퓨터를 켤 때 이 팬 설정 적용"을 켜면 로그인할 때 자동 실행

**고급 팬 제어** (선택 기능): 패널이나 오른쪽 클릭 메뉴에서 켭니다. 관리자 확인 한 번으로 Sharktic 서비스와 [PawnIO](https://pawnio.eu) 드라이버(LibreHardwareMonitor, FanControl도 쓰는 서명된 오픈 소스 드라이버)를 설치합니다. 앱 자체는 계속 관리자 권한 없이 동작합니다.
- **지금:** CPU가 직접 재는 CPU 온도(Intel. AMD Ryzen은 확인 전). PC 온도가 `--`로 나오는 데스크톱에서도 표시. Nuvoton, ITE 칩을 쓰는 데스크톱 메인보드의 팬 속도(시험 중)
- **다음:** 데스크톱 팬의 온도별 제어, ThinkPad 팬
- 같은 창에서 언제든 끌 수 있고, 끄면 서비스를 제거합니다. PawnIO는 다른 프로그램도 쓸 수 있어서 선택했을 때만 지웁니다.

**진단 정보 복사:** 오른쪽 클릭 메뉴에서 모델명, BIOS 버전과 팬 관련 펌웨어 값을 클립보드에 복사합니다. 고급 팬 제어를 켰다면 서비스 버전, 찾은 메인보드 칩, 서비스 로그의 마지막 줄도 함께 복사합니다. 문제를 알릴 때 붙여 넣어 주세요.

## 주의

- 팬 제어는 제조사가 공개하지 않은 펌웨어 인터페이스를 씁니다. Armoury Crate 같은 제조사 앱과 함께 쓰면 서로 설정을 덮어쓸 수 있습니다.
- 팬 곡선을 낮게 잡으면 기기가 뜨거워지고 성능이 떨어질 수 있습니다.
- 펌웨어가 현재 모드를 알려 주지 않는 기기에서는, 앱에서 고르기 전에는 선택 표시가 없고 Fn 키로 바꾸면 표시와 달라질 수 있습니다.
- 일부 온라인 게임의 안티치트(예: FACEIT)가 고급 팬 제어의 PawnIO 드라이버를 문제 삼을 수 있습니다. 그럴 땐 고급 팬 제어를 PawnIO와 함께 끄세요.
- 보증 없이 제공됩니다.

## 개발

Go 1.27.3 이상과 Windows가 필요합니다.

    go test ./...                       # 이 PC의 실제 센서까지 검사
    go test -short ./...                # 기기와 무관한 검사만
    go tool mygo build -skip-installer  # build/windows-amd64/Sharktic.exe
    go build -o build/fan-probe.exe ./cmd/fan-probe

## 라이선스

MIT License (`LICENSE`). 사용한 라이브러리의 라이선스는 `THIRD_PARTY_NOTICES.txt`에 있습니다.

</details>
