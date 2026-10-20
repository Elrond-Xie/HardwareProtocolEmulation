#!/usr/bin/env bash
# File: update-bootstrap.sh
# Purpose: Rebuild the checked-in Linux and Windows bootstrap executables.
# Author: Elrond-Xie
# Copyright (c) 2026 Elrond-Xie. SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0.

set -euo pipefail

script_directory="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
project_root="$(cd -- "${script_directory}/.." && pwd)"
bootstrap_directory="${project_root}/bootstrap"
build_root="${HPE_BUILD_ROOT:-${project_root}/build}"
go_version="1.27.2"
go_archive=""
go_sha256=""

case "$(uname -s)/$(uname -m)" in
    Linux/x86_64)
        go_archive="go${go_version}.linux-amd64.tar.gz"
        go_sha256="ecbadb99091a3f46e31f5f934b068b1864eafa7995211b39eaddf76996045fe5"
        ;;
    Linux/aarch64|Linux/arm64)
        go_archive="go${go_version}.linux-arm64.tar.gz"
        go_sha256="94f3e30b8e374bc285e7dadc11e0865726b9bc6e85b841ccceaabc0214c6b7c8"
        ;;
    *)
        printf 'Unsupported bootstrap update host: %s/%s\n' "$(uname -s)" "$(uname -m)" >&2
        exit 1
        ;;
esac

mkdir -p "${build_root}"
build_root="$(cd -- "${build_root}" && pwd)"
download_directory="${build_root}/downloads/bootstrap"
toolchain_directory="${build_root}/bootstrap-toolchain"
go_directory="${toolchain_directory}/go-${go_version}"
go_binary="${go_directory}/bin/go"
archive_path="${download_directory}/${go_archive}"
marker_path="${go_directory}/.hpe-go-sha256"

mkdir -p "${download_directory}" "${toolchain_directory}" "${bootstrap_directory}/bin"

download_go()
{
    local temporary_path="${archive_path}.part"
    local actual_sha256

    if [[ -f "${archive_path}" ]]; then
        actual_sha256="$(sha256sum "${archive_path}" | awk '{print $1}')"
        if [[ "${actual_sha256}" == "${go_sha256}" ]]; then
            printf 'Reusing verified Go archive: %s\n' "${archive_path}"
            return
        fi
        rm -f "${archive_path}"
    fi

    printf 'Downloading Go %s for bootstrap maintenance.\n' "${go_version}"
    curl --fail --location --retry 3 --output "${temporary_path}" "https://go.dev/dl/${go_archive}"
    actual_sha256="$(sha256sum "${temporary_path}" | awk '{print $1}')"
    if [[ "${actual_sha256}" != "${go_sha256}" ]]; then
        rm -f "${temporary_path}"
        printf 'Go archive SHA-256 mismatch: expected %s, got %s\n' "${go_sha256}" "${actual_sha256}" >&2
        exit 1
    fi
    mv "${temporary_path}" "${archive_path}"
}

prepare_go()
{
    local installed_sha256=""
    local staging_directory="${toolchain_directory}/go-${go_version}.staging"

    if [[ -f "${marker_path}" ]]; then
        installed_sha256="$(<"${marker_path}")"
    fi
    if [[ -x "${go_binary}" && "${installed_sha256}" == "${go_sha256}" ]] && "${go_binary}" version | grep -q "go${go_version}"; then
        printf 'Reusing verified Go compiler: %s\n' "${go_binary}"
        return
    fi

    rm -rf "${go_directory}" "${staging_directory}"
    mkdir -p "${staging_directory}"
    tar -xzf "${archive_path}" --strip-components=1 -C "${staging_directory}"
    if [[ ! -x "${staging_directory}/bin/go" ]] || ! "${staging_directory}/bin/go" version | grep -q "go${go_version}"; then
        rm -rf "${staging_directory}"
        printf 'The downloaded Go archive did not contain the expected compiler.\n' >&2
        exit 1
    fi
    mv "${staging_directory}" "${go_directory}"
    printf '%s\n' "${go_sha256}" >"${marker_path}"
}

build_bootstrap()
{
    local target_os="$1"
    local target_arch="$2"
    local output_name="$3"

    printf 'Building %s/%s: %s\n' "${target_os}" "${target_arch}" "${output_name}"
    CGO_ENABLED=0 GOOS="${target_os}" GOARCH="${target_arch}" \
        "${go_binary}" build -trimpath -ldflags='-s -w' -o "${bootstrap_directory}/bin/${output_name}" .
}

download_go
prepare_go
export GOTOOLCHAIN=local
export GOMODCACHE="${build_root}/bootstrap-mod-cache"
export GOCACHE="${build_root}/bootstrap-go-cache"
mkdir -p "${GOMODCACHE}" "${GOCACHE}"

cd "${bootstrap_directory}"
"${go_binary}" mod download

build_bootstrap linux amd64 hpe-bootstrap-linux-x64
build_bootstrap linux arm64 hpe-bootstrap-linux-arm64
build_bootstrap windows amd64 hpe-bootstrap-windows-x64.exe
build_bootstrap windows arm64 hpe-bootstrap-windows-arm64.exe

chmod +x \
    "${bootstrap_directory}/bin/hpe-bootstrap-linux-x64" \
    "${bootstrap_directory}/bin/hpe-bootstrap-linux-arm64" \
    "${bootstrap_directory}/bin/hpe-bootstrap-windows-x64.exe" \
    "${bootstrap_directory}/bin/hpe-bootstrap-windows-arm64.exe"

printf 'Bootstrap executables updated in %s/bin.\n' "${bootstrap_directory}"
