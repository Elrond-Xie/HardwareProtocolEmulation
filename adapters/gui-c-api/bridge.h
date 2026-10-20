/*
 * File: bridge.h
 * Purpose: Project-owned implementation and interface for adapters / gui-c-api / bridge.h.
 * Author: Elrond-Xie
 * Copyright (c) 2026 Elrond-Xie. SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0.
 */

#ifndef HPE_GUI_BRIDGE_H
#define HPE_GUI_BRIDGE_H
#include <stddef.h>
#include <stdint.h>
#ifdef _WIN32
#define HPE_GUI_API __declspec(dllexport)
#else
#define HPE_GUI_API __attribute__((visibility("default")))
#endif
typedef struct hpe_gui_sim hpe_gui_sim;
typedef struct hpe_gui_event
{
    uint64_t time_ns, data_start_ns;
    int32_t kind, protocol;
    uint8_t scl, sda, clock, mosi, miso, chip_select, value, read;
    uint8_t data_lines[8], data_line_count;
    char message[96];
} hpe_gui_event;
typedef struct hpe_gui_device
{
    int32_t protocol;
    char name[48];
    uint32_t locator, identity, memory_size;
    uint8_t state, flags;
} hpe_gui_device;
HPE_GUI_API hpe_gui_sim *hpe_gui_sim_create(void);
HPE_GUI_API void hpe_gui_sim_destroy(hpe_gui_sim *sim);
HPE_GUI_API int32_t hpe_gui_scenario_run(hpe_gui_sim *sim, const char *text, char *error,
                                         size_t error_size);
HPE_GUI_API size_t hpe_gui_event_count(const hpe_gui_sim *sim);
HPE_GUI_API int32_t hpe_gui_event_get(const hpe_gui_sim *sim, size_t index, hpe_gui_event *out);
HPE_GUI_API size_t hpe_gui_device_count(const hpe_gui_sim *sim);
HPE_GUI_API int32_t hpe_gui_device_get(const hpe_gui_sim *sim, size_t index, hpe_gui_device *out);
HPE_GUI_API int32_t hpe_gui_device_read(const hpe_gui_sim *sim, size_t index, uint32_t offset,
                                        uint8_t *data, size_t length);
#endif
