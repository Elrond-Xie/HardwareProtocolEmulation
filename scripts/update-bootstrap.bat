@echo off
rem File: update-bootstrap.bat
rem Purpose: Rebuild the checked-in bootstrap tools and GUI runtime launchers.
rem Author: Elrond-Xie
rem Copyright (c) 2026 Elrond-Xie. SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0.

setlocal EnableExtensions
set "SCRIPT_DIRECTORY=%~dp0"
for %%I in ("%SCRIPT_DIRECTORY%..") do set "PROJECT_ROOT=%%~fI"
set "BOOTSTRAP_DIRECTORY=%PROJECT_ROOT%\bootstrap"
set "LAUNCHER_DIRECTORY=%PROJECT_ROOT%\apps\gui\launcher"
if not defined HPE_BUILD_ROOT set "HPE_BUILD_ROOT=%PROJECT_ROOT%\build"
for %%I in ("%HPE_BUILD_ROOT%") do set "HPE_BUILD_ROOT=%%~fI"
set "GO_VERSION=1.27.2"

if defined PROCESSOR_ARCHITEW6432 (
    set "HOST_ARCH=%PROCESSOR_ARCHITEW6432%"
) else (
    set "HOST_ARCH=%PROCESSOR_ARCHITECTURE%"
)

if /I "%HOST_ARCH%"=="AMD64" (
    set "GO_ARCH=amd64"
    set "GO_SHA256=1314008898bd40df77af4b014f777f08873dbdfbcd3d92308728ee03304fe04f"
) else if /I "%HOST_ARCH%"=="ARM64" (
    set "GO_ARCH=arm64"
    set "GO_SHA256=5c4b294f23b558619ae8a4aa94d023e3bca03f52c8398bb41b9026002b0013de"
) else (
    echo Unsupported bootstrap update architecture: %HOST_ARCH% 1>&2
    exit /b 1
)

set "GO_ARCHIVE=go%GO_VERSION%.windows-%GO_ARCH%.zip"
set "DOWNLOAD_DIRECTORY=%HPE_BUILD_ROOT%\downloads\bootstrap"
set "TOOLCHAIN_DIRECTORY=%HPE_BUILD_ROOT%\bootstrap-toolchain"
set "GO_DIRECTORY=%TOOLCHAIN_DIRECTORY%\go-%GO_VERSION%"
set "GO_BINARY=%GO_DIRECTORY%\bin\go.exe"
set "ARCHIVE_PATH=%DOWNLOAD_DIRECTORY%\%GO_ARCHIVE%"
set "MARKER_PATH=%GO_DIRECTORY%\.hpe-go-sha256"
set "STAGING_DIRECTORY=%TOOLCHAIN_DIRECTORY%\go-%GO_VERSION%.staging"

if not exist "%DOWNLOAD_DIRECTORY%" mkdir "%DOWNLOAD_DIRECTORY%"
if not exist "%TOOLCHAIN_DIRECTORY%" mkdir "%TOOLCHAIN_DIRECTORY%"
if not exist "%BOOTSTRAP_DIRECTORY%\bin" mkdir "%BOOTSTRAP_DIRECTORY%\bin"
if errorlevel 1 exit /b 1

call :download_go
if errorlevel 1 exit /b 1
call :prepare_go
if errorlevel 1 exit /b 1

set "GOTOOLCHAIN=local"
set "GOMODCACHE=%HPE_BUILD_ROOT%\bootstrap-mod-cache"
set "GOCACHE=%HPE_BUILD_ROOT%\bootstrap-go-cache"
if not exist "%GOMODCACHE%" mkdir "%GOMODCACHE%"
if not exist "%GOCACHE%" mkdir "%GOCACHE%"

pushd "%BOOTSTRAP_DIRECTORY%"
if errorlevel 1 exit /b 1
"%GO_BINARY%" mod download
if errorlevel 1 goto :failed

call :build_bootstrap linux amd64 hpe-bootstrap-linux-x64
if errorlevel 1 goto :failed
call :build_bootstrap linux arm64 hpe-bootstrap-linux-arm64
if errorlevel 1 goto :failed
call :build_bootstrap windows amd64 hpe-bootstrap-windows-x64.exe
if errorlevel 1 goto :failed
call :build_bootstrap windows arm64 hpe-bootstrap-windows-arm64.exe
if errorlevel 1 goto :failed
popd
pushd "%LAUNCHER_DIRECTORY%"
if errorlevel 1 exit /b 1
"%GO_BINARY%" mod download
if errorlevel 1 goto :failed
call :build_gui_launcher linux amd64 linux-x64 hpe-gui
if errorlevel 1 goto :failed
call :build_gui_launcher linux arm64 linux-arm64 hpe-gui
if errorlevel 1 goto :failed
call :build_gui_launcher windows amd64 win-x64 hpe-gui.exe
if errorlevel 1 goto :failed
call :build_gui_launcher windows arm64 win-arm64 hpe-gui.exe
if errorlevel 1 goto :failed
popd
echo Bootstrap executables updated in "%BOOTSTRAP_DIRECTORY%\bin".
echo GUI launchers updated in "%LAUNCHER_DIRECTORY%\bin".
exit /b 0

