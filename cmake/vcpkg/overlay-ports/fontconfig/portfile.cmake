vcpkg_from_gitlab(
    GITLAB_URL https://gitlab.freedesktop.org
    OUT_SOURCE_PATH SOURCE_PATH
    REPO fontconfig/fontconfig
    REF ${VERSION}
    SHA512 daa6d1e6058e12c694d9e1512e09be957ff7f3fa375246b9d13eb0a8cf2f21e1512a5cabe93f270e96790e2c20420bf7422d213e43ab9749da3255286ea65a7c
    HEAD_REF master
)

set(options "")
if("iconv" IN_LIST FEATURES)
    list(APPEND options "-Diconv=enabled")
else()
    list(APPEND options "-Diconv=disabled")
endif()
if("nls" IN_LIST FEATURES)
    list(APPEND options "-Dnls=enabled")
else()
    list(APPEND options "-Dnls=disabled")
endif()
if("tools" IN_LIST FEATURES)
    list(APPEND options "-Dtools=enabled")
else()
    list(APPEND options "-Dtools=disabled")
endif()

vcpkg_configure_meson(
    SOURCE_PATH "${SOURCE_PATH}"
    OPTIONS
        ${options}
        -Ddoc=disabled
        -Dcache-build=disabled
        -Dtests=disabled
        -Dc_args=-O2
    ADDITIONAL_BINARIES
        "gperf = ['${HPE_GPERF}${VCPKG_HOST_EXECUTABLE_SUFFIX}']"
)

# The project compiler wrapper preprocesses Fontconfig's legacy gperf input
# before generating its object-name lookup table. Keep this port revisioned
# with the wrapper's preprocessing contract: the generated table must contain
# lowercase public names such as "family", not FC_FAMILY macro identifiers.

# https://www.freedesktop.org/software/fontconfig/fontconfig-user.html
# Adding OPTIONS for e.g. baseconfig-dir etc. won't work since meson will try to install into those dirs!
# Since adding OPTIONS does not work use a replacement in the generated config.h instead
set(replacement "")
if(VCPKG_TARGET_IS_WINDOWS)
    set(replacement "**invalid-fontconfig-dir-do-not-use**")
endif()
set(configfile "${CURRENT_BUILDTREES_DIR}/${TARGET_TRIPLET}-rel/config.h")
vcpkg_replace_string("${configfile}" "${CURRENT_PACKAGES_DIR}" "${replacement}")
if(NOT VCPKG_BUILD_TYPE)
    set(configfile "${CURRENT_BUILDTREES_DIR}/${TARGET_TRIPLET}-dbg/config.h")
    vcpkg_replace_string("${configfile}" "${CURRENT_PACKAGES_DIR}/debug" "${replacement}")
endif()

vcpkg_install_meson(ADD_BIN_TO_PATH)

vcpkg_copy_pdbs()
#Fix missing libintl static dependency
if("nls" IN_LIST FEATURES AND VCPKG_TARGET_IS_WINDOWS AND NOT VCPKG_TARGET_IS_MINGW)
    if(NOT VCPKG_BUILD_TYPE)
        vcpkg_replace_string("${CURRENT_PACKAGES_DIR}/debug/lib/pkgconfig/fontconfig.pc" "-liconv" "-liconv -lintl" IGNORE_UNCHANGED)
    endif()
    vcpkg_replace_string("${CURRENT_PACKAGES_DIR}/lib/pkgconfig/fontconfig.pc" "-liconv" "-liconv -lintl" IGNORE_UNCHANGED)
endif()
vcpkg_fixup_pkgconfig()

# Fix paths in debug pc file.
if(NOT VCPKG_BUILD_TYPE)
    set(fontconfig_pc_debug "${CURRENT_PACKAGES_DIR}/debug/lib/pkgconfig/fontconfig.pc")
    vcpkg_replace_string("${fontconfig_pc_debug}" "/etc" "/../etc" REGEX)
    vcpkg_replace_string("${fontconfig_pc_debug}" "/var" "/../var" REGEX)
endif()

# Make path to cache in fonts.conf relative
set(_file "${CURRENT_PACKAGES_DIR}/etc/fonts/fonts.conf")
if(EXISTS "${_file}")
    vcpkg_replace_string("${_file}" "${CURRENT_PACKAGES_DIR}/var/cache/fontconfig" "./../../var/cache/fontconfig" IGNORE_UNCHANGED)
endif()

file(REMOVE_RECURSE "${CURRENT_PACKAGES_DIR}/debug/var"
                    "${CURRENT_PACKAGES_DIR}/debug/share"
                    "${CURRENT_PACKAGES_DIR}/debug/etc"
                    "${CURRENT_PACKAGES_DIR}/var")

if("tools" IN_LIST FEATURES)
    vcpkg_copy_tools(
        TOOL_NAMES fc-match fc-cat fc-list fc-pattern fc-query fc-scan fc-cache fc-validate fc-conflist
        AUTO_CLEAN
    )
endif()

configure_file("${CMAKE_CURRENT_LIST_DIR}/vcpkg-cmake-wrapper.cmake.in" "${CURRENT_PACKAGES_DIR}/share/${PORT}/vcpkg-cmake-wrapper.cmake" @ONLY)
file(INSTALL "${CMAKE_CURRENT_LIST_DIR}/usage" DESTINATION "${CURRENT_PACKAGES_DIR}/share/${PORT}")
vcpkg_install_copyright(FILE_LIST "${SOURCE_PATH}/COPYING")
