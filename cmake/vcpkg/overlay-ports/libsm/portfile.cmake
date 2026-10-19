# libSM expects the Xtrans headers in the normal include root, while vcpkg's
# xtrans port keeps these shared implementation headers under share/xtrans.
file(MAKE_DIRECTORY "${CURRENT_INSTALLED_DIR}/include/X11")
file(COPY "${CURRENT_INSTALLED_DIR}/share/xtrans/include/X11/Xtrans"
     DESTINATION "${CURRENT_INSTALLED_DIR}/include/X11")
get_filename_component(HPE_SOURCE_ROOT "${CMAKE_CURRENT_LIST_DIR}/../../../.." ABSOLUTE)
include("${HPE_SOURCE_ROOT}/third_party/vcpkg/ports/libsm/portfile.cmake")
