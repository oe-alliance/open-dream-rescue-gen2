/* SPDX-License-Identifier: GPL-3.0-only */
#ifndef ODR_DM920_VIDEO_H
#define ODR_DM920_VIDEO_H

#include <stdint.h>

/* HDMI input is 1280 by 720, tightly packed 0xffRRGGBB. */
int video_open(void);
int video_present(const uint32_t *pixels);
void video_close(void);

/* Front panel input is 400 by 240 in the same format. Stride is in pixels. */
int video_front_present(const uint32_t *pixels, unsigned int stride, const char *web);

#endif
