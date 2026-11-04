/* SPDX-License-Identifier: GPL-3.0-only */
#include <errno.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>
#include "remote.h"
#include "screen.h"
#include "video.h"

static uint32_t pixels[ODR_WIDTH * ODR_HEIGHT];
static int hdmi;
static char last_frame[8192];

static int show(char *line)
{
	char *fields[FIELD_COUNT];
	for (int i = 0; i < FIELD_COUNT; i++) {
		if (!line) return 0;
		fields[i] = strsep(&line, "\t");
	}
	if (line) return 0;
	if (hdmi) {
		draw_screen(pixels, fields);
		if (video_present(pixels) < 0) { video_close(); hdmi = 0; }
	}
	draw_front(pixels, fields);
	video_front_present(pixels, ODR_WIDTH, fields[WEB]);
	return 0;
}

int main(void)
{
	hdmi = video_open() == 0;
	remote_open();
	char frame[8192];
	size_t used = 0;
	struct pollfd input = { STDIN_FILENO, POLLIN, 0 };
	int status = 0;
	struct timespec now;
	clock_gettime(CLOCK_MONOTONIC, &now);
	time_t retry = now.tv_sec + 5;
	for (;;) {
		int n = poll(&input, 1, 40);
		if (n < 0 && errno == EINTR) continue;
		if (n < 0) { status = 1; break; }
		if (input.revents & (POLLIN | POLLHUP | POLLERR)) {
			ssize_t bytes = read(STDIN_FILENO, frame + used, sizeof(frame) - used);
			if (bytes <= 0) break;
			used += (size_t)bytes;
			char *start = frame, *end;
			while ((end = memchr(start, '\n', used))) {
				*end = 0;
				memcpy(last_frame, start, (size_t)(end - start) + 1);
				if (show(start) < 0) { status = 1; goto done; }
				used -= (size_t)(end + 1 - start);
				start = end + 1;
			}
			if (used == sizeof(frame)) { status = 1; break; }
			memmove(frame, start, used);
		}
		if (!hdmi && clock_gettime(CLOCK_MONOTONIC, &now) == 0 && now.tv_sec >= retry) {
			retry = now.tv_sec + 5;
			hdmi = video_open() == 0;
			if (hdmi && last_frame[0]) {
				char copy[sizeof(last_frame)];
				strcpy(copy, last_frame);
				show(copy);
			}
		}
		int key = remote_key();
		if (key > 0) { printf("KEY %d\n", key); fflush(stdout); }
	}
done:
	remote_close();
	video_close();
	return status;
}
