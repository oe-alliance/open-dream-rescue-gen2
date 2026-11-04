#!/bin/sh
# Run in a separate output directory on the matching OpenATV builder.
set -eu
src=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
ODR_MODEL=${ODR_MODEL:-dm920}
case "$ODR_MODEL" in
	dm900|dm920)
		ARCH=arm; triple=arm-oe-linux-gnueabi; goarch=arm; musl_target=arm-linux-musleabihf
		arch="-march=armv7-a -mfpu=neon -mfloat-abi=hard"; limit=14417920 ;;
	dm520|dm820|dm7080)
		ARCH=mips; triple=mipsel-oe-linux; goarch=mipsle; musl_target=mipsel-linux-musl
		arch="-march=mips32 -mabi=32"; limit=15728640
		if [ "$ODR_MODEL" = dm520 ]; then limit=13631488; fi ;;
	*) echo "Unknown ODR model: $ODR_MODEL" >&2; exit 1 ;;
esac
export ODR_MODEL
upper=$(printf '%s' "$ODR_MODEL" | tr a-z A-Z)
work=${ODR_KERNEL_WORK:-${DM920_KERNEL_WORK:-}}
kernel_src=${ODR_KERNEL_SOURCE:-${DM920_KERNEL_SOURCE:-}}
driver_dir=${ODR_DRIVER_DIR:-${DM920_DRIVER_DIR:-}}
sysroot=${ODR_APP_SYSROOT:-${DM920_APP_SYSROOT:-}}
: "${work:?Set ODR_KERNEL_WORK to the configured kernel work directory}"
: "${kernel_src:?Set ODR_KERNEL_SOURCE to the patched kernel source directory}"
: "${driver_dir:?Set ODR_DRIVER_DIR to the matching Dream driver package}"
: "${sysroot:?Set ODR_APP_SYSROOT to the target sysroot with libgcc}"
config=${ODR_KERNEL_CONFIG:-$work/build/.config}
: "${ODR_OUT:?Set to a separate output directory}"
: "${ODR_FACTORY:?Set to the pinned factory rescue for this model}"
: "${ODR_MUSL:?Set to musl-1.2.5.tar.gz}"
case "$ODR_OUT" in /*) ;; *) echo 'ODR_OUT must be absolute' >&2; exit 1 ;; esac
mkdir -p "$ODR_OUT"
test -z "$(ls -A "$ODR_OUT")" || { echo 'Choose an empty ODR_OUT' >&2; exit 1; }
mkdir "$ODR_OUT/kernel"
out=$(cd "$ODR_OUT" && pwd)
oe_output=$(cd "$work/build" && pwd)
test "$out" != "$oe_output" && test "$(cd "$out/kernel" && pwd -P)" != "$oe_output" || exit 1
ns="$work/recipe-sysroot-native"
export PATH="$ns/usr/bin/$triple:$ns/usr/bin:$PATH"
export ARCH CROSS_COMPILE="$triple-"
grep -qx "CONFIG_DREAMBOX_${upper}=y" "$config"
grep -qx "CONFIG_LOCALVERSION=\"-$ODR_MODEL\"" "$config"
server="$out/odr-server"
(cd "$src/backend"; GO111MODULE=off go test -tags "$ODR_MODEL"; CGO_ENABLED=0 GO111MODULE=off GOOS=linux GOARCH="$goarch" GOARM=7 GOMIPS=softfloat go build -tags "$ODR_MODEL" -trimpath -ldflags='-s -w -buildid=' -o "$server" .)
echo "a9a118bbe84d8764da0ea0d28b3ab3fae8477fc7e4085d90102b8596fc7c75e4  $ODR_MUSL" | sha256sum -c -
cc="${CROSS_COMPILE}gcc"
if [ "$ARCH" = mips ]; then
	# Linux 3.4 predates GCC 15's switch to a C23 default.
	if [ "$("$cc" -dumpversion | cut -d. -f1)" -ge 15 ]; then
		export KCFLAGS="${KCFLAGS:+$KCFLAGS }-std=gnu17 -Wno-error=incompatible-pointer-types -Wno-error=int-conversion -Wno-error=address-of-packed-member"
	fi
fi
mkdir -p "$out/musl-src"
tar -xzf "$ODR_MUSL" --strip-components=1 -C "$out/musl-src"
(cd "$out/musl-src"; CC="$cc $arch" ./configure --target="$musl_target" --prefix=/usr --disable-shared --disable-wrapper; make -j"${JOBS:-12}"; make install DESTDIR="$out/musl")
gccrt=$("$cc" --sysroot="$sysroot" -print-libgcc-file-name)
test -f "$gccrt"
make -s -C "$kernel_src" O="$out/kernel" headers_install INSTALL_HDR_PATH="$out/uapi"
"$cc" $arch --sysroot="$out/musl" -idirafter "$out/uapi/include" \
	-Os -static -no-pie -nostdlib -s -Wall -Wextra -Werror -ffunction-sections -fdata-sections \
	-Wl,--gc-sections -o "$out/odr-hdmi" "$out/musl/usr/lib/crt1.o" "$src"/hdmi/*.c \
	-Wl,--start-group "$out/musl/usr/lib/libc.a" "$gccrt" -Wl,--end-group
if [ "$ODR_MODEL" = dm7080 ] || [ "$ODR_MODEL" = dm820 ]; then
	ofg="$src/reference/ofgwrite"
	"$cc" $arch --sysroot="$out/musl" -I"$ofg" -Os -static -no-pie -nostdlib -s -Wall -Wextra -Werror \
		"-DODR_BANK_MODEL=\"$ODR_MODEL\"" -o "$out/odr-bank" "$out/musl/usr/lib/crt1.o" "$src/tools/bank.c" "$ofg/dream_bootblob.c" "$ofg/dream_lcd.c" \
		-Wl,--start-group "$out/musl/usr/lib/libc.a" "$gccrt" -Wl,--end-group
fi
sh "$src/tools/driver-pack.sh" "$driver_dir" "$out/drivers"
go run "$src/tools/pack.go" "$ODR_FACTORY" "$server" "$out/odr-hdmi" "$out/drivers" "$src/hdmi/load-drivers.sh" "$src/tools/start-ssh.sh" "$out/odr.cpio"
xz -c -7 -T2 --check=crc32 "$out/odr.cpio" > "$out/odr.cpio.xz"
cp "$config" "$out/kernel/.config"
sed -i "s@^CONFIG_INITRAMFS_SOURCE=.*@CONFIG_INITRAMFS_SOURCE=\"$out/odr.cpio.xz\"@" "$out/kernel/.config"
if [ "$ARCH" = mips ]; then
	sed -i 's/^CONFIG_DVB_STV0299=m/CONFIG_DVB_STV0299=y/;s/^# CONFIG_MTD_CHAR is not set/CONFIG_MTD_CHAR=y/' "$out/kernel/.config"
	make -s -f Makefile -C "$kernel_src" O="$out/kernel" silentoldconfig </dev/null
	make -s -f Makefile -C "$kernel_src" O="$out/kernel" -j"${JOBS:-12}" vmlinux KBUILD_BUILD_USER="odr-$ODR_MODEL" KBUILD_BUILD_HOST=rescue
	"${CROSS_COMPILE}strip" -o "$out/rescue.elf" "$out/kernel/vmlinux"
	zimage="$out/rescue.elf.gz"
	gzip -n -9 -c "$out/rescue.elf" > "$zimage"
else
	make -s -C "$kernel_src" O="$out/kernel" olddefconfig
	make -s -C "$kernel_src" O="$out/kernel" -j"${JOBS:-12}" zImage KBUILD_BUILD_USER="odr-$ODR_MODEL" KBUILD_BUILD_HOST=rescue EXTRA_CFLAGS=-Wno-attribute-alias
	zimage="$out/kernel/arch/arm/boot/zImage"
fi
bytes=$(wc -c < "$zimage")
if [ "$bytes" -gt "$limit" ]; then
	echo "$ODR_MODEL rescue is $((bytes - limit)) bytes too large for the $limit-byte SPI partition" >&2
	exit 1
fi
echo "$ODR_MODEL rescue: $bytes of $limit bytes, $((limit - bytes)) bytes free"
(cd "$src/backend"; ODR_TEST_RESCUE="$zimage" ODR_TEST_NON_ODR="$ODR_FACTORY" GO111MODULE=off go test -tags "$ODR_MODEL" -run TestRescueBuildIdentity)
usb="ODR${ODR_MODEL#dm}.BIN"
cp "$zimage" "$out/$usb"
(cd "$out"; sha256sum "$usb" > "$usb.sha256")
digest=$(cut -d' ' -f1 "$out/$usb.sha256")
release="releases/download/dreambox-rescue-1.0-$ODR_MODEL"
file="odr-rescue-1.0-$ODR_MODEL-$(printf '%.12s' "$digest").bin"
mkdir -p "$out/bootmenu/$ODR_MODEL" "$out/bootmenu/$release"
cp "$zimage" "$out/bootmenu/$release/$file"
printf 'rescue 1.0 %s %s %s\n' "$bytes" "$digest" \
	"https://raw.githubusercontent.com/oe-alliance/bootmenu/master/$release/$file" > "$out/bootmenu/$ODR_MODEL/rescue"

