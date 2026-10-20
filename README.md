[简体中文](README.zh-CN.md)

# Hardware Protocol Emulation

Hardware Protocol Emulation (HPE) is a cross-platform, software-only simulator for learning and exploring hardware communication protocols. It creates virtual devices, runs protocol scenarios, and displays decoded events and signal timing. No physical bus adapter or peripheral is required.

The simulator and command-line interface are written in C. The desktop application is a separate Avalonia GUI that uses the simulator through a C API bridge.

## Protocol coverage

| Protocol | Current model | Example |
| --- | --- | --- |
| I2C | 7-bit addressing, START/repeated START/STOP, ACK/NACK, register devices, address scanning and injected NACKs | [`i2c_basic.hpe`](examples/i2c/i2c_basic.hpe) |
| SPI | SPI modes 0–3, full-duplex byte transfers, and a software SPI NOR flash model | [`spi_nor.hpe`](examples/spi/spi_nor.hpe) |
| SD / eMMC | Native command and response flow, card initialization, register inspection, and 512-byte block I/O | [`sd_emmc_blocks.hpe`](examples/sdmmc/sd_emmc_blocks.hpe) |
| USB 2.0 | Full-speed device enumeration, standard control requests, bulk echo and interrupt IN reports | [`usb2_echo.hpe`](examples/usb/usb2_echo.hpe) |
| PCI Express | Educational endpoint enumeration, configuration space, BAR0 and MMIO accesses, with basic TLP acknowledgment and replay | [`pcie_basic.hpe`](examples/pcie/pcie_basic.hpe) |

These are teaching models, not complete electrical or standards-conformance implementations. Each protocol currently implements a defined subset; for example, USB high-speed and hubs are not modeled, and PCIe PHY behavior, general flow control and switches are outside the current model. I2C has no universal enumeration procedure, so `scan` probes the unreserved 7-bit address range and reports devices that acknowledge.

## Build

HPE provides native build bootstrappers for Linux and Windows. Run the build commands below from the repository root (the directory containing `README.md`). A clean build needs an internet connection to download pinned, SHA-256-verified toolchain and dependency archives. Once the source is present, building does not require a preinstalled compiler, Go, Python, .NET SDK, CMake, Ninja, Git, or package manager. The `build` command downloads and installs the required local tools automatically; running `tools` first is optional.

### Getting the source

On Linux, clone the repository:

```sh
git clone https://github.com/Elrond-Xie/HardwareProtocolEmulation.git
cd HardwareProtocolEmulation
./bootstrap/bin/hpe-bootstrap-linux-x64 build
```

