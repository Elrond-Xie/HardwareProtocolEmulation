# File: hpe-windows.cmake
# Purpose: Configure dependency builds for the Windows target.
# Author: Elrond-Xie
# Copyright (c) 2026 Elrond-Xie. SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0.

if(DEFINED ENV{HPE_VCPKG_ARCH})
    set(VCPKG_TARGET_ARCHITECTURE "$ENV{HPE_VCPKG_ARCH}")
else()
    set(VCPKG_TARGET_ARCHITECTURE x64)
endif()
set(VCPKG_CMAKE_SYSTEM_NAME MinGW)
set(VCPKG_CRT_LINKAGE dynamic)
set(VCPKG_LIBRARY_LINKAGE dynamic)
set(VCPKG_BUILD_TYPE release)
set(VCPKG_ENV_PASSTHROUGH PATH HPE_ZIG HPE_CC HPE_CXX HPE_CCACHE CCACHE_DIR CCACHE_BASEDIR CCACHE_COMPILERCHECK)
set(VCPKG_CHAINLOAD_TOOLCHAIN_FILE "${CMAKE_CURRENT_LIST_DIR}/../../toolchains/zig.cmake")
