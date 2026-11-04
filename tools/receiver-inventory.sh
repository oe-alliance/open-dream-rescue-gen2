#!/bin/sh
# Record the running hardware and storage layout without mounting any volumes.
set -eu
model=$(cat /proc/stb/info/model)
case "$model" in
	dm520|dm525|dm520mini)
		[ "$(cat /proc/stb/info/mid)" = 21 ] || { echo "Unexpected $model hardware ID" >&2; exit 1; }
		profile=dm520 ;;
	dm820) profile=dm820 ;;
	*) echo "Unsupported receiver: $model" >&2; exit 1 ;;
esac
if [ "$#" -gt 0 ] && [ "$profile" != "$1" ] && [ "$model" != "$1" ]; then
	echo "Expected $1, found $model" >&2
	exit 1
fi
printf 'Open Dream Rescue - %s\nKernel: %s\n' "$model" "$(uname -r)"
for path in /proc/stb/info/mid /proc/cmdline /proc/mtd /proc/partitions /proc/mounts /proc/bus/input/devices; do
	if [ -r "$path" ]; then printf '\n%s\n' "$path"; cat "$path"; fi
done
printf '\nMTD geometry (bytes)\n'
for dir in /sys/class/mtd/mtd[0-9]*; do
	[ -r "$dir/type" ] || continue
	printf '%s %s type=%s size=%s erase=%s write=%s\n' "${dir##*/}" "$(cat "$dir/name")" "$(cat "$dir/type")" "$(cat "$dir/size")" "$(cat "$dir/erasesize")" "$(cat "$dir/writesize")"
done
if [ "$profile" = dm520 ]; then
	printf '\nInternal NAND (single image)\n'
	for dir in /sys/class/ubi/ubi[0-9]*; do
		[ -r "$dir/mtd_num" ] || continue
		printf '%s on mtd%s\n' "${dir##*/}" "$(cat "$dir/mtd_num")"
	done
fi
printf '\nUBI volumes\n'
for dir in /sys/class/ubi/ubi*_*; do
	[ -r "$dir/name" ] || continue
	printf '%s %s\n' "${dir##*/}" "$(cat "$dir/name")"
done
printf '\nBlock devices (512-byte sectors)\n'
for dir in /sys/class/block/mmcblk* /sys/class/block/sd*; do
	[ -r "$dir/size" ] || continue
	printf '%s size=%s' "${dir##*/}" "$(cat "$dir/size")"
	if [ -r "$dir/start" ]; then printf ' start=%s' "$(cat "$dir/start")"; fi
	printf '\n'
done
printf '\nRAM, network and display\n'
sed -n '/^MemTotal:/p' /proc/meminfo
for path in /sys/class/net/eth0/address /proc/stb/lcd/xres /proc/stb/lcd/yres /proc/stb/lcd/bpp /sys/class/graphics/fb0/name; do
	if [ -r "$path" ]; then printf '%s %s\n' "$path" "$(cat "$path")"; fi
done