On Windows, get the project source ZIP from the [GitHub repository page](https://github.com/Elrond-Xie/HardwareProtocolEmulation) using **Code → Download ZIP**, then extract it with File Explorer. Open **Command Prompt** in the extracted project directory and run the Windows build command below. This ZIP does not contain Git history, so get a fresh ZIP to update it. You do not need to find or download compiler SDKs or dependency packages yourself; the bootstrapper downloads and verifies them during the build. On Windows ARM64, use `hpe-bootstrap-windows-arm64.exe` instead.

### Linux

```sh
# From the repository root, on Linux x86-64:
./bootstrap/bin/hpe-bootstrap-linux-x64 build
```

On Linux ARM64, run this command instead:

```sh
./bootstrap/bin/hpe-bootstrap-linux-arm64 build
```

To download and prepare the local toolchain without building HPE, replace `build` with `tools`. To limit parallel compilation, set `HPE_JOBS` in the current terminal before building:

```sh
export HPE_JOBS=8
./bootstrap/bin/hpe-bootstrap-linux-x64 build
```

`HPE_JOBS` is optional; by default the build uses the detected CPU count. You can also set it for one command only: `HPE_JOBS=8 ./bootstrap/bin/hpe-bootstrap-linux-x64 build`.

### Windows

Open **Command Prompt** (`cmd.exe`), change to the repository root, and run the host architecture's bootstrapper:

```cmd
cd /d C:\path\to\Hardware_Protocol_Emulation
bootstrap\bin\hpe-bootstrap-windows-x64.exe build
```

On Windows ARM64, use `hpe-bootstrap-windows-arm64.exe` instead. To limit parallel compilation, set `HPE_JOBS` in the current Command Prompt session before building:

```cmd
set HPE_JOBS=8
bootstrap\bin\hpe-bootstrap-windows-x64.exe build
```

`HPE_JOBS` is optional; by default the build uses the detected CPU count. `set HPE_JOBS=` clears the setting in the current Command Prompt session. Builds target the host operating system and architecture; the bootstrapper does not cross-compile the application for another platform.

The bootstrapper downloads compiler SDKs and extracts them under `build/toolchains/`. Downloads show transfer progress, byte counts and current speed. Downloaded build tools, dependency sources, compiled dependencies and intermediate files go under `build/`. Final applications and runtime files are written to `install/<RID>/`, such as `install/linux-x64/` or `install/win-x64/`. Build output is incremental and reuses verified downloads, CMake/Ninja build trees, ccache entries, vcpkg packages and NuGet packages. On Linux, the bootstrapper shallow-fetches the pinned vcpkg and ccache commits with system Git when available, verifies the exact commit, and removes Git metadata. If Git is unavailable or the checkout fails, it automatically downloads the pinned SHA-256-verified tarballs. On Windows, it downloads and verifies ZIP archives directly, so Git for Windows is not required. The bootstrapper also downloads and verifies other dependency sources as part of the build.

The build uses all detected CPU cores by default. Set `HPE_JOBS` to limit parallelism, and optionally set these paths:

| Variable | Purpose | Default |
| --- | --- | --- |
| `HPE_JOBS` | Maximum parallel build jobs | Detected CPU count |
| `HPE_BUILD_ROOT` | Downloaded build tools, dependencies, caches and intermediate output | `<checkout>/build` |
| `HPE_TOOL_ROOT` | Compiler and SDK installations | `<checkout>/build/toolchains` |
| `HPE_INSTALL_ROOT` | Installed application package | `install/<RID>` |
| `HPE_LOG_DIR` | Build workflow logs | `<HPE_BUILD_ROOT>/logs` |

No environment variables need to be set for a normal build. All variables in the table are optional. To use custom paths, for example on Linux:

```sh
export HPE_BUILD_ROOT="$PWD/build-cache"
export HPE_TOOL_ROOT="$PWD/compiler-sdks"
export HPE_INSTALL_ROOT="$PWD/package"
export HPE_LOG_DIR="$PWD/build-logs"
./bootstrap/bin/hpe-bootstrap-linux-x64 build
```

In Windows Command Prompt, use Windows-style absolute paths and `set`:

```cmd
set HPE_BUILD_ROOT=C:\hpe-data\build
set HPE_TOOL_ROOT=C:\hpe-data\compiler-sdks
set HPE_INSTALL_ROOT=C:\hpe-data\install
set HPE_LOG_DIR=C:\hpe-data\logs
bootstrap\bin\hpe-bootstrap-windows-x64.exe build
```

These settings apply only to the current terminal session. Omit them to use the defaults in the table.

Linux still uses the host kernel and compatible system C library. Running the Linux GUI also requires an active graphical session such as X11, Wayland or WSLg. Windows uses its operating-system APIs. The project bundles its application runtime and required build dependencies; it does not replace the host operating system.

## Build logs and cleanup

The `tools`, `build` and `clean` commands write a full workflow log while also printing progress to the terminal. Logs include named steps, commands and working directories, download and checksum results, cache decisions, compiler output, errors and elapsed times. The newest 10 retained logs are kept in each configured log directory; older logs are removed automatically. `distclean` records a temporary log while running: on success it removes the entire build directory and discards that log, while on failure it keeps the log for diagnosis.

```sh
# Linux
./bootstrap/bin/hpe-bootstrap-linux-x64 clean
./bootstrap/bin/hpe-bootstrap-linux-x64 distclean
```

```cmd
:: Windows Command Prompt
bootstrap\bin\hpe-bootstrap-windows-x64.exe clean
bootstrap\bin\hpe-bootstrap-windows-x64.exe distclean
```

`clean` removes generated build intermediates and installed HPE applications while keeping downloaded archives, toolchains, dependency sources, tool installations, package/compiler caches and logs. `distclean` removes the complete build directory, including downloaded files, caches and old logs. Its own log is discarded after a successful cleanup and retained if cleanup fails. The temporary log path is printed when the command starts; on failure, the retained log path is printed again.

## Run

After building, run the CLI or GUI from the platform-specific installation directory. For example, on Linux:

```sh
./install/linux-x64/hpe-cli --demo --trace i2c-trace.txt
./install/linux-x64/hpe-cli \
  --scenario install/linux-x64/examples/i2c/i2c_basic.hpe \
  --trace i2c-trace.txt
./install/linux-x64/hpe-gui
```

On Windows, launch `install\win-x64\hpe-cli.exe` or `install\win-x64\hpe-gui.exe` (use `win-arm64` on ARM64). Example scenarios for all supported protocols are installed once under `install/<RID>/examples/`; shared runtime libraries are under `install/<RID>/lib/`.

The CLI prints decoded events and device results. An optional trace file records protocol events with virtual timestamps. The GUI provides scenario selection and execution, decoded-event records, a zoomable and pannable timing view, a movable cursor, selectable signal-value radices (binary, octal, decimal and hexadecimal), English and Simplified Chinese language settings, and live virtual-device register and memory inspection.

## Scenario format

Scenario files are UTF-8 text. Blank lines and lines beginning with `#` are ignored. Numeric arguments accept decimal or `0x` hexadecimal notation. A small I2C example:

```text
device eeprom 0x50 256 0xFF
device temperature_sensor 0x48 16 0x00
set 0x48 0x00 0x5A
scan
readreg 0x48 0x00 1
```

Scenarios can create virtual devices, initialize registers or storage, issue protocol transactions, and exercise selected error paths. The installed example directory contains runnable scenarios for I2C, SPI, SD/eMMC, USB and PCIe.

## Updating the bootstrap executables

The checked-in native bootstrap executables let users build HPE without installing Go. When the bootstrap source or its pinned Go version changes, maintainers can rebuild all four Linux and Windows executables with the platform script. The script downloads the pinned Go 1.27.2 archive, verifies its SHA-256 digest, and keeps the compiler, downloaded archive, module cache and compiler cache under `build/`.

```sh
# Linux or another POSIX shell
./scripts/update-bootstrap.sh
```

```cmd
:: Windows Command Prompt
scripts\update-bootstrap.bat
```

## C API

The public C API is declared in [`include/hpe/hpe.h`](include/hpe/hpe.h). Applications can create a simulation context, add virtual devices, run protocol-specific operations and inspect trace events. The opaque simulation context owns its devices and events until `hpe_sim_destroy`. Time is deterministic and virtual; API calls do not wait in real time. I2C devices can provide callbacks for register reads and writes.

## License

Project-owned code is licensed under the [PolyForm Noncommercial License 1.0.0](https://polyformproject.org/licenses/noncommercial/1.0.0); see [`LICENSE.txt`](LICENSE.txt). It permits free use, modification and redistribution for noncommercial purposes. Commercial use requires separate permission from the copyright holder. This is a source-available license, not an OSI-approved open-source license, because it restricts use by field of endeavor. The license includes specific permitted uses for educational, charitable, public research, public safety or health, environmental protection, and government organizations.

Third-party components, submodules, fonts and downloaded dependencies retain their own licenses and notices; the project license does not replace them. The project license notice is also included in the built installation package.

## Project layout

```text
apps/cli/              C command-line application
apps/gui/Hpe.Gui/      Avalonia desktop application
apps/gui/launcher/     GUI runtime launchers and platform-specific binaries
adapters/gui-c-api/    C API bridge used by the GUI
include/hpe/           Public C API
src/                   Simulation engine, devices and scenario runner
protocols/             I2C, SPI, SD/eMMC, USB and PCIe models
examples/              Runnable protocol scenarios
tests/integration/     Integration tests
assets/                Fonts and application artwork
cmake/                 CMake toolchains and vcpkg configuration
bootstrap/             Native Go build bootstrapper source and binaries
scripts/               Maintainer scripts for rebuilding bootstrap tools and GUI launchers
third_party/           Optional upstream source submodules
build/                 Dependencies, caches and intermediate output (generated)
install/<RID>/         User-facing application packages
```
