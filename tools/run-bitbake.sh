#!/bin/sh
set -eu
: "${BITBAKE_ROOT:?Set the BitBake source directory}" "${ODR_BUILD:?Set a fresh absolute BitBake build directory}"
: "${OE_TMP:?Set the absolute OpenATV tmp directory}" "${ODR_MODEL:?Set the target model}"
: "${ODR_DRIVER_DIR:?Set the matching Dream driver directory}" "${ODR_MUSL:?Set the musl archive}"
case "$ODR_BUILD" in /*) ;; *) echo 'ODR_BUILD must be absolute' >&2; exit 1 ;; esac
ODR_SOURCE=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
mkdir -p "$ODR_BUILD"
test -z "$(ls -A "$ODR_BUILD")" || { echo 'Choose an empty ODR_BUILD' >&2; exit 1; }
export ODR_SOURCE BBPATH="$ODR_BUILD:$ODR_SOURCE" PATH="$BITBAKE_ROOT/bin:$PATH"
export BB_ENV_PASSTHROUGH_ADDITIONS="ODR_SOURCE ODR_MODEL OE_TMP ODR_DRIVER_DIR ODR_MUSL ODR_FACTORY ODR_KERNEL_WORK ODR_KERNEL_SOURCE ODR_KERNEL_CONFIG ODR_APP_SYSROOT JOBS"
cd "$ODR_BUILD"
exec bitbake odr-gen2
