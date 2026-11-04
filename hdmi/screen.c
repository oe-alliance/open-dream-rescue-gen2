/* Linux adaptation of dreambox-bootmenu gfx.h/styles.h, OE-Alliance 2026.
 * SPDX-License-Identifier: GPL-3.0-only
 * Uses its 4-bit Roboto glyphs and centered scrolling selection. No logo,
 * raw hardware addresses, countdown, default boot or persistent writes. */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include "screen.h"
typedef uint8_t u8;
typedef uint16_t u16;
typedef int16_t s16;
typedef uint32_t u32;
struct glyph { u8 w, h; s16 xoff, yoff; u8 adv; u32 off; };
struct font { const struct glyph *g; const u8 *bits; const u16 *codes; int ascent, height, count; };
#include "fonts/bootmenu-fonts.h"
#include "fonts/bootmenu-fonts-bold.h"
#include "fonts/bootmenu-fonts-small.h"
#define GROUND 0x0b1016
#define SURFACE 0x121a23
#define DIALOG 0x1a2530
#define LINE 0x253443
#define TEXT 0xe8edf2
#define BODY 0xc9d4de
#define MUTED 0x7d8fa1
#define DIM 0x506172
#define FOCUS 0x2b7fd9
#define WARN 0xf2b84b
#define GREEN 0x4cc26a
#define LINK 0x7cc4ff

static void pixel(u32 *pixels, int x, int y, u32 color, unsigned int alpha)
{
	if (x < 0 || x >= ODR_WIDTH || y < 0 || y >= ODR_HEIGHT || !alpha) return;
	u32 *dst = &pixels[y * ODR_WIDTH + x], result = 0;
	for (int shift = 0; shift <= 16; shift += 8) {
		unsigned int a = (color >> shift) & 255, b = (*dst >> shift) & 255;
		result |= ((a * alpha + b * (255 - alpha) + 127) / 255) << shift;
	}
	*dst = 0xff000000u | result;
}

static void rect(u32 *pixels, int x, int y, int w, int h, u32 color)
{
	for (int row = 0; row < h; row++) for (int col = 0; col < w; col++) pixel(pixels, x + col, y + row, color, 255);
}

/* Portable parts adapted from gfx.h; no UART/MMIO/cache operations. */
static u32 utf8_next(const char **s)
{
	const u8 *p = (const u8 *)*s;
	u32 c = *p++;
	int need = c >= 0xf0 ? 3 : c >= 0xe0 ? 2 : c >= 0xc0 ? 1 : c >= 0x80 ? -1 : 0;
	if (need < 0) { c = '?'; need = 0; } else if (need) c &= 0x3f >> need;
	while (need--) { if ((*p & 0xc0) != 0x80) { c = '?'; break; } c = c << 6 | (*p++ & 0x3f); }
	*s = (const char *)p;
	return c;
}

static int glyph_index(const struct font *f, u32 c)
{
	int lo = 0, hi = f->count - 1;
	while (lo <= hi) { int mid = (lo + hi) / 2; if (f->codes[mid] == c) return mid; if (f->codes[mid] < c) lo = mid + 1; else hi = mid - 1; }
	return -1;
}

static const struct glyph *glyph_of(const struct font *f, u32 c)
{
	int i = glyph_index(f, c);
	return &f->g[i < 0 ? glyph_index(f, '?') : i];
}

static int text_width(const struct font *f, const char *t)
{
	int width = 0;
	while (*t) width += glyph_of(f, utf8_next(&t))->adv;
	return width;
}

static void text(u32 *pixels, const struct font *f, int x, int y, const char *t, u32 color, int right)
{
	int pen = x;
	while (*t) {
		const struct glyph *g = glyph_of(f, utf8_next(&t));
		const u8 *bits = f->bits + g->off;
		int stride = (g->w + 1) / 2;
		if (pen + g->xoff + g->w > right) break;
		for (int row = 0; row < g->h; row++) for (int col = 0; col < g->w; col++) {
			u32 v = bits[row * stride + col / 2];
			u32 alpha = (col & 1 ? v & 15 : v >> 4) * 17;
			pixel(pixels, pen + g->xoff + col, y + g->yoff + row, color, alpha);
		}
		pen += g->adv;
	}
}

