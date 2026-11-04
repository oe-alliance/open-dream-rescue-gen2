/* SPDX-License-Identifier: GPL-3.0-only */
/* Broadcom kernel framebuffer and Dreambox front-panel LCD. */
#include "video.h"
#include "screen.h"
#include <errno.h>
#include <fcntl.h>
#include <linux/fb.h>
#include <linux/kd.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>
#include "fonts/lcdfont.h"

/* Dreambox framebuffer extensions, also used by Enigma2. */
#define DREAM_MANUAL_BLIT _IOW('F', 0x21, unsigned char)
#define DREAM_BLIT 0x22

static int fb = -1, tty = -1, tty_mode = -1;
static int manual_blit;
static struct fb_var_screeninfo mode;
static struct fb_fix_screeninfo info;
static uint8_t *mapping;
static uint8_t *saved;

static int channel_ok(struct fb_bitfield field)
{
	return !field.msb_right && field.length <= 8 && field.offset <= mode.bits_per_pixel &&
		field.length <= mode.bits_per_pixel - field.offset;
}

static uint32_t channel(uint32_t value, struct fb_bitfield field)
{
	return field.length ? (value >> (8 - field.length)) << field.offset : 0;
}

static size_t visible_offset(void)
{
	return (size_t)mode.yoffset * info.line_length + mode.xoffset * (mode.bits_per_pixel / 8);
}

int video_open(void)
{
	if (fb >= 0) return 0;
	fb = open("/dev/fb0", O_RDWR | O_CLOEXEC);
	if (fb < 0 || ioctl(fb, FBIOGET_VSCREENINFO, &mode) || ioctl(fb, FBIOGET_FSCREENINFO, &info))
		goto error;
	if (!mode.xres || !mode.yres || mode.xres > 4096 || mode.yres > 2160 ||
		info.type != FB_TYPE_PACKED_PIXELS || info.visual != FB_VISUAL_TRUECOLOR ||
		(mode.bits_per_pixel != 32 && mode.bits_per_pixel != 16) || mode.nonstd ||
		!channel_ok(mode.red) || !channel_ok(mode.green) || !channel_ok(mode.blue) ||
		!channel_ok(mode.transp) || !mode.red.length || !mode.green.length || !mode.blue.length ||
		((uint64_t)mode.xoffset + mode.xres) * (mode.bits_per_pixel / 8) > info.line_length ||
		((uint64_t)mode.yoffset + mode.yres) * info.line_length > info.smem_len) {
		errno = ENOTSUP;
		goto error;
	}
	mapping = mmap(NULL, info.smem_len, PROT_READ | PROT_WRITE, MAP_SHARED, fb, 0);
	if (mapping == MAP_FAILED) { mapping = NULL; goto error; }
	size_t bytes = (size_t)mode.xres * (mode.bits_per_pixel / 8);
	saved = malloc(bytes * mode.yres);
	if (!saved) goto error;
	for (unsigned int y = 0; y < mode.yres; y++)
		memcpy(saved + y * bytes, mapping + visible_offset() + y * info.line_length, bytes);
	tty = open("/dev/tty0", O_RDWR | O_CLOEXEC);
	if (tty >= 0 && ioctl(tty, KDGETMODE, &tty_mode) == 0)
		ioctl(tty, KDSETMODE, KD_GRAPHICS);
	ioctl(fb, FBIOBLANK, FB_BLANK_UNBLANK);
	unsigned char enabled = 1;
	manual_blit = ioctl(fb, DREAM_MANUAL_BLIT, &enabled) == 0;
	return 0;
error:
	perror("Open Dreambox HDMI framebuffer");
	video_close();
	return -1;
}

static int same_geometry(void)
{
	struct fb_var_screeninfo now;
	return ioctl(fb, FBIOGET_VSCREENINFO, &now) == 0 &&
		now.xres == mode.xres && now.yres == mode.yres &&
		now.xoffset == mode.xoffset && now.yoffset == mode.yoffset &&
		now.bits_per_pixel == mode.bits_per_pixel &&
		!memcmp(&now.red, &mode.red, sizeof(now.red)) &&
		!memcmp(&now.green, &mode.green, sizeof(now.green)) &&
		!memcmp(&now.blue, &mode.blue, sizeof(now.blue)) &&
		!memcmp(&now.transp, &mode.transp, sizeof(now.transp));
}

int video_present(const uint32_t *pixels)
{
	if (!mapping || !same_geometry()) return -1;
	unsigned int width = mode.xres, height = mode.xres * ODR_HEIGHT / ODR_WIDTH;
	if (height > mode.yres) { height = mode.yres; width = height * ODR_WIDTH / ODR_HEIGHT; }
	if (!width || !height) return -1;
	const unsigned int left = (mode.xres - width) / 2, top = (mode.yres - height) / 2;
	const unsigned int bytes = mode.bits_per_pixel / 8;
	const uint32_t alpha = channel(255, mode.transp);
	for (unsigned int y = 0; y < mode.yres; y++) {
		uint8_t *row = mapping + visible_offset() + y * info.line_length;
		const uint32_t *source = (y >= top && y < top + height) ?
			pixels + ((y - top) * ODR_HEIGHT / height) * ODR_WIDTH : NULL;
		for (unsigned int x = 0; x < mode.xres; x++) {
			uint32_t rgb = (source && x >= left && x < left + width) ?
				source[(x - left) * ODR_WIDTH / width] : 0;
			uint32_t pixel = alpha | channel((rgb >> 16) & 255, mode.red) |
				channel((rgb >> 8) & 255, mode.green) | channel(rgb & 255, mode.blue);
			memcpy(row + x * bytes, &pixel, bytes);
		}
	}
	return manual_blit ? ioctl(fb, DREAM_BLIT, 0) : 0;
}

