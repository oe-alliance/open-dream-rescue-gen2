/* SPDX-License-Identifier: GPL-3.0-only */
#ifndef ODR_SCREEN_H
#define ODR_SCREEN_H
#include <stdint.h>
#define ODR_WIDTH 1280
#define ODR_HEIGHT 720
enum frame_field { VIEW, PROFILE, TITLE, FAR_ABOVE, ABOVE, FOCUS_ROW, DETAIL,
	BELOW, FAR_BELOW, WEB, HINT, FOOTER, FIELD_COUNT };
void draw_screen(uint32_t *pixels, char *const fields[FIELD_COUNT]);
void draw_front(uint32_t *pixels, char *const fields[FIELD_COUNT]);
#endif
