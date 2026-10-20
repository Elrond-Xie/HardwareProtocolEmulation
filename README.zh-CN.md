[English](README.md)

# 硬件协议模拟器

硬件协议模拟器（Hardware Protocol Emulation，简称 HPE）是一款跨平台、纯软件实现的通信协议学习工具。它可以创建虚拟设备、执行协议场景，并显示解码后的事件和信号时序。使用 HPE 不需要实体总线适配器或外设。

模拟器核心和命令行程序使用 C 编写。桌面 GUI 是独立的 Avalonia 应用，通过 C API 桥接层调用模拟器。

## 协议支持范围

| 协议 | 当前模拟范围 | 示例 |
| --- | --- | --- |
| I2C | 7 位地址、START/重复 START/STOP、ACK/NACK、寄存器设备、地址扫描及注入 NACK | [`i2c_basic.hpe`](examples/i2c/i2c_basic.hpe) |
| SPI | SPI 模式 0–3、全双工字节传输和软件模拟的 SPI NOR 闪存 | [`spi_nor.hpe`](examples/spi/spi_nor.hpe) |
| SD / eMMC | 原生命令与响应流程、卡初始化、寄存器查看和 512 字节块读写 | [`sd_emmc_blocks.hpe`](examples/sdmmc/sd_emmc_blocks.hpe) |
| USB 2.0 | 全速设备枚举、标准控制请求、批量回显和中断 IN 报告 | [`usb2_echo.hpe`](examples/usb/usb2_echo.hpe) |
| PCI Express | 教学用途的端点枚举、配置空间、BAR0 和 MMIO 访问，以及基础 TLP 确认与重放 | [`pcie_basic.hpe`](examples/pcie/pcie_basic.hpe) |

这些模型用于教学，不代表完整的电气层或标准符合性实现。每种协议目前只覆盖明确的子集；例如，当前不模拟 USB 高速模式和集线器，也不模拟 PCIe PHY、通用流控和交换机。I2C 没有通用的枚举流程，因此 `scan` 会探测未保留的 7 位地址范围，并报告应答的设备。

## 构建

HPE 为 Linux 和 Windows 提供原生构建引导程序。请在项目根目录（即包含 `README.md` 的目录）中运行构建命令。首次构建需要联网下载固定版本并通过 SHA-256 校验的工具链和依赖归档文件。源码准备好之后，构建不要求预先安装编译器、Go、Python、.NET SDK、CMake、Ninja、Git 或包管理器。`build` 命令会自动下载并安装所需的本地构建工具；无需预先单独运行 `tools`。

### 获取项目源码

Linux 下使用 Git 克隆项目：

```sh
git clone https://github.com/Elrond-Xie/HardwareProtocolEmulation.git
cd HardwareProtocolEmulation
./bootstrap/bin/hpe-bootstrap-linux-x64 build
```

