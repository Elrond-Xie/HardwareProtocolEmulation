cmake_minimum_required(VERSION 3.16)
project(hpe VERSION 0.1.0 LANGUAGES C)

include(CTest)

set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)
set(CMAKE_C_EXTENSIONS OFF)
if(CMAKE_BUILD_TYPE STREQUAL "Release")
    string(APPEND CMAKE_C_FLAGS_RELEASE " -g0")
endif()
if(CMAKE_INSTALL_PREFIX_INITIALIZED_TO_DEFAULT)
    set(CMAKE_INSTALL_PREFIX "${CMAKE_SOURCE_DIR}/install" CACHE PATH "Project-local install output" FORCE)
endif()

add_library(hpe_core SHARED
    src/engine/sim.c
    protocols/i2c/i2c.c
    protocols/spi/spi.c
    protocols/sdmmc/card.c
    protocols/usb/usb.c
    protocols/pcie/pcie.c
    src/devices/register_device.c
    src/scenario/scenario.c)
target_include_directories(hpe_core PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/include)
target_include_directories(hpe_core PRIVATE
    ${CMAKE_CURRENT_SOURCE_DIR}/src
    ${CMAKE_CURRENT_SOURCE_DIR}/src/engine)
target_compile_definitions(hpe_core PRIVATE HPE_BUILD_SHARED)
set_target_properties(hpe_core PROPERTIES
    VERSION ${PROJECT_VERSION}
    SOVERSION ${PROJECT_VERSION_MAJOR}
    INSTALL_RPATH "$ORIGIN")
if(MSVC)
    target_compile_options(hpe_core PRIVATE /W4)
else()
    target_compile_options(hpe_core PRIVATE -Wall -Wextra -Wpedantic)
endif()

add_executable(hpe-cli apps/cli/main.c)
target_compile_definitions(hpe-cli PRIVATE HPE_USE_SHARED)
target_link_libraries(hpe-cli PRIVATE hpe_core)
set_target_properties(hpe-cli PROPERTIES INSTALL_RPATH "$ORIGIN/lib")
if(CMAKE_BUILD_TYPE STREQUAL "Release" AND NOT MSVC)
    target_link_options(hpe-cli PRIVATE -Wl,--strip-debug)
endif()

if(BUILD_TESTING)
    add_executable(hpe-protocol-tests tests/integration/test_protocols.c)
    target_compile_definitions(hpe-protocol-tests PRIVATE HPE_USE_SHARED)
    target_link_libraries(hpe-protocol-tests PRIVATE hpe_core)
    add_test(NAME hpe_protocol_models COMMAND hpe-protocol-tests)
endif()

install(TARGETS hpe-cli RUNTIME DESTINATION .)
install(DIRECTORY examples/ DESTINATION examples)
install(TARGETS hpe_core
    RUNTIME DESTINATION gui
    LIBRARY DESTINATION lib
    ARCHIVE DESTINATION "${CMAKE_BINARY_DIR}/import-libs")
if(WIN32)
    # The GUI and its bridge live in install/gui, while the CLI is installed
    # at the install root. Keep a copy of the core DLL beside hpe-cli.exe too.
    install(FILES "$<TARGET_FILE:hpe_core>" DESTINATION .)
endif()

add_library(hpe_gui_bridge SHARED adapters/gui-c-api/bridge.c)
target_include_directories(hpe_gui_bridge PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/adapters/gui-c-api)
target_compile_definitions(hpe_gui_bridge PRIVATE HPE_USE_SHARED)
target_link_libraries(hpe_gui_bridge PRIVATE hpe_core)
set_target_properties(hpe_gui_bridge PROPERTIES
    OUTPUT_NAME hpe_gui_bridge
    INSTALL_RPATH "$ORIGIN/../lib")
install(TARGETS hpe_gui_bridge RUNTIME DESTINATION gui LIBRARY DESTINATION gui
    ARCHIVE DESTINATION "${CMAKE_BINARY_DIR}/import-libs")
install(FILES LICENSE.txt DESTINATION .)

# Linux native libraries used by Avalonia's X11/Skia backends are packaged under install/lib.
if(VCPKG_INSTALLED_DIR AND VCPKG_TARGET_TRIPLET)
    set(HPE_VCPKG_TARGET_DIR "${VCPKG_INSTALLED_DIR}/${VCPKG_TARGET_TRIPLET}")
    if(WIN32)
        install(DIRECTORY "${HPE_VCPKG_TARGET_DIR}/bin/" DESTINATION .
            FILES_MATCHING PATTERN "*.dll")
    else()
        install(DIRECTORY "${HPE_VCPKG_TARGET_DIR}/lib/" DESTINATION lib
            FILES_MATCHING PATTERN "pkgconfig" EXCLUDE
            PATTERN "*.so" PATTERN "*.so.*")
    endif()
endif()