:download_go
set "EXPECTED_ARCHIVE=go%GO_VERSION%.windows-%GO_ARCH%.zip"
if exist "%ARCHIVE_PATH%" (
    call :verify_archive "%ARCHIVE_PATH%"
    if not errorlevel 1 exit /b 0
    del /q "%ARCHIVE_PATH%" >nul 2>nul
)

echo Downloading Go %GO_VERSION% for bootstrap maintenance.
curl.exe --fail --location --retry 3 --output "%ARCHIVE_PATH%.part" "https://go.dev/dl/%GO_ARCHIVE%"
if errorlevel 1 exit /b 1
call :verify_archive "%ARCHIVE_PATH%.part"
if errorlevel 1 (
    del /q "%ARCHIVE_PATH%.part" >nul 2>nul
    exit /b 1
)
move /y "%ARCHIVE_PATH%.part" "%ARCHIVE_PATH%" >nul
exit /b %errorlevel%

:verify_archive
set "ARCHIVE_TO_VERIFY=%~1"
set "ACTUAL_SHA256="
for /f "skip=1 tokens=1" %%H in ('certutil -hashfile "%ARCHIVE_TO_VERIFY%" SHA256') do if not defined ACTUAL_SHA256 set "ACTUAL_SHA256=%%H"
if /I not "%ACTUAL_SHA256%"=="%GO_SHA256%" (
    echo Go archive SHA-256 mismatch: expected %GO_SHA256%, got %ACTUAL_SHA256%. 1>&2
    exit /b 1
)
exit /b 0

:prepare_go
set "INSTALLED_SHA256="
if exist "%MARKER_PATH%" set /p "INSTALLED_SHA256="<"%MARKER_PATH%"
if exist "%GO_BINARY%" if /I "%INSTALLED_SHA256%"=="%GO_SHA256%" (
    for /f "tokens=3" %%V in ('"%GO_BINARY%" version') do if "%%V"=="go%GO_VERSION%" exit /b 0
)

if exist "%GO_DIRECTORY%" rmdir /s /q "%GO_DIRECTORY%"
if exist "%STAGING_DIRECTORY%" rmdir /s /q "%STAGING_DIRECTORY%"
mkdir "%STAGING_DIRECTORY%"
tar.exe -xf "%ARCHIVE_PATH%" -C "%STAGING_DIRECTORY%"
if errorlevel 1 exit /b 1
if not exist "%STAGING_DIRECTORY%\go\bin\go.exe" (
    echo The downloaded Go archive did not contain the expected compiler. 1>&2
    exit /b 1
)
for /f "tokens=3" %%V in ('"%STAGING_DIRECTORY%\go\bin\go.exe" version') do if not "%%V"=="go%GO_VERSION%" exit /b 1
move "%STAGING_DIRECTORY%\go" "%GO_DIRECTORY%" >nul
if errorlevel 1 exit /b 1
echo %GO_SHA256%>"%MARKER_PATH%"
exit /b %errorlevel%

:build_bootstrap
echo Building %1/%2: %3
set "CGO_ENABLED=0"
set "GOOS=%1"
set "GOARCH=%2"
"%GO_BINARY%" build -trimpath -ldflags="-s -w" -o "%BOOTSTRAP_DIRECTORY%\bin\%3" .
exit /b %errorlevel%

:build_gui_launcher
if /I "%1"=="windows" goto :build_windows_gui_launcher
echo Building %1/%2 GUI launcher: %3
set "CGO_ENABLED=0"
set "GOOS=%1"
set "GOARCH=%2"
"%GO_BINARY%" build -trimpath -ldflags="-s -w" -o "%LAUNCHER_DIRECTORY%\bin\%3\%4" .
exit /b %errorlevel%

:build_windows_gui_launcher
echo Embedding the Windows application icon for %3
set "GOOS=windows"
set "GOARCH=%GO_ARCH%"
"%GO_BINARY%" run github.com/akavel/rsrc -ico "%PROJECT_ROOT%\apps\gui\Hpe.Gui\Assets\hpe-icon.ico" -arch %2 -o "rsrc_windows_%2.syso"
if errorlevel 1 exit /b %errorlevel%
echo Building windows/%2 GUI launcher: %3
set "CGO_ENABLED=0"
set "GOOS=windows"
set "GOARCH=%2"
"%GO_BINARY%" build -trimpath -ldflags="-s -w -H=windowsgui" -o "%LAUNCHER_DIRECTORY%\bin\%3\%4" .
exit /b %errorlevel%

:failed
set "BUILD_ERROR=%errorlevel%"
popd
exit /b %BUILD_ERROR%