Windows 下可在[项目 GitHub 页面](https://github.com/Elrond-Xie/HardwareProtocolEmulation)点击 **Code → Download ZIP** 获取源码，再使用文件资源管理器解压。然后在解压后的项目目录打开**命令提示符**，执行下方 Windows 构建章节中的命令。该 ZIP 不含 Git 历史，更新时重新获取一份 ZIP 即可。无需自行查找或下载编译器 SDK、依赖包；引导程序会在构建期间自动下载并校验。Windows ARM64 请使用 `hpe-bootstrap-windows-arm64.exe`。

### Linux

```sh
# 在项目根目录中，Linux x86-64 执行：
./bootstrap/bin/hpe-bootstrap-linux-x64 build
```

Linux ARM64 请改为执行：

```sh
./bootstrap/bin/hpe-bootstrap-linux-arm64 build
```

如果只想下载并准备本地工具链、不构建 HPE，将 `build` 替换为 `tools`。如需限制并行编译任务数，请在构建前于当前终端设置 `HPE_JOBS`：

```sh
export HPE_JOBS=8
./bootstrap/bin/hpe-bootstrap-linux-x64 build
```

`HPE_JOBS` 是可选项；默认使用检测到的 CPU 核心数。也可以只对单次命令设置：`HPE_JOBS=8 ./bootstrap/bin/hpe-bootstrap-linux-x64 build`。

### Windows

打开 **命令提示符**（`cmd.exe`），切换到项目根目录，然后运行对应主机架构的引导程序：

```cmd
cd /d C:\path\to\Hardware_Protocol_Emulation
bootstrap\bin\hpe-bootstrap-windows-x64.exe build
```

Windows ARM64 请改用 `hpe-bootstrap-windows-arm64.exe`。如需限制并行编译任务数，请在构建前于当前命令提示符会话中设置 `HPE_JOBS`：

```cmd
set HPE_JOBS=8
bootstrap\bin\hpe-bootstrap-windows-x64.exe build
```

`HPE_JOBS` 是可选项；默认使用检测到的 CPU 核心数。执行 `set HPE_JOBS=` 可在当前命令提示符会话中清除该设置。构建目标是当前主机的操作系统和架构；引导程序不会为其他平台交叉编译应用程序。

引导程序会将编译器 SDK 下载并解压到 `build/toolchains/`。下载时会显示进度、已下载大小和当前速度。下载的构建工具、依赖源码、编译出的依赖和中间文件位于 `build/`。最终应用程序及运行文件输出到 `install/<RID>/`，例如 `install/linux-x64/` 或 `install/win-x64/`。构建支持增量编译，并会复用已校验的下载文件、CMake/Ninja 构建目录、ccache 条目、vcpkg 包和 NuGet 包。Linux 主机已安装 Git 时，引导程序会浅层拉取固定提交的 vcpkg 和 ccache 源码，校验提交号后删除 Git 元数据；Git 不可用或拉取失败时，会自动下载并校验固定 SHA-256 的 tar.gz 归档。Windows 则由引导程序直接下载并校验 ZIP 归档，不需要 Git for Windows。其他依赖源码同样由引导程序在构建过程中自动获取并校验。

默认使用检测到的全部 CPU 核心。可通过 `HPE_JOBS` 限制并行任务数，也可以设置以下路径：

| 变量 | 用途 | 默认值 |
| --- | --- | --- |
| `HPE_JOBS` | 最大并行构建任务数 | 检测到的 CPU 核心数 |
| `HPE_BUILD_ROOT` | 下载的构建工具、依赖、缓存和中间文件目录 | `<项目目录>/build` |
| `HPE_TOOL_ROOT` | 编译器和 SDK 安装目录 | `<项目目录>/build/toolchains` |
| `HPE_INSTALL_ROOT` | 已安装的应用程序目录 | `install/<RID>` |
| `HPE_LOG_DIR` | 构建流程日志目录 | `<HPE_BUILD_ROOT>/logs` |

正常构建无需设置任何环境变量。表格中的变量均为可选项。如需使用自定义路径，例如在 Linux 上：

```sh
export HPE_BUILD_ROOT="$PWD/build-cache"
export HPE_TOOL_ROOT="$PWD/compiler-sdks"
export HPE_INSTALL_ROOT="$PWD/package"
export HPE_LOG_DIR="$PWD/build-logs"
./bootstrap/bin/hpe-bootstrap-linux-x64 build
```

在 Windows 命令提示符中使用 Windows 绝对路径和 `set`：

```cmd
set HPE_BUILD_ROOT=C:\hpe-data\build
set HPE_TOOL_ROOT=C:\hpe-data\compiler-sdks
set HPE_INSTALL_ROOT=C:\hpe-data\install
set HPE_LOG_DIR=C:\hpe-data\logs
bootstrap\bin\hpe-bootstrap-windows-x64.exe build
```

这些设置只对当前终端会话生效。省略这些设置即可使用上表中的默认路径。

Linux 仍需要主机内核和兼容的系统 C 库。运行 Linux GUI 还需要可用的图形会话，例如 X11、Wayland 或 WSLg。Windows 使用系统 API。项目会自带应用运行时和构建所需依赖，但不会替代主机操作系统。

## 构建日志与清理

`tools`、`build` 和 `clean` 命令会记录完整流程日志，同时在终端显示进度。日志包含步骤名称、命令及工作目录、下载和校验结果、缓存复用情况、编译器输出、错误和耗时。每个配置的日志目录最多保留最新 10 份，较旧日志会自动删除。`distclean` 执行期间会临时记录日志：成功后删除整个 `build` 目录并丢弃本次日志；失败时则保留日志用于排查。

```sh
# Linux
./bootstrap/bin/hpe-bootstrap-linux-x64 clean
./bootstrap/bin/hpe-bootstrap-linux-x64 distclean
```

```cmd
:: Windows 命令提示符
bootstrap\bin\hpe-bootstrap-windows-x64.exe clean
bootstrap\bin\hpe-bootstrap-windows-x64.exe distclean
```

`clean` 会删除构建中间文件和已安装的 HPE 程序，同时保留下载归档、工具链、依赖源码、工具安装目录、包和编译器缓存以及日志。`distclean` 会删除整个 `build` 目录，包括下载内容、缓存和旧日志。本次 `distclean` 日志在清理成功后丢弃，清理失败时保留用于分析。命令开始时会显示临时日志路径；若失败，结束时会再次显示保留的日志路径。

## 运行

构建完成后，从对应平台的安装目录运行 CLI 或 GUI。例如，在 Linux 上：

```sh
./install/linux-x64/hpe-cli --demo --trace i2c-trace.txt
./install/linux-x64/hpe-cli \
  --scenario install/linux-x64/examples/i2c/i2c_basic.hpe \
  --trace i2c-trace.txt
./install/linux-x64/hpe-gui
```

Windows 对应程序为 `install\win-x64\hpe-cli.exe` 和 `install\win-x64\hpe-gui.cmd`（ARM64 使用 `win-arm64`）。所有支持协议的示例场景都安装在 `install/<RID>/examples/` 下。

CLI 会输出解码后的事件和设备结果。可选的跟踪文件会记录带有虚拟时间戳的协议事件。GUI 提供场景选择和运行、解码事件列表、可缩放和平移的时序图、可移动光标、可选进制的信号值（二进制、八进制、十进制和十六进制）、英文与简体中文设置，以及虚拟设备寄存器和内存的实时查看。

## 场景文件格式

场景文件使用 UTF-8 编码。空行和以 `#` 开头的行会被忽略。数值参数支持十进制或 `0x` 开头的十六进制。下面是一个简单的 I2C 示例：

```text
device eeprom 0x50 256 0xFF
device temperature_sensor 0x48 16 0x00
set 0x48 0x00 0x5A
scan
readreg 0x48 0x00 1
```

场景可以创建虚拟设备、初始化寄存器或存储、执行协议事务，并触发部分异常路径。安装目录中的示例覆盖 I2C、SPI、SD/eMMC、USB 和 PCIe。

## 更新构建引导程序

仓库中已包含 Linux 和 Windows 的原生引导程序，普通构建无需安装 Go。只有引导程序源码或其 Go 版本更新时，维护者才需要重新生成四个平台的引导程序。更新脚本会下载固定版本 Go 1.27.2 并校验 SHA-256，编译器、下载归档、模块缓存和编译缓存均保存在 `build/` 下。

```sh
# Linux 或其他 POSIX shell
./scripts/update-bootstrap.sh
```

```cmd
:: Windows 命令提示符
scripts\update-bootstrap.bat
```

## C API

公共 C API 声明位于 [`include/hpe/hpe.h`](include/hpe/hpe.h)。应用程序可以创建模拟上下文、添加虚拟设备、调用协议操作并查看跟踪事件。不透明模拟上下文会持有设备和事件，直到调用 `hpe_sim_destroy`。模拟时间是确定性的虚拟时间，API 调用不会等待现实时间。I2C 设备可以提供寄存器读写回调。

## 许可证

本项目自有代码采用 [PolyForm Noncommercial License 1.0.0](https://polyformproject.org/licenses/noncommercial/1.0.0) 授权，详见 [`LICENSE.txt`](LICENSE.txt)。该许可证允许免费以非商业目的使用、修改和再分发；商业用途需要另行取得版权持有者授权。这属于“源码可见”授权，不是 OSI 认可的开源许可证，因为它限制了特定领域的使用。许可证明确允许教育、慈善、公共研究、公共安全或卫生、环境保护及政府机构按非商业用途使用。

第三方组件、子模块、字体和下载的依赖仍遵循各自的许可证和声明；本项目许可证不会替代这些条款。构建生成的安装包也会包含本项目的许可证声明。

## 项目目录

```text
apps/cli/              C 命令行程序
apps/gui/Hpe.Gui/      Avalonia 桌面应用
adapters/gui-c-api/    GUI 使用的 C API 桥接层
include/hpe/           公共 C API
src/                   模拟引擎、设备和场景运行器
protocols/             I2C、SPI、SD/eMMC、USB 和 PCIe 模型
examples/              可运行的协议场景
tests/integration/     集成测试
assets/                字体和应用图稿
cmake/                 CMake 工具链和 vcpkg 配置
bootstrap/             原生 Go 构建引导程序源码和可执行文件
scripts/               用于重新生成引导程序的维护脚本
third_party/           可选的上游源码子模块
build/                 依赖、缓存和构建中间文件（生成目录）
install/<RID>/         面向用户的应用程序包
```
