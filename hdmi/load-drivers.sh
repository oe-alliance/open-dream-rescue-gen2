#!/bin/sh
# Invoked once at boot before the HDMI process starts.
set -eu
case "$(uname -r)" in
	3.14-1.17-dm900|3.14-1.17-dm920) chip=bcm7439 ;;
	3.4-4.0-dm7080|3.4-4.0-dm820) chip=bcm7435 ;;
	3.4-4.0-dm520) chip=bcm73625 ;;
	*) exit 1 ;;
esac
test ! -d /sys/module/bcmdriver || { echo "ODR: Nexus is already using the display" >&2; exit 1; }
# Use the same probe order as the Dream image.
/sbin/modprobe stb-core
if [ "$chip" != bcm73625 ]; then /sbin/modprobe lcd; fi
/sbin/modprobe "$chip"
if [ "$chip" != bcm7439 ]; then
	/sbin/modprobe dreambox_rc2
fi
