#!/bin/sh
# Copy the matching Dream kernel modules from the openATV image or vendor package.
set -eu
test "$#" = 2 || { echo "Usage: driver-pack.sh ROOTFS_OR_EXTRA OUTPUT_ROOT" >&2; exit 1; }
model=${ODR_MODEL:-dm920}
release=3.14-1.17-$model
chip=bcm7439
abi="ARMv7 p2v8"
files="stb-core.ko lcd.ko dreambox_rc2.ko lnb_ctrl.ko tu1216.ko cu1216mk3.ko
	alps_bsbe1.ko alps_bsbe1_c01a.ko alps_bsbe2.ko bcm4506.ko cxd1981.ko
	nims/si2166b/si2166b.ko nims/si2183/si2183.ko"
case "$model" in
	dm900) version=20200226 ;;
	dm920) version=20200321 ;;
	dm7080|dm820)
		version=20190502
		if [ "$model" = dm820 ]; then version=20181018; fi
		release=3.4-4.0-$model; chip=bcm7435; abi="BMIPS 32BIT"; files="$files fp_hwmon.ko" ;;
	dm520)
		version=20180222; release=3.4-4.0-dm520; chip=bcm73625; abi="BMIPS 32BIT"
		files="stb-core.ko dreambox_rc2.ko lnb_ctrl.ko nims/atbm781x/atbm781x.ko" ;;
	*) exit 1 ;;
esac
src=$1
mkdir -p "$2"
out=$(cd "$2" && pwd)
if [ -d "$src/lib/modules/$release/extra" ]; then
	src="$src/lib/modules/$release/extra"
fi
modules="$out/lib/modules/$release"
test "$(modinfo -F version "$src/$chip.ko")" = "$version-$model"
test ! -e "$modules/extra/bcmdriver.ko" || { echo "Output contains the incompatible Nexus driver" >&2; exit 1; }
mkdir -p "$modules/extra"
cd "$src"
for file in $files "$chip.ko"; do
	case "$(modinfo -F vermagic "$file")" in
		"$release SMP mod_unload $abi"*) ;;
		*) echo "Unsupported module ABI: $file" >&2; exit 1 ;;
	esac
	install -D -m 0644 "$file" "$modules/extra/$file"
done
install -m 0644 LICENSE "$modules/extra/LICENSE"
# No in-tree loadable modules in this initramfs. The named dependencies
# of bcm7439 are all supplied above; the remainder are kernel builtins.
: > "$modules/modules.order"
: > "$modules/modules.builtin"
: > "$modules/modules.builtin.modinfo"
depmod -b "$out" "$release"
modprobe -d "$out" -S "$release" --show-depends "$chip" >/dev/null
