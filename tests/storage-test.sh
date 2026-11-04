#!/bin/bash
# Integration test on a Linux builder. Creates private file-backed devices only.
# Host binary must be statically linked. Inputs are unmodified release images.
set -euo pipefail
if [[ $# != 3 || $EUID != 0 ]]; then echo "Usage as root: storage-test.sh STATIC_HOST_SERVER ATV_ZIP DREAMOS_TAR" >&2; exit 2; fi
if [[ ${ODR_TEST_NAMESPACE:-} != private ]]; then
 exec unshare --mount --net --propagation private env ODR_TEST_NAMESPACE=private bash "$0" "$@"
fi
host=$(realpath "$1"); atv=$(realpath "$2"); legacy=$(realpath "$3")
work=$(mktemp -d /tmp/odr-dm920-test.XXXXXXXX); root="$work/root"; mountpoint="$work/view"
mkdir -p "$root" "$mountpoint"; declare -a loops=(); pid=""
cleanup(){
 if [[ -n $pid ]];then kill "$pid" 2>/dev/null || true;wait "$pid" 2>/dev/null || true;fi
 umount "$mountpoint" 2>/dev/null || true
 for loop in "${loops[@]}";do losetup -d "$loop";done
 if [[ $work == /tmp/odr-dm920-test.* ]];then rm -rf -- "$work";fi
}
trap cleanup EXIT
ip link set lo up
mkdir -p "$root"/{dev,run,tmp,etc,usr/sbin,bin,sbin,proc/stb/info,proc/sys/kernel,proc/self,sys/block/mmcblk0boot1,sys/class/block}
cp "$host" "$root/usr/sbin/odr-dm920"
libs(){ local binary=$1 target=$2;mkdir -p "$root/$(dirname "$target")";cp -L "$binary" "$root/$target";ldd "$binary" | awk '{ for(i=1;i<=NF;i++) if ($i ~ /^\//) print $i }' | while read -r lib; do mkdir -p "$root$(dirname "$lib")";cp -L "$lib" "$root$lib";done; }
libs "$(command -v mkfs.ext4)" /sbin/mkfs.ext4
libs "$(command -v xz)" /bin/xz-real
libs /bin/sh /bin/sh
printf '%s\n' '#!/bin/sh' 'echo decode >> /run/test-xz' 'exec /bin/xz-real "$@"' > "$root/bin/xz"
chmod 755 "$root/bin/xz"
# Physical reboot is unreachable in the sandbox.
libs /bin/true /sbin/reboot
[[ ! -f /etc/mke2fs.conf ]] || cp /etc/mke2fs.conf "$root/etc/"
echo dm920 >"$root/proc/stb/info/model"; echo 1.0 >"$root/etc/odr-dm920"; cp /proc/meminfo "$root/proc/meminfo"
echo fixture >"$root/proc/sys/kernel/osrelease";echo 1 >"$root/sys/block/mmcblk0boot1/force_ro"
ln -s /usr/sbin/odr-dm920 "$root/proc/self/exe";echo '1 0 0:1 / / rw - tmpfs tmpfs rw' >"$root/proc/self/mountinfo"
mknod -m 666 "$root/dev/null" c 1 3; mknod -m 444 "$root/dev/urandom" c 1 9
declare -A disk
for spec in mmcblk0boot1:4M mmcblk0p1:31M mmcblk0p2:992M mmcblk0p3:1400M;do
 name=${spec%:*};size=${spec#*:};file="$work/$name.img";truncate -s "$size" "$file"
 case "$name" in *boot1) mkfs.vfat "$file" >/dev/null;; *p[23]) mkfs.ext4 -q -F "$file";; esac
 loop=$(losetup --find --show "$file");loops+=("$loop");disk[$name]=$loop
 numbers=$(lsblk -dnro MAJ:MIN "$loop");mknod "$root/dev/$name" b "${numbers%:*}" "${numbers#*:}"
 mkdir -p "$root/sys/class/block/$name";echo "$numbers" >"$root/sys/class/block/$name/dev";blockdev --getsz "$loop" >"$root/sys/class/block/$name/size"
done
losetup -f >/dev/null
cp -a /dev/loop-control /dev/loop[0-9]* "$root/dev/"
mount "${disk[mmcblk0boot1]}" "$mountpoint"
for n in 1 2 6;do part=3;[[ $n != 1 ]]||part=2;echo "kernel=/dev/mmcblk0p1 root=/dev/mmcblk0p$part rootsubdir=linuxrootfs$n" >"$mountpoint/STARTUP_$n";done
cp "$mountpoint/STARTUP_1" "$mountpoint/STARTUP";umount "$mountpoint"
for n in 1 2 6;do part=3;[[ $n != 1 ]]||part=2;mount -o nodev,nosuid,noexec "${disk[mmcblk0p$part]}" "$mountpoint"
 mkdir -p "$mountpoint/linuxrootfs$n/sbin";touch "$mountpoint/linuxrootfs$n/sbin/"{init,ldconfig};echo retained >"$mountpoint/retain-data";umount "$mountpoint";done
chroot "$root" /usr/sbin/odr-dm920 18080 >"$work/server.log" 2>&1 & pid=$!
url=http://127.0.0.1:18080/api
get(){ curl --noproxy '*' -fsS "$url/$1"; }
post(){ curl --noproxy '*' -fsS -H 'X-ODR-Request: 1' -H 'Content-Type: application/json' --data "$2" "$url/$1"; }
until get job >/dev/null 2>&1;do kill -0 "$pid";sleep 0.2;done
wait_job(){ local j;for ((i=0;i<420;i++));do j=$(get job);if [[ $j == *\"$1\"* ]];then return;fi;if [[ $j == *'"State":"error"'* ]];then echo "$j" >&2;return 1;fi;sleep 1;done;return 1; }
upload(){ curl --noproxy '*' -fsS -H 'X-ODR-Request: 1' -H 'X-ODR-Image: DM920%20test%20image' --data-binary "@$1" "$url/upload";wait_job ready; }
kernelhash(){ sha256sum "$work/mmcblk0p1.img" | cut -d' ' -f1; }
unzip -p "$atv" dm920/kernel.bin >"$work/shared-kernel"; test -s "$work/shared-kernel"
dd if="$work/shared-kernel" of="${disk[mmcblk0p1]}" conv=notrunc status=none
backup(){
 local kind=$1 slot=$2 id
 post backup "{\"Kind\":\"$kind\",\"Slot\":$slot,\"Date\":\"2026-11-02\"}";wait_job done
 id=$(get job | python3 -c 'import json,sys; j=json.load(sys.stdin); assert j["Action"]=="backup" and j["Slot"]==int(sys.argv[1]) and "2026-11-02" in j["Filename"] and "dm920-" in j["Filename"];print(j["BackupID"])' "$slot")
 get "backup-file?id=$id" >"$work/$kind.out"
 [[ $(curl --noproxy '*' -s -o /dev/null -w '%{http_code}' "$url/backup-file?id=invalid") == 404 ]]
}
initial=$(kernelhash)
tar czf "$work/incomplete.tar.gz" --files-from /dev/null
upload "$work/incomplete.tar.gz"
post install '{"Slot":2,"Confirm":"install"}';wait_job error
mount -o ro,nodev,nosuid,noexec "${disk[mmcblk0p3]}" "$mountpoint"
test -f "$mountpoint/linuxrootfs2/sbin/init"
test -z "$(find "$mountpoint" -maxdepth 1 -name '.odr-new-*' -print -quit)"
umount "$mountpoint"
echo "PASS rejected incomplete archive before replacing the guest slot"
# Force an extraction error after erasure and verify the persisted boot guard.
python3 - "$work" <<'PY'
import io, pathlib, sys, tarfile, zipfile
w=pathlib.Path(sys.argv[1]); buf=io.BytesIO()
with tarfile.open(fileobj=buf, mode="w") as tar:
    for name in ["sbin/init", "sbin/ldconfig", "sbin/init"]:
        entry=tarfile.TarInfo(name); entry.size=1; tar.addfile(entry,io.BytesIO(b"x"))
with zipfile.ZipFile(w/"bad-extract.zip","w") as z:
    z.write(w/"shared-kernel","dm920/kernel.bin"); z.writestr("dm920/rootfs.tar",buf.getvalue())
PY
upload "$work/bad-extract.zip"; post install '{"Slot":6,"Confirm":"install"}'; wait_job error
get job | python3 -c 'import json,sys; j=json.load(sys.stdin); assert j["DestinationAtRisk"] and j["Slot"]==6'
get info | python3 -c 'import json,sys; s=next(x for x in json.load(sys.stdin)["Layout"]["Slots"] if x["ID"]==6); assert s["Incomplete"] and not s["Installed"]'
post start '{"Slot":6,"Confirm":"start"}'; wait_job error
[[ $(kernelhash) == "$initial" ]]; echo "PASS interrupted guest installation is labelled incomplete and cannot be started"
get info | python3 -c 'import json,sys; l=json.load(sys.stdin)["Layout"]; assert l["Multi"] and [s["ID"] for s in l["Slots"]]==[1,2,6]'
upload "$legacy"
image_id=$(get job | python3 -c 'import json,sys; j=json.load(sys.stdin); assert j["Action"]=="upload" and j["Image"]=="DM920 test image"; print(j["ImageID"])')
[[ -n $image_id ]]
[[ $(curl --noproxy '*' -s -o /dev/null -w '%{http_code}' -H 'X-ODR-Request: 1' -d '{"Slot":2,"Confirm":"install","ImageID":"old-image"}' "$url/install") == 409 ]]
: > "$root/run/test-xz"
post install "{\"Slot\":2,\"Confirm\":\"install\",\"ImageID\":\"$image_id\"}";wait_job done
[[ $(wc -l < "$root/run/test-xz") == 1 ]]
get job | python3 -c 'import json,sys; j=json.load(sys.stdin); assert j["Slot"] == 2 and j["Action"]=="install" and j["Image"]=="DM920 test image"'
[[ $(kernelhash) == "$initial" ]];echo "PASS legacy image into guest slot, shared kernel unchanged"
backup settings 2
tar tzf "$work/settings.out" >"$work/settings.list"; grep -q '^etc/enigma2' "$work/settings.list"; ! grep -q '^sbin/' "$work/settings.list"
backup image 2
unzip -p "$work/image.out" dm920/kernel.bin | cmp - "$work/shared-kernel"
upload "$work/image.out";post install '{"Slot":6,"Confirm":"install"}';wait_job done
get job | python3 -c 'import json,sys; j=json.load(sys.stdin); assert "Extracting" in j["Details"] and "multiboot kernel" in j["Details"]'
[[ $(kernelhash) == "$initial" ]];echo "PASS settings download, full guest backup and reinstall into another slot, shared kernel unchanged"
post start '{"Slot":2,"Confirm":"start"}';wait_job reboot;sleep 1
mount -o ro "${disk[mmcblk0boot1]}" "$mountpoint";cmp "$mountpoint/STARTUP" "$mountpoint/STARTUP_2";umount "$mountpoint"
post remove '{"Confirm":"remove"}';wait_job done
mount -o ro "${disk[mmcblk0boot1]}" "$mountpoint";[[ -z $(find "$mountpoint" -iname 'STARTUP*' -print -quit) ]];umount "$mountpoint"
mount -o ro,nodev,nosuid,noexec "${disk[mmcblk0p3]}" "$mountpoint";[[ -f "$mountpoint/retain-data" && ! -e "$mountpoint/linuxrootfs2" && ! -e "$mountpoint/linuxrootfs6" ]];umount "$mountpoint"
get info | python3 -c 'import json,sys; l=json.load(sys.stdin)["Layout"]; assert not l["Multi"] and l["Slots"][0]["Folder"]=="linuxrootfs1" and l["Profile"]=="Single image \u00b7 shared kernel"'
upload "$atv";post install '{"Slot":1,"Confirm":"install"}';wait_job done;[[ $(kernelhash) == "$initial" ]];echo "PASS removal, fallback slot 1, OpenATV reinstall preserves kernel"
# Model a classic DreamOS root directly on p2 after a fresh fixture mkfs.
mkfs.ext4 -q -F "$work/mmcblk0p2.img"
mount -o nodev,nosuid,noexec "${disk[mmcblk0p2]}" "$mountpoint";mkdir "$mountpoint/sbin";touch "$mountpoint/sbin/"{init,ldconfig};umount "$mountpoint"
get info | python3 -c 'import json,sys; l=json.load(sys.stdin)["Layout"]; assert not l["Multi"] and l["Slots"][0]["Folder"]==""'
uuid=$(blkid -p -o value -s UUID "${disk[mmcblk0p2]}")
upload "$work/incomplete.tar.gz"
post install '{"Slot":1,"Confirm":"install"}';wait_job error
[[ $(blkid -p -o value -s UUID "${disk[mmcblk0p2]}") == "$uuid" && $(kernelhash) == "$initial" ]]
echo "PASS rejected incomplete single image before formatting or writing the kernel"
upload "$legacy";post install '{"Slot":1,"Confirm":"install"}';wait_job done
mount -o ro,nodev,nosuid,noexec "${disk[mmcblk0p2]}" "$mountpoint";k=$(find "$mountpoint/boot" -type f -name 'zImage-*-dm920' -print -quit)
cmp -n "$(stat -c %s "$k")" "$k" "$work/mmcblk0p1.img";umount "$mountpoint";echo "PASS classic DreamOS root on p2 and matching raw kernel on p1"
backup image 1
unzip -p "$work/image.out" dm920/kernel.bin >"$work/classic-kernel"; cmp -n "$(stat -c %s "$work/classic-kernel")" "$work/classic-kernel" "$work/mmcblk0p1.img"
echo "PASS classic DreamOS single-image backup"

# Recreate multiboot over the classic fixture. The legacy kernel remains on
# the virtual kernel partition so a successful OpenATV write is distinguishable.
mkfs.ext4 -q -F "$work/mmcblk0p2.img"
mount "${disk[mmcblk0boot1]}" "$mountpoint"
for n in 1 2; do part=3; [[ $n != 1 ]] || part=2; echo "kernel=/dev/mmcblk0p1 root=/dev/mmcblk0p$part rootsubdir=linuxrootfs$n" > "$mountpoint/STARTUP_$n"; done
cp "$mountpoint/STARTUP_2" "$mountpoint/STARTUP"; umount "$mountpoint"
mount "${disk[mmcblk0p3]}" "$mountpoint"; mkdir "$mountpoint/linuxrootfs2"; touch "$mountpoint/linuxrootfs2/guest-data"; umount "$mountpoint"
upload "$atv"; post install '{"Slot":1,"Confirm":"install"}'; wait_job done
cmp -n "$(stat -c %s "$work/shared-kernel")" "$work/shared-kernel" "$work/mmcblk0p1.img"
echo "PASS OpenATV slot 1 replaced the legacy kernel without a multiboot override"
# Leave much less space than DreamOS needs beside the currently installed image.
mount "${disk[mmcblk0p2]}" "$mountpoint"
read -r blocks bs < <(stat -f -c '%a %S' "$mountpoint")
fallocate -l $((blocks*bs-32*1024*1024)) "$mountpoint/linuxrootfs1/old-slot-data"
umount "$mountpoint"
upload "$legacy"; post install '{"Slot":1,"Confirm":"install"}'; wait_job kernel-choice
get job | python3 -c 'import json,sys; j=json.load(sys.stdin); assert not j.get("DestinationAtRisk") and "Clearing slot" not in j["Details"]'
decision(){ get job | python3 -c 'import json,sys; j=json.load(sys.stdin); assert j["State"]=="kernel-choice"; print(j["DecisionID"])'; }
original_choice=$(decision)
[[ $(curl -s -o /dev/null -w '%{http_code}' -H 'X-ODR-Request: 1' -d '{"Confirm":"disable-multiboot","DecisionID":"stale"}' "$url/kernel-decision") == 409 ]]
post kernel-decision "{\"Confirm\":\"cancel\",\"DecisionID\":\"$original_choice\"}"; wait_job ready
mount -o ro "${disk[mmcblk0p2]}" "$mountpoint"; test -s "$mountpoint/linuxrootfs1/old-slot-data"; umount "$mountpoint"
get info | python3 -c 'import json,sys; l=json.load(sys.stdin)["Layout"]; assert l["Multi"] and l["Selected"]==2'
cmp -n "$(stat -c %s "$work/shared-kernel")" "$work/shared-kernel" "$work/mmcblk0p1.img"
post install '{"Slot":1,"Confirm":"install"}'; wait_job kernel-choice
current_choice=$(decision); [[ $current_choice != "$original_choice" ]]
post kernel-decision "{\"Confirm\":\"disable-multiboot\",\"DecisionID\":\"$current_choice\"}"; wait_job done
cmp -n "$(stat -c %s "$work/classic-kernel")" "$work/classic-kernel" "$work/mmcblk0p1.img"
get info | python3 -c 'import json,sys; l=json.load(sys.stdin)["Layout"]; assert not l["Multi"] and l["Slots"][0]["Installed"] and not l["Slots"][0]["Folder"]'
mount -o ro "${disk[mmcblk0boot1]}" "$mountpoint"; [[ -z $(find "$mountpoint" -iname 'STARTUP*' -print -quit) ]]; umount "$mountpoint"
mount -o ro "${disk[mmcblk0p3]}" "$mountpoint"; test -f "$mountpoint/linuxrootfs2/guest-data"; umount "$mountpoint"
echo "PASS DreamOS slot 1 requires fresh consent; classic root, image kernel and retained guest data verified"

