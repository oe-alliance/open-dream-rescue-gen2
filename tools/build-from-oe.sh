#!/bin/sh
# Resolve one model against an already configured OpenATV kernel workspace.
set -eu
repo=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
: "${OE_TMP:?Set the absolute OpenATV tmp directory}"
: "${ODR_MODEL:?Choose dm520, dm7080, dm820, dm900 or dm920}"
case "$ODR_MODEL" in
    dm520|dm7080|dm820)
        work="$OE_TMP/work/$ODR_MODEL-oe-linux/linux-dreambox/3.4"
        source="$OE_TMP/work-shared/$ODR_MODEL/kernel-source"
        config="$work/build/.config"
        sysroot="$OE_TMP/sysroots-components/mips32el/libgcc"
        factory="vmlinux.gz-rescue-3.4-r1-$ODR_MODEL-20201201.bin"
        ;;
    dm900|dm920)
        work="$OE_TMP/work/dm920-oe-linux-gnueabi/linux-dreambox/3.14"
        source="$OE_TMP/work-shared/dm7252s/kernel-source"
        config="$repo/configs/$ODR_MODEL/defconfig"
        sysroot="$OE_TMP/sysroots-components/cortexa15hf-neon-vfpv4/libgcc"
        if [ "$ODR_MODEL" = dm900 ]; then factory=zImage-rescue-3.14-r0-dm900-20201201.bin
        else factory=zImage-rescue-dm920-testbox.bin; fi
        ;;
    *) echo "Unknown model: $ODR_MODEL" >&2; exit 1 ;;
esac
export ODR_KERNEL_WORK="${ODR_KERNEL_WORK:-$work}"
export ODR_KERNEL_SOURCE="${ODR_KERNEL_SOURCE:-$source}"
export ODR_KERNEL_CONFIG="${ODR_KERNEL_CONFIG:-$config}"
export ODR_APP_SYSROOT="${ODR_APP_SYSROOT:-$sysroot}"
export ODR_FACTORY="${ODR_FACTORY:-$repo/vendor/factory/$factory}"
exec sh "$repo/tools/build.sh"
