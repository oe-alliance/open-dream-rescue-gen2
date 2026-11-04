/* SPDX-License-Identifier: GPL-3.0-only */
#include <errno.h>
#include <fcntl.h>
#include <linux/input.h>
#include <stdio.h>
#include <sys/ioctl.h>
#include <time.h>
#include <unistd.h>

#include "remote.h"

enum { MAX_EVENTS = 32 };
static int devices[MAX_EVENTS];
static time_t next_scan;

static int supports_key(const unsigned char *bits, int key)
{
	return bits[key / 8] & (1u << (key % 8));
}

static void scan(void)
{
	for (int i = 0; i < MAX_EVENTS; i++) {
		if (devices[i] >= 0) continue;
		char path[32];
		snprintf(path, sizeof(path), "/dev/input/event%d", i);
		int fd = open(path, O_RDONLY | O_NONBLOCK | O_CLOEXEC);
		if (fd < 0) continue;
		unsigned char keys[KEY_MAX / 8 + 1] = {0};
		if (ioctl(fd, EVIOCGBIT(EV_KEY, sizeof(keys)), keys) < 0 ||
		    !(supports_key(keys, KEY_OK) || supports_key(keys, KEY_UP))) {
			close(fd);
			continue;
		}
		devices[i] = fd;
		char name[128] = {0};
		ioctl(fd, EVIOCGNAME(sizeof(name) - 1), name);
		fprintf(stderr, "ODR input: %s (%s)\n", name, path);
	}
}

int remote_open(void)
{
	for (int i = 0; i < MAX_EVENTS; i++) devices[i] = -1;
	scan();
	next_scan = 0;
	return 0;
}

int remote_key(void)
{
	struct timespec now;
	if (clock_gettime(CLOCK_MONOTONIC, &now) == 0 && now.tv_sec >= next_scan) {
		scan();
		next_scan = now.tv_sec + 3;
	}
	for (int i = 0; i < MAX_EVENTS; i++) {
		if (devices[i] < 0) continue;
		for (int n = 0; n < 16; n++) {
			// Linux 3.14 evdev uses two native longs, including on ARM
			// when the userspace libc defines time_t to be 64 bits.
			struct { long sec, usec; __u16 type, code; __s32 value; } event;
			ssize_t count = read(devices[i], &event, sizeof(event));
			if (count != (ssize_t)sizeof(event)) {
				if (count >= 0 || (errno != EAGAIN && errno != EINTR)) {
					close(devices[i]);
					devices[i] = -1;
				}
				break;
			}
			/* A held OK or number must never become a second confirmation. */
			if (event.type == EV_KEY && event.value == 1) return event.code;
			if (event.type == EV_KEY && event.value == 2 &&
			    (event.code == KEY_UP || event.code == KEY_DOWN))
				return event.code;
		}
	}
	return 0;
}

void remote_close(void)
{
	for (int i = 0; i < MAX_EVENTS; i++) {
		if (devices[i] >= 0) close(devices[i]);
		devices[i] = -1;
	}
}
