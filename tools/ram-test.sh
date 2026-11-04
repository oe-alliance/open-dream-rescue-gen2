#!/bin/sh
# First boot on an unverified receiver. Serve measured device information only.
set -eu
model=${0##*/odr-}
actual=$(cat /proc/stb/info/model)
work=$(mktemp -d /run/odr-probe.XXXXXX)
webpid=
hpid=
cleanup() {
	[ -z "$webpid" ] || kill "$webpid" 2>/dev/null || :
	[ -z "$hpid" ] || kill "$hpid" 2>/dev/null || :
	rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 0' INT TERM
mkdir "$work/www"
/usr/sbin/odr-inventory "$model" > "$work/www/device.txt" || { sleep 10; exit 1; }
cat > "$work/http.conf" <<EOF
server.document-root = "$work/www"
server.port = 80
index-file.names = ( "device.txt" )
mimetype.assign = ( ".txt" => "text/plain; charset=utf-8" )
EOF
/usr/sbin/lighttpd -D -f "$work/http.conf" &
webpid=$!
mkfifo "$work/frames" "$work/keys"
/usr/sbin/odr-hdmi < "$work/frames" > "$work/keys" &
hpid=$!
exec 3>"$work/frames" 4<"$work/keys"
page=0
while kill -0 "$hpid" && kill -0 "$webpid"; do
	ipaddr=$(ip -4 addr show dev eth0 | awk '/inet / {sub(/\/.*/, "", $2); print $2; exit}')
	url="Waiting for network"
	[ -z "$ipaddr" ] || url="http://$ipaddr/"
	if [ "$page" = 0 ]; then
		set -- "Receiver" "Model   $actual" "Model ID   $(cat /proc/stb/info/mid)" "Kernel   $(uname -r)" "$(sed -n '/^MemTotal:/p' /proc/meminfo | tr -s ' ')"
	elif [ "$model" = dm520 ]; then
		set -- "Storage" "Internal NAND - single image" "$(awk '$1=="mtd2:"{print}' /proc/mtd)" "$(awk '$1=="mtd0:"{print}' /proc/mtd)" "Full hardware inventory in the browser"
	else
		set -- "Storage" "$(awk 'NR==2{print}' /proc/mtd)" "$(awk 'NR==3{print}' /proc/mtd)" "$(awk 'NR==4{print}' /proc/mtd)" "Full hardware inventory in the browser"
	fi
	printf 'device\t%s\t%s\t%s\t%s\t%s\t%s\t\t\t%s\t%s\t%s\n' "Dreambox $actual  |  Open Dream Rescue" "$@" "$url" "Hardware inventory" "INFO / ARROWS: change page" >&3
	if read -r -t 4 kind key <&4 && [ "$kind" = KEY ]; then
		case "$key" in 103|105|106|108|358) page=$((1-page)) ;; 158|174) page=0 ;; esac
	fi
done