void video_close(void)
{
	if (saved && mapping && same_geometry()) {
		size_t bytes = (size_t)mode.xres * (mode.bits_per_pixel / 8);
		for (unsigned int y = 0; y < mode.yres; y++)
			memcpy(mapping + visible_offset() + y * info.line_length, saved + y * bytes, bytes);
		if (manual_blit) ioctl(fb, DREAM_BLIT, 0);
	}
	if (manual_blit) {
		unsigned char disabled = 0;
		ioctl(fb, DREAM_MANUAL_BLIT, &disabled);
		manual_blit = 0;
	}
	if (tty_mode >= 0) ioctl(tty, KDSETMODE, tty_mode);
	if (tty >= 0) close(tty);
	if (mapping) munmap(mapping, info.smem_len);
	if (fb >= 0) close(fb);
	free(saved);
	saved = NULL;
	mapping = NULL;
	tty = fb = tty_mode = -1;
}

static int panel_matches(const char *path, const char *expected)
{
	char value[32];
	FILE *stream = fopen(path, "r");
	if (!stream) return 0;
	int result = fscanf(stream, "%31s", value) == 1 && !strcmp(value, expected);
	fclose(stream);
	return result;
}

/* Dream's original OLED packs two grayscale pixels into a byte, high nibble first. */
static void oled_line(uint8_t *page, unsigned int width, unsigned int y, const char *text)
{
	for (unsigned int x = 2; *text && x + 6 <= width; x += 6, text++) {
		unsigned char glyph = (unsigned char)*text;
		if (glyph >= 128) glyph = '?';
		for (unsigned int col = 0; col < 6; col++)
			for (unsigned int row = 0; row < 8; row++)
				if (lcdfont[glyph * 6 + col] & (1 << row))
					page[(y + row) * (width / 2) + (x + col) / 2] |= ((x + col) & 1) ? 0x0f : 0xf0;
	}
}

static int present_oled(const char *web, int dm820)
{
	uint8_t page[128 * 64 / 2] = {0};
	uint16_t color[96 * 64];
	unsigned int width = dm820 ? 96 : 128;
	const void *frame = page;
	size_t size = sizeof(page);
	oled_line(page, width, 3, dm820 ? "ODR 1.0" : "Open Dream Rescue");
	oled_line(page, width, 18, dm820 ? "DM820" : "DM7080");
	oled_line(page, width, 37, "Web interface");
	oled_line(page, width, 51, !strncmp(web, "http://", 7) ? web + 7 : (dm820 ? "Waiting for IP" : "Waiting for network"));
	if (dm820) {
		/* Black and white have the same byte order in RGB565 BE and LE. */
		for (size_t i = 0; i < sizeof(color) / sizeof(color[0]); i++)
			color[i] = page[i / 2] & ((i & 1) ? 0x0f : 0xf0) ? 0xffff : 0;
		frame = color;
		size = sizeof(color);
	}
	const char *dev = access("/dev/dbox/oled0", W_OK) == 0 ? "/dev/dbox/oled0" : "/dev/dbox/lcd0";
	int fd = open(dev, O_WRONLY | O_NONBLOCK | O_CLOEXEC);
	if (fd < 0) return -1;
	struct stat st;
	ssize_t written = -1;
	if (fstat(fd, &st) == 0 && S_ISCHR(st.st_mode)) {
		do { written = write(fd, frame, size); } while (written < 0 && errno == EINTR);
	}
	close(fd);
	return written == (ssize_t)size ? 0 : -1;
}

int video_front_present(const uint32_t *pixels, unsigned int stride, const char *web)
{
	enum { width = 400, height = 240 };
	static uint16_t frame[width * height];
	int dm7080 = access("/etc/odr-dm7080", F_OK) == 0;
	if (dm7080 &&
		(access("/proc/stb/lcd/xres", F_OK) != 0 || panel_matches("/proc/stb/lcd/xres", "00000080")) &&
		(access("/proc/stb/lcd/yres", F_OK) != 0 || panel_matches("/proc/stb/lcd/yres", "00000040")) &&
		(access("/proc/stb/lcd/bpp", F_OK) != 0 || panel_matches("/proc/stb/lcd/bpp", "00000004")))
		return present_oled(web, 0);
	if (access("/etc/odr-dm820", F_OK) == 0 &&
		panel_matches("/proc/stb/lcd/xres", "00000060") &&
		panel_matches("/proc/stb/lcd/yres", "00000040") &&
		panel_matches("/proc/stb/lcd/bpp", "00000010") &&
		panel_matches("/proc/stb/lcd/colorformat", "RGB_565_BE"))
		return present_oled(web, 1);
	if (stride < width || !panel_matches("/proc/stb/lcd/xres", "00000190") ||
		!panel_matches("/proc/stb/lcd/yres", "000000f0") ||
		!panel_matches("/proc/stb/lcd/bpp", "00000010") ||
		!panel_matches("/proc/stb/lcd/colorformat", "BGR_565_LE"))
		return -1;
	for (unsigned int y = 0; y < height; y++) {
		for (unsigned int x = 0; x < width; x++) {
			uint32_t rgb = pixels[y * stride + x];
			frame[y * width + x] = ((rgb >> 8) & 0xf800) |
				((rgb >> 5) & 0x07e0) | ((rgb >> 3) & 0x001f);
		}
	}
	int fd = open("/dev/dbox/oled0", O_WRONLY | O_CLOEXEC);
	if (fd < 0) return -1;
	ssize_t written;
	do { written = write(fd, frame, sizeof(frame)); } while (written < 0 && errno == EINTR);
	close(fd);
	return written == (ssize_t)sizeof(frame) ? 0 : -1;
}
