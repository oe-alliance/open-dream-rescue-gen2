/* SPDX-License-Identifier: GPL-2.0-or-later */
/* Use the Dream BCM7435 boot container and its direct-I/O readback. */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/sysmacros.h>
#include <unistd.h>
#include "dream_bootblob.h"
#include "dream_lcd.h"

static unsigned char *load(const char *path, size_t *length)
{
	struct stat st;
	void *data = NULL;
	FILE *in = fopen(path, "rb");
	if (!in) return NULL;
	if (fstat(fileno(in), &st) || !S_ISREG(st.st_mode) || st.st_size < 4096 ||
	    st.st_size > DREAM_BANK_SIZE || posix_memalign(&data, 4096, (size_t)st.st_size) ||
	    fread(data, 1, st.st_size, in) != (size_t)st.st_size) {
		free(data);
		data = NULL;
	} else {
		*length = st.st_size;
	}
	fclose(in);
	return data;
}

int main(int argc, char **argv)
{
	unsigned char *data = NULL, *blob = NULL;
	size_t size = 0, length = 0, animsize;
	struct dream_bootblob_parts parts;
	const int fd = 3; /* Passed by the backend after checking model, layout and bank selector. */
	struct stat disk;
	int ok = 0;
	if (argc == 4 && !strcmp(argv[1], "prepare")) {
		data = load(argv[2], &size);
		const unsigned char *anim = dream_lcd_animation(ODR_BANK_MODEL, &animsize);
		if (data && dream_bootblob_create(data, size, anim, animsize, &blob, &length)) {
			FILE *out = fopen(argv[3], "wb");
			if (out) {
				ok = fwrite(blob, 1, length, out) == length;
				if (fclose(out)) ok = 0;
			}
		}
	} else if (fstat(fd, &disk) || !S_ISBLK(disk.st_mode) || disk.st_rdev != makedev(179, 0)) {
		fprintf(stderr, "Expected the internal eMMC on fd 3\n");
		return 1;
	} else if (argc == 3 && !strcmp(argv[1], "write")) {
		data = load(argv[2], &size);
		if (data && dream_bootblob_parse(data, size, &parts) && parts.blob_size == size) {
			ok = dream_bootblob_write_a(fd, data, size, NULL);
		}
	} else if (argc == 2 && !strcmp(argv[1], "read")) {
		if (dream_bootblob_read_a(fd, &blob, &length) && dream_bootblob_parse(blob, length, &parts))
			ok = fwrite(blob + parts.kernel_offset, 1, parts.kernel_size, stdout) == parts.kernel_size && !fflush(stdout);
	}
	free(data);
	free(blob);
	if (!ok) fprintf(stderr, "%s kernel bank operation failed: %s\n", ODR_BANK_MODEL, strerror(errno));
	return !ok;
}