static void center(u32 *pixels, const struct font *f, int y, const char *t, u32 color)
{
	/* Ellipsize at code point boundaries instead of overflowing long titles. */
	char clipped[256]; size_t size = strlen(t);
	if (size >= sizeof(clipped)) size = sizeof(clipped) - 1;
	memcpy(clipped, t, size); clipped[size] = 0;
	if (text_width(f, clipped) > 1100) {
		while (size && text_width(f, clipped) + text_width(f, "...") > 1100) { do { size--; } while (size && ((unsigned char)clipped[size] & 0xc0) == 0x80); clipped[size] = 0; }
		if (size + 3 < sizeof(clipped)) strcat(clipped, "...");
	}
	text(pixels, f, (ODR_WIDTH - text_width(f, clipped)) / 2, y, clipped, color, ODR_WIDTH - 50);
}

void draw_screen(u32 *pixels, char *const f[FIELD_COUNT])
{
	for (size_t i = 0; i < ODR_WIDTH * ODR_HEIGHT; i++)
		pixels[i] = 0xff000000u | GROUND;
	center(pixels, &font_sel, 47, "ODR  |  Open Dream Rescue", TEXT);
	center(pixels, &font_tiny, 111, f[PROFILE], MUTED);
	center(pixels, &font_head, 173, f[TITLE], BODY);
	if (!strcmp(f[VIEW], "device")) {
		for (int i = 0; i < 4; i++) {
			int y = 241 + i * 61;
			center(pixels, &font_far, y, f[FAR_ABOVE + i], TEXT);
			if (f[FAR_ABOVE + i][0]) rect(pixels, 270, y + 41, 740, 1, LINE);
		}
	} else if (!strcmp(f[VIEW], "message")) {
		rect(pixels, 152, 266, 976, 150, DIALOG);
		center(pixels, &font_list, 290, f[FOCUS_ROW], TEXT);
		center(pixels, &font_tiny, 358, f[DETAIL], BODY);
	} else {
		center(pixels, &font_far, 233, f[FAR_ABOVE], DIM);
		center(pixels, &font_list, 274, f[ABOVE], MUTED);
		rect(pixels, 140, 330, 1000, 1, LINE);
		rect(pixels, 140, 330, 4, 71, FOCUS);
		rect(pixels, 140, 401, 1000, 1, LINE);
		center(pixels, &font_sel, 334, f[FOCUS_ROW], TEXT);
		center(pixels, &font_tiny, 379, f[DETAIL], LINK);
		center(pixels, &font_list, 420, f[BELOW], MUTED);
		center(pixels, &font_far, 465, f[FAR_BELOW], DIM);
	}
	rect(pixels, 80, 524, 1120, 1, LINE);
	center(pixels, &font_head, 548, "OPEN THE WEB INTERFACE IN YOUR BROWSER", BODY);
	center(pixels, &font_list, 585, f[WEB], LINK);
	center(pixels, &font_tiny, 629, f[HINT], MUTED);
	rect(pixels, 0, 677, ODR_WIDTH, 43, SURFACE);
	center(pixels, &font_tiny, 691, f[FOOTER], BODY);
}

void draw_front(u32 *pixels, char *const f[FIELD_COUNT])
{
	rect(pixels, 0, 0, 400, 240, GROUND);
	rect(pixels, 0, 0, 6, 240, FOCUS);
	text(pixels, &font_far, 22, 15, "Open Dream Rescue", TEXT, 390);
	text(pixels, &font_tiny, 23, 61, f[PROFILE], MUTED, 390);
	rect(pixels, 20, 93, 360, 1, LINE);
	text(pixels, &font_far, 22, 110, !strcmp(f[VIEW], "device") ? "Device information" : f[FOCUS_ROW], BODY, 390);
	text(pixels, &font_tiny, 23, 174, "Web interface", MUTED, 390);
	const char *address = !strncmp(f[WEB], "http://", 7) ? f[WEB] + 7 : "Waiting for network";
	text(pixels, &font_far, 22, 195, address, LINK, 390);
}

