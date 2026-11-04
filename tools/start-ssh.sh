#!/bin/sh
# SysV init starts this once alongside ODR, after xinetd has started.
set -eu
/etc/init.d/dropbear start
test -s /etc/dropbear/dropbear_rsa_host_key
ln -sf /etc/dropbear/ssh.xinetd /etc/xinetd.d/dropbear
exec /etc/init.d/xinetd reload
