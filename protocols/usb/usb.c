/*
 * File: usb.c
 * Purpose: Project-owned implementation and interface for protocols / usb / usb.c.
 * Author: Elrond-Xie
 * Copyright (c) 2026 Elrond-Xie. SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0.
 */

#include "internal.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define USB_PID_OUT 0xe1u
#define USB_PID_IN 0x69u
#define USB_PID_SETUP 0x2du
#define USB_PID_SOF 0xa5u
#define USB_PID_DATA0 0xc3u
#define USB_PID_DATA1 0x4bu
#define USB_PID_ACK 0xd2u
#define USB_PID_NAK 0x5au
#define USB_PID_STALL 0x1eu
#define USB_EP0_PACKET 64u

/** @brief Implements the usb_device_at operation. */
static hpe_usb_device_t *usb_device_at(const hpe_sim_t *sim, size_t index)
{
    if (!sim || index >= sim->usb_device_count) {
        return NULL;
    }
    return sim->usb_devices[index];
}

/** @brief Implements the copy_ascii operation. */
static void copy_ascii(char dst[48], const char *src)
{
    size_t i;
    for (i = 0; i < 47 && src && src[i]; ++i) {
        unsigned char c = (unsigned char)src[i];
        dst[i] = c < 0x80 ? (char)c : '?';
    }
}

/** @brief Implements the hpe_usb_device_add operation. */
hpe_status_t hpe_usb_device_add(hpe_sim_t *sim, const char *name, uint16_t vendor_id,
                                uint16_t product_id, const char *manufacturer, const char *product)
{
    hpe_usb_device_t *device;
    size_t i;
    if (!sim || !name || !name[0] || !manufacturer || !product) {
        return HPE_ERR_ARGUMENT;
    }
    if (sim->usb_device_count == sim->usb_device_capacity) {
        size_t capacity = sim->usb_device_capacity ? sim->usb_device_capacity * 2 : 4;
        hpe_usb_device_t **devices =
            (hpe_usb_device_t **)realloc(sim->usb_devices, capacity * sizeof(*devices));
        if (!devices) {
            return HPE_ERR_NOMEM;
        }
        sim->usb_devices = devices;
        sim->usb_device_capacity = capacity;
    }
    device = (hpe_usb_device_t *)calloc(1, sizeof(*device));
    if (!device) {
        return HPE_ERR_NOMEM;
    }
    copy_ascii(device->name, name);
    copy_ascii(device->manufacturer, manufacturer);
    copy_ascii(device->product, product);
    device->vendor_id = vendor_id;
    device->product_id = product_id;
    device->release = 0x0100;
    device->state = HPE_USB_DEFAULT;
    for (i = 0; i < sim->usb_device_count; ++i) {
        if (sim->usb_devices[i]->vendor_id == vendor_id &&
            sim->usb_devices[i]->product_id == product_id &&
            strcmp(sim->usb_devices[i]->name, device->name) == 0) {
            free(device);
            return HPE_ERR_ARGUMENT;
        }
    }
    sim->usb_devices[sim->usb_device_count++] = device;
    return HPE_OK;
}

/** @brief Implements the hpe_usb_device_count operation. */
size_t hpe_usb_device_count(const hpe_sim_t *sim)
{
    return sim ? sim->usb_device_count : 0;
}

/** @brief Implements the usb_crc16 operation. */
static uint16_t usb_crc16(const uint8_t *data, size_t length)
{
    uint16_t crc = 0xffffu;
    size_t i;
    unsigned bit;
    for (i = 0; i < length; ++i) {
        crc ^= data[i];
        for (bit = 0; bit < 8; ++bit) {
            crc = (uint16_t)((crc >> 1) ^ ((crc & 1u) ? 0xa001u : 0u));
        }
    }
    return (uint16_t)~crc;
}

/** @brief Implements the usb_crc5_token operation. */
static uint8_t usb_crc5_token(uint16_t token)
{
    uint8_t crc = 0x1fu;
    unsigned bit;
    for (bit = 0; bit < 11; ++bit) {
        uint8_t feedback = (uint8_t)((crc ^ (token >> bit)) & 1u);
        crc >>= 1;
        if (feedback) {
            crc ^= 0x14u;
        }
    }
    return (uint8_t)(~crc & 0x1fu);
}

/** @brief Implements the usb_emit_line_bit operation. */
static void usb_emit_line_bit(hpe_sim_t *sim, uint8_t dplus, uint8_t dminus, uint8_t pid,
                              uint8_t address, uint8_t endpoint, const char *description)
{
    hpe_event_t *event;
    size_t event_count = sim->event_count;
    sim->usb_dplus = dplus;
    sim->usb_dminus = dminus;
    sim->usb_bit_time_remainder = (uint8_t)(sim->usb_bit_time_remainder + 250u);
    if (sim->usb_bit_time_remainder >= 3u) {
        sim->time_ns += sim->usb_bit_time_remainder / 3u;
        sim->usb_bit_time_remainder %= 3u;
    }
    hpe_emit(sim, HPE_EVENT_LINE, (uint8_t)((dplus << 1) | dminus), 0, description);
    if (sim->event_count == event_count) {
        return;
    }
    event = &sim->events[sim->event_count - 1];
    event->usb_pid = pid;
    event->usb_address = address;
    event->usb_endpoint = endpoint;
    event->clock = dplus;
    event->mosi = dminus;
    event->scl = dplus;
    event->sda = dminus;
}

/** @brief Implements the usb_emit_nrzi_packet_raw operation. */
static void usb_emit_nrzi_packet_raw(hpe_sim_t *sim, uint8_t address, uint8_t endpoint, uint8_t pid,
                                     uint16_t token_bits, const uint8_t *payload,
                                     uint16_t payload_length)
{
    uint8_t packet[USB_EP0_PACKET + 4u];
    size_t packet_length = 2;
    size_t i;
    unsigned bit;
    unsigned ones = 0;
    uint8_t dplus = 1, dminus = 0;
    int token =
        pid == USB_PID_SETUP || pid == USB_PID_IN || pid == USB_PID_OUT || pid == USB_PID_SOF;

    packet[0] = 0x80u; /* SYNC, transmitted least-significant bit first. */
    packet[1] = pid;
    if (token) {
        uint16_t with_crc = (uint16_t)(token_bits | ((uint16_t)usb_crc5_token(token_bits) << 11));
        packet[2] = (uint8_t)with_crc;
        packet[3] = (uint8_t)(with_crc >> 8);
        packet_length = 4;
    }
    else if (pid == USB_PID_DATA0 || pid == USB_PID_DATA1) {
        if (payload_length > USB_EP0_PACKET) {
            payload_length = USB_EP0_PACKET;
        }
        if (payload_length && payload) {
            memcpy(packet + 2, payload, payload_length);
        }
        else {
            payload_length = 0;
        }
        {
            uint16_t crc = usb_crc16(payload, payload_length);
            packet[2 + payload_length] = (uint8_t)crc;
            packet[3 + payload_length] = (uint8_t)(crc >> 8);
        }
        packet_length = 4u + payload_length;
    }

    /* Full-speed bus idle and packet start are both J; SYNC begins with six zeros. */
    for (i = 0; i < packet_length; ++i) {
        for (bit = 0; bit < 8; ++bit) {
            uint8_t value = (uint8_t)((packet[i] >> bit) & 1u);
            if (!value) {
                dplus ^= 1u;
                dminus ^= 1u;
            }
            usb_emit_line_bit(sim, dplus, dminus, pid, address, endpoint,
                              value ? "USB NRZI bit 1" : "USB NRZI bit 0");
            if (value) {
                if (++ones == 6u) {
                    dplus ^= 1u;
                    dminus ^= 1u;
                    usb_emit_line_bit(sim, dplus, dminus, pid, address, endpoint,
                                      "USB stuffed zero bit");
                    ones = 0;
                }
            }
            else {
                ones = 0;
            }
        }
        if (i == 0) {
            ones = 0; /* Stuffing starts after the fixed SYNC field. */
        }
    }
    usb_emit_line_bit(sim, 0, 0, pid, address, endpoint, "USB EOP SE0 1/2");
    usb_emit_line_bit(sim, 0, 0, pid, address, endpoint, "USB EOP SE0 2/2");
    usb_emit_line_bit(sim, 1, 0, pid, address, endpoint, "USB EOP return to J");
}

/** @brief Implements the usb_emit operation. */
static void usb_emit(hpe_sim_t *sim, hpe_event_kind_t kind, uint8_t pid, uint8_t address,
                     uint8_t endpoint, uint16_t length, uint8_t value, uint16_t crc,
                     const char *message)
{
    hpe_event_t *event;
    size_t event_count = sim->event_count;
    sim->active_protocol = HPE_PROTOCOL_USB;
    hpe_emit(sim, kind, value, 0, message);
    if (sim->event_count == event_count) {
        return;
    }
    event = &sim->events[sim->event_count - 1];
    event->usb_pid = pid;
    event->usb_address = address;
    event->usb_endpoint = endpoint;
    event->payload_length = length;
    event->data_crc16 = crc;
    event->clock = sim->usb_dplus;
    event->mosi = sim->usb_dminus;
    event->scl = sim->usb_dplus;
    event->sda = sim->usb_dminus;
}

/** @brief Implements the usb_emit_due_sof operation. */
static void usb_emit_due_sof(hpe_sim_t *sim)
{
    uint64_t resume_time = sim->time_ns;
    while (resume_time >= sim->usb_next_sof_time_ns) {
        uint16_t frame = (uint16_t)(sim->usb_frame_number & 0x07ffu);
        char message[48];
        sim->time_ns = sim->usb_next_sof_time_ns;
        if (sim->event_count && sim->time_ns < sim->events[sim->event_count - 1].time_ns) {
            sim->time_ns = sim->events[sim->event_count - 1].time_ns;
        }
        usb_emit_nrzi_packet_raw(sim, 0, 0, USB_PID_SOF, frame, NULL, 0);
        snprintf(message, sizeof(message), "USB SOF frame %u", frame);
        usb_emit(sim, HPE_EVENT_INFO, USB_PID_SOF, 0, 0, 0, 0, 0, message);
        if (sim->event_count) {
            sim->events[sim->event_count - 1].protocol_argument = frame;
        }
        sim->usb_frame_number = (uint16_t)((frame + 1u) & 0x07ffu);
        sim->usb_next_sof_time_ns += 1000000u;
    }
    if (sim->time_ns < resume_time) {
        sim->time_ns = resume_time;
    }
}

/** @brief Implements the usb_emit_nrzi_packet operation. */
static void usb_emit_nrzi_packet(hpe_sim_t *sim, uint8_t address, uint8_t endpoint, uint8_t pid,
                                 const uint8_t *payload, uint16_t payload_length)
{
    uint16_t token_bits = (uint16_t)((address & 0x7fu) | ((uint16_t)(endpoint & 0x0fu) << 7));
    if (pid != USB_PID_SOF) {
        usb_emit_due_sof(sim);
    }
    usb_emit_nrzi_packet_raw(sim, address, endpoint, pid, token_bits, payload, payload_length);
}

/** @brief Implements the make_descriptor operation. */
static size_t make_descriptor(const hpe_usb_device_t *device, uint8_t type, uint8_t index,
                              uint16_t language, uint8_t out[256])
{
    size_t length = 0, i;
    if (type == 1 && index == 0) {
        const uint8_t descriptor[] = {18,
                                      1,
                                      0x00,
                                      0x02,
                                      0,
                                      0,
                                      0,
                                      USB_EP0_PACKET,
                                      (uint8_t)device->vendor_id,
                                      (uint8_t)(device->vendor_id >> 8),
                                      (uint8_t)device->product_id,
                                      (uint8_t)(device->product_id >> 8),
                                      (uint8_t)device->release,
                                      (uint8_t)(device->release >> 8),
                                      1,
                                      2,
                                      0,
                                      1};
        memcpy(out, descriptor, sizeof(descriptor));
        return sizeof(descriptor);
    }
    if (type == 2 && index == 0) {
        const uint8_t descriptor[] = {9, 2,    39, 0,  1, 1, 0, 0xa0, 50,   9,  4, 0, 0,
                                      3, 0xff, 0,  0,  0, 7, 5, 0x81, 2,    64, 0, 0, 7,
                                      5, 0x01, 2,  64, 0, 0, 7, 5,    0x82, 3,  8, 0, 10};
        memcpy(out, descriptor, sizeof(descriptor));
        return sizeof(descriptor);
    }
    if (type != 3) {
        return 0;
    }
    if (index == 0) {
        if (language != 0) {
            return 0;
        }
        out[0] = 4;
        out[1] = 3;
        out[2] = 0x09;
        out[3] = 0x04;
        return 4;
    }
    if (language != 0x0409) {
        return 0;
    }
    {
        const char *string = index == 1   ? device->manufacturer
                             : index == 2 ? device->product
                                          : NULL;
        if (!string) {
            return 0;
        }
        length = strlen(string);
        if (length > 126) {
            length = 126;
        }
        out[0] = (uint8_t)(2 + length * 2);
        out[1] = 3;
        for (i = 0; i < length; ++i) {
            out[2 + i * 2] = (uint8_t)string[i];
            out[3 + i * 2] = 0;
        }
        return out[0];
    }
}

/** @brief Implements the usb_emit_data_packets operation. */
static void usb_emit_data_packets(hpe_sim_t *sim, uint8_t address, uint8_t endpoint,
                                  const uint8_t *data, uint16_t length, uint8_t pid,
                                  const char *direction)
{
    uint16_t offset = 0;
    uint8_t toggle = 1;
    do {
        uint16_t packet_length = (uint16_t)(length - offset);
        uint16_t crc;
        char msg[96];
        if (packet_length > USB_EP0_PACKET) {
            packet_length = USB_EP0_PACKET;
        }
        snprintf(msg, sizeof(msg), "%s token DATA%u packet (%u byte(s))", direction, toggle,
                 packet_length);
        usb_emit_nrzi_packet(sim, address, endpoint, pid, NULL, 0);
        usb_emit(sim, HPE_EVENT_START, pid, address, endpoint, 0, 0, 0, msg);
        crc = usb_crc16(data ? data + offset : NULL, packet_length);
        usb_emit_nrzi_packet(sim, address, endpoint, toggle ? USB_PID_DATA1 : USB_PID_DATA0,
                             data ? data + offset : NULL, packet_length);
        usb_emit(sim, HPE_EVENT_DATA, toggle ? USB_PID_DATA1 : USB_PID_DATA0, address, endpoint,
                 packet_length, packet_length ? data[offset] : 0, crc,
                 "USB data packet; CRC16 included");
        usb_emit_nrzi_packet(sim, address, endpoint, USB_PID_ACK, NULL, 0);
        usb_emit(sim, HPE_EVENT_ACK, USB_PID_ACK, address, endpoint, 0, 0, 0, "USB ACK handshake");
        offset = (uint16_t)(offset + packet_length);
        toggle ^= 1u;
        if (packet_length == 0 || offset >= length) {
            break;
        }
    }
    while (1);
}

/** @brief Implements the hpe_usb_control_transfer operation. */
hpe_status_t hpe_usb_control_transfer(hpe_sim_t *sim, size_t device_index, uint8_t address,
                                      uint8_t request_type, uint8_t request, uint16_t value,
                                      uint16_t index, uint8_t *data, uint16_t length,
                                      uint16_t *actual_length)
{
    hpe_usb_device_t *device = usb_device_at(sim, device_index);
    uint8_t buffer[256];
    size_t descriptor_length = 0;
    uint16_t actual = 0;
    uint32_t argument;
    int in_direction = (request_type & 0x80u) != 0;
    int endpoint_halt_action = 0;
    int remote_wakeup_action = 0;
    uint8_t endpoint_address = (uint8_t)index;
    char message[96];
    hpe_status_t status = HPE_OK;
    if (!sim || !device || (length && !data)) {
        return HPE_ERR_ARGUMENT;
    }
    sim->active_protocol = HPE_PROTOCOL_USB;
    if ((device->state == HPE_USB_DEFAULT && address != 0) ||
        (device->state != HPE_USB_DEFAULT && address != device->address)) {
        status = HPE_ERR_NOT_FOUND;
    }

    if (status == HPE_OK && request_type == 0x80 && request == 6) {
        descriptor_length =
            make_descriptor(device, (uint8_t)(value >> 8), (uint8_t)value, index, buffer);
        if (!descriptor_length) {
            status = HPE_ERR_NOT_FOUND;
        }
        else {
            actual = (uint16_t)(descriptor_length < length ? descriptor_length : length);
            if (actual) {
                memcpy(data, buffer, actual);
            }
        }
    }
    else if (status == HPE_OK && request_type == 0x00 && request == 5) {
        if (device->state != HPE_USB_DEFAULT || value == 0 || value > 127 || index != 0 ||
            length != 0) {
            status = HPE_ERR_ARGUMENT;
        }
    }
    else if (status == HPE_OK && request_type == 0x00 && request == 9) {
        if (device->state == HPE_USB_DEFAULT || value > 1 || index != 0 || length != 0) {
            status = HPE_ERR_ARGUMENT;
        }
    }
    else if (status == HPE_OK && request_type == 0x80 && request == 8) {
        if (device->state == HPE_USB_DEFAULT || value != 0 || index != 0 || length != 1) {
            status = HPE_ERR_ARGUMENT;
        }
        buffer[0] = device->configuration;
        actual = status == HPE_OK ? 1 : 0;
        if (actual) {
            data[0] = buffer[0];
        }
    }
    else if (status == HPE_OK && request_type == 0x80 && request == 0) {
        if (value != 0 || index != 0 || length != 2) {
            status = HPE_ERR_ARGUMENT;
        }
        buffer[0] = device->remote_wakeup_enabled ? 0x02u : 0;
        buffer[1] = 0;
        actual = status == HPE_OK ? 2 : 0;
        if (actual) {
            memcpy(data, buffer, actual);
        }
    }
    else if (status == HPE_OK && request_type == 0x00 && (request == 1 || request == 3) &&
             value == 1) {
        if (device->state != HPE_USB_CONFIGURED || index != 0 || length != 0) {
            status = HPE_ERR_ARGUMENT;
        }
        else {
            remote_wakeup_action = request == 3 ? 1 : -1;
        }
    }
    else if (status == HPE_OK && request_type == 0x81 && request == 10) {
        if (device->state != HPE_USB_CONFIGURED || value != 0 || index != 0 || length != 1) {
            status = HPE_ERR_ARGUMENT;
        }
        buffer[0] = device->interface_alt_setting;
        actual = status == HPE_OK ? 1 : 0;
        if (actual) {
            data[0] = buffer[0];
        }
    }
    else if (status == HPE_OK && request_type == 0x01 && request == 11) {
        if (device->state != HPE_USB_CONFIGURED || value != 0 || index != 0 || length != 0) {
            status = HPE_ERR_ARGUMENT;
        }
    }
    else if (status == HPE_OK && request_type == 0x81 && request == 0) {
        if (device->state != HPE_USB_CONFIGURED || value != 0 || index != 0 || length != 2) {
            status = HPE_ERR_ARGUMENT;
        }
        buffer[0] = buffer[1] = 0;
        actual = status == HPE_OK ? 2 : 0;
        if (actual) {
            memcpy(data, buffer, actual);
        }
    }
    else if (status == HPE_OK && request_type == 0x82 && request == 0) {
        if (device->state == HPE_USB_DEFAULT || value != 0 || (index & 0xff00u) != 0 ||
            length != 2 ||
            (endpoint_address != 0x00u && endpoint_address != 0x80u &&
             (device->state != HPE_USB_CONFIGURED ||
              (endpoint_address != 0x01u && endpoint_address != 0x81u &&
               endpoint_address != 0x82u)))) {
            status = HPE_ERR_ARGUMENT;
        }
        else {
            buffer[0] = endpoint_address == 0x01u   ? device->bulk_out_halted
                        : endpoint_address == 0x81u ? device->bulk_in_halted
                        : endpoint_address == 0x82u ? device->interrupt_in_halted
                                                    : 0;
            buffer[1] = 0;
            actual = 2;
            memcpy(data, buffer, actual);
        }
    }
    else if (status == HPE_OK && request_type == 0x02 && (request == 1 || request == 3)) {
        if (device->state != HPE_USB_CONFIGURED || value != 0 || length != 0 ||
            (index & 0xff00u) != 0 ||
            (endpoint_address != 0x01u && endpoint_address != 0x81u && endpoint_address != 0x82u)) {
            status = HPE_ERR_ARGUMENT;
        }
        else {
            endpoint_halt_action = request == 3 ? 1 : -1;
        }
    }
    else if (status == HPE_OK) {
        status = HPE_ERR_NOT_FOUND;
    }

    argument = ((uint32_t)request_type << 24) | ((uint32_t)value << 8) | index;
    snprintf(message, sizeof(message),
             "SETUP bmRequestType=0x%02X bRequest=%u wValue=0x%04X wIndex=0x%04X wLength=%u",
             request_type, request, value, index, length);
    usb_emit_nrzi_packet(sim, address, 0, USB_PID_SETUP, NULL, 0);
    usb_emit(sim, HPE_EVENT_COMMAND, USB_PID_SETUP, address, 0, 8, request_type, 0, message);
    sim->events[sim->event_count - 1].protocol_command = request;
    sim->events[sim->event_count - 1].protocol_argument = argument;
    sim->events[sim->event_count - 1].protocol_response = status == HPE_OK ? actual : 0xffffffffu;
    {
        uint8_t setup_data[8] = {request_type,    request,
                                 (uint8_t)value,  (uint8_t)(value >> 8),
                                 (uint8_t)index,  (uint8_t)(index >> 8),
                                 (uint8_t)length, (uint8_t)(length >> 8)};
        usb_emit_nrzi_packet(sim, address, 0, USB_PID_DATA0, setup_data, sizeof(setup_data));
        usb_emit(sim, HPE_EVENT_DATA, USB_PID_DATA0, address, 0, 8, setup_data[0],
                 usb_crc16(setup_data, sizeof(setup_data)), "SETUP DATA0 payload");
        usb_emit_nrzi_packet(sim, address, 0, USB_PID_ACK, NULL, 0);
        usb_emit(sim, HPE_EVENT_ACK, USB_PID_ACK, address, 0, 0, 0, 0, "device ACK for SETUP");
    }
    if (status != HPE_OK) {
        usb_emit_nrzi_packet(sim, address, 0, USB_PID_STALL, NULL, 0);
        usb_emit(sim, HPE_EVENT_ERROR, USB_PID_STALL, address, 0, 0, 0, 0,
                 "control endpoint STALL; request unsupported or invalid in current state");
        if (actual_length) {
            *actual_length = 0;
        }
        return status;
    }

    if (in_direction && actual) {
        usb_emit_data_packets(sim, address, 0, data, actual, USB_PID_IN, "IN");
    }
    else if (!in_direction && length) {
        usb_emit_data_packets(sim, address, 0, data, length, USB_PID_OUT, "OUT");
    }
    if (in_direction) {
        usb_emit_data_packets(sim, address, 0, NULL, 0, USB_PID_OUT, "OUT status");
    }
    else {
        usb_emit_data_packets(sim, address, 0, NULL, 0, USB_PID_IN, "IN status");
    }

    if (request_type == 0x00 && request == 5) {
        device->address = (uint8_t)value;
        device->state = HPE_USB_ADDRESSED;
    }
    else if (request_type == 0x00 && request == 9) {
        device->configuration = (uint8_t)value;
        device->state = value ? HPE_USB_CONFIGURED : HPE_USB_ADDRESSED;
        device->interface_alt_setting = 0;
        device->remote_wakeup_enabled = 0;
        device->bulk_in_toggle = device->bulk_out_toggle = 0;
        device->interrupt_in_toggle = 0;
        device->bulk_in_halted = device->bulk_out_halted = 0;
        device->interrupt_in_halted = 0;
        device->interrupt_pending = 0;
        device->interrupt_next_poll_time_ns = 0;
        device->bulk_length = 0;
    }
    else if (request_type == 0x01 && request == 11) {
        device->interface_alt_setting = (uint8_t)value;
        device->bulk_in_toggle = device->bulk_out_toggle = 0;
    }
    else if (remote_wakeup_action) {
        device->remote_wakeup_enabled = remote_wakeup_action > 0;
    }
    else if (endpoint_halt_action) {
        uint8_t *halted = endpoint_address == 0x01u   ? &device->bulk_out_halted
                          : endpoint_address == 0x81u ? &device->bulk_in_halted
                                                      : &device->interrupt_in_halted;
        uint8_t *toggle = endpoint_address == 0x01u   ? &device->bulk_out_toggle
                          : endpoint_address == 0x81u ? &device->bulk_in_toggle
                                                      : &device->interrupt_in_toggle;
        *halted = endpoint_halt_action > 0;
        if (endpoint_halt_action < 0) {
            *toggle = 0;
        }
    }
    if (actual_length) {
        *actual_length = actual;
    }
    return HPE_OK;
}

/** @brief Implements the hpe_usb_bulk_transfer operation. */
hpe_status_t hpe_usb_bulk_transfer(hpe_sim_t *sim, size_t device_index, uint8_t address,
                                   uint8_t endpoint_address, uint8_t *data, uint16_t length,
                                   uint16_t *actual_length)
{
    hpe_usb_device_t *device = usb_device_at(sim, device_index);
    uint16_t actual = 0, offset = 0;
    int in_direction = (endpoint_address & 0x80u) != 0;
    uint8_t *payload;
    if (!sim || !device || (length && !data)) {
        return HPE_ERR_ARGUMENT;
    }
    sim->active_protocol = HPE_PROTOCOL_USB;
    if (device->state != HPE_USB_CONFIGURED || device->address != address ||
        (endpoint_address != 0x01u && endpoint_address != 0x81u)) {
        usb_emit_nrzi_packet(sim, address, endpoint_address & 0x0fu,
                             in_direction ? USB_PID_IN : USB_PID_OUT, NULL, 0);
        usb_emit_nrzi_packet(sim, address, endpoint_address & 0x0fu, USB_PID_STALL, NULL, 0);
        usb_emit(sim, HPE_EVENT_ERROR, USB_PID_STALL, address, endpoint_address & 0x0fu, 0, 0, 0,
                 "bulk endpoint STALL: invalid address, endpoint, or configuration");
        return HPE_ERR_IO;
    }
    if ((endpoint_address == 0x01u && device->bulk_out_halted) ||
        (endpoint_address == 0x81u && device->bulk_in_halted)) {
        usb_emit_nrzi_packet(sim, address, 1, in_direction ? USB_PID_IN : USB_PID_OUT, NULL, 0);
        usb_emit(sim, HPE_EVENT_START, in_direction ? USB_PID_IN : USB_PID_OUT, address, 1, 0, 0, 0,
                 "bulk token to halted endpoint");
        usb_emit_nrzi_packet(sim, address, 1, USB_PID_STALL, NULL, 0);
        usb_emit(sim, HPE_EVENT_ERROR, USB_PID_STALL, address, 1, 0, 0, 0,
                 "bulk endpoint STALL: ENDPOINT_HALT feature is set");
        if (actual_length) {
            *actual_length = 0;
        }
        return HPE_ERR_IO;
    }
    if (!in_direction) {
        if (length > device->bulk_capacity) {
            size_t capacity = length;
            uint8_t *next = (uint8_t *)realloc(device->bulk_data, capacity ? capacity : 1);
            if (!next) {
                return HPE_ERR_NOMEM;
            }
            device->bulk_data = next;
            device->bulk_capacity = capacity;
        }
        if (length) {
            memcpy(device->bulk_data, data, length);
        }
        device->bulk_length = length;
        actual = length;
        payload = data;
    }
    else {
        if (!device->bulk_length) {
            usb_emit_nrzi_packet(sim, address, 1, USB_PID_IN, NULL, 0);
            usb_emit(sim, HPE_EVENT_START, USB_PID_IN, address, 1, 0, 0, 0, "bulk IN token");
            usb_emit_nrzi_packet(sim, address, 1, 0x5au, NULL, 0);
            usb_emit(sim, HPE_EVENT_NACK, 0x5au, address, 1, 0, 0, 0,
                     "bulk endpoint has no queued echo data");
            if (actual_length) {
                *actual_length = 0;
            }
            return HPE_ERR_NACK;
        }
        actual = (uint16_t)(device->bulk_length < length ? device->bulk_length : length);
        if (actual) {
            memcpy(data, device->bulk_data, actual);
        }
        memmove(device->bulk_data, device->bulk_data + actual, device->bulk_length - actual);
        device->bulk_length -= actual;
        payload = data;
    }
    do {
        uint16_t packet_length = (uint16_t)(actual - offset);
        uint16_t crc;
        uint8_t *toggle = in_direction ? &device->bulk_in_toggle : &device->bulk_out_toggle;
        char message[80];
        if (packet_length > 64) {
            packet_length = 64;
        }
        snprintf(message, sizeof(message), "bulk %s token endpoint 1", in_direction ? "IN" : "OUT");
        usb_emit_nrzi_packet(sim, address, 1, in_direction ? USB_PID_IN : USB_PID_OUT, NULL, 0);
        usb_emit(sim, HPE_EVENT_START, in_direction ? USB_PID_IN : USB_PID_OUT, address, 1, 0, 0, 0,
                 message);
        crc = usb_crc16(packet_length ? payload + offset : NULL, packet_length);
        usb_emit_nrzi_packet(sim, address, 1, *toggle ? USB_PID_DATA1 : USB_PID_DATA0,
                             packet_length ? payload + offset : NULL, packet_length);
        usb_emit(sim, HPE_EVENT_DATA, *toggle ? USB_PID_DATA1 : USB_PID_DATA0, address, 1,
                 packet_length, packet_length ? payload[offset] : 0, crc, "USB bulk data packet");
        usb_emit_nrzi_packet(sim, address, 1, USB_PID_ACK, NULL, 0);
        usb_emit(sim, HPE_EVENT_ACK, USB_PID_ACK, address, 1, 0, 0, 0,
                 in_direction ? "host ACK" : "device ACK");
        *toggle ^= 1u;
        offset = (uint16_t)(offset + packet_length);
        if (!packet_length || offset >= actual) {
            break;
        }
    }
    while (1);
    if (actual_length) {
        *actual_length = actual;
    }
    return HPE_OK;
}

/** @brief Implements the hpe_usb_interrupt_queue operation. */
hpe_status_t hpe_usb_interrupt_queue(hpe_sim_t *sim, size_t device_index, const uint8_t *data,
                                     uint8_t length)
{
    hpe_usb_device_t *device = usb_device_at(sim, device_index);
    char message[96];
    if (!sim || !device || !data || length == 0 || length > sizeof(device->interrupt_data)) {
        return HPE_ERR_ARGUMENT;
    }
    if (device->interrupt_pending) {
        return HPE_ERR_RANGE;
    }
    memcpy(device->interrupt_data, data, length);
    device->interrupt_length = length;
    device->interrupt_pending = 1;
    sim->active_protocol = HPE_PROTOCOL_USB;
    snprintf(message, sizeof(message), "USB interrupt report queued (%u byte(s))", length);
    hpe_emit(sim, HPE_EVENT_INFO, 0, 0, message);
    return HPE_OK;
}

/** @brief Implements the hpe_usb_interrupt_poll operation. */
hpe_status_t hpe_usb_interrupt_poll(hpe_sim_t *sim, size_t device_index, uint8_t address,
                                    uint8_t *data, uint8_t capacity, uint8_t *actual_length)
{
    hpe_usb_device_t *device = usb_device_at(sim, device_index);
    uint8_t toggle;
    uint16_t crc;
    if (!sim || !device || !data || !capacity) {
        return HPE_ERR_ARGUMENT;
    }
    sim->active_protocol = HPE_PROTOCOL_USB;
    if (device->state != HPE_USB_CONFIGURED || device->address != address) {
        usb_emit_nrzi_packet(sim, address, 2, USB_PID_IN, NULL, 0);
        usb_emit_nrzi_packet(sim, address, 2, USB_PID_STALL, NULL, 0);
        usb_emit(sim, HPE_EVENT_ERROR, USB_PID_STALL, address, 2, 0, 0, 0,
                 "interrupt endpoint STALL: invalid address or configuration");
        if (actual_length) {
            *actual_length = 0;
        }
        return HPE_ERR_IO;
    }
    if (device->interrupt_next_poll_time_ns > sim->time_ns) {
        sim->time_ns = device->interrupt_next_poll_time_ns;
    }
    device->interrupt_next_poll_time_ns = sim->time_ns + 10000000u;
    if (device->interrupt_in_halted) {
        usb_emit_nrzi_packet(sim, address, 2, USB_PID_IN, NULL, 0);
        usb_emit_nrzi_packet(sim, address, 2, USB_PID_STALL, NULL, 0);
        usb_emit(sim, HPE_EVENT_ERROR, USB_PID_STALL, address, 2, 0, 0, 0,
                 "interrupt endpoint STALL: ENDPOINT_HALT feature is set");
        if (actual_length) {
            *actual_length = 0;
        }
        return HPE_ERR_IO;
    }
    if (device->interrupt_pending && capacity < device->interrupt_length) {
        if (actual_length) {
            *actual_length = 0;
        }
        return HPE_ERR_RANGE;
    }
    usb_emit_nrzi_packet(sim, address, 2, USB_PID_IN, NULL, 0);
    usb_emit(sim, HPE_EVENT_START, USB_PID_IN, address, 2, 0, 0, 0, "interrupt IN poll endpoint 2");
    if (!device->interrupt_pending) {
        usb_emit_nrzi_packet(sim, address, 2, USB_PID_NAK, NULL, 0);
        usb_emit(sim, HPE_EVENT_NACK, USB_PID_NAK, address, 2, 0, 0, 0,
                 "interrupt endpoint has no queued report");
        if (actual_length) {
            *actual_length = 0;
        }
        return HPE_ERR_NACK;
    }
    memcpy(data, device->interrupt_data, device->interrupt_length);
    toggle = device->interrupt_in_toggle;
    crc = usb_crc16(data, device->interrupt_length);
    usb_emit_nrzi_packet(sim, address, 2, toggle ? USB_PID_DATA1 : USB_PID_DATA0, data,
                         device->interrupt_length);
    usb_emit(sim, HPE_EVENT_DATA, toggle ? USB_PID_DATA1 : USB_PID_DATA0, address, 2,
             device->interrupt_length, data[0], crc, "USB interrupt data packet");
    usb_emit_nrzi_packet(sim, address, 2, USB_PID_ACK, NULL, 0);
    usb_emit(sim, HPE_EVENT_ACK, USB_PID_ACK, address, 2, 0, 0, 0, "host ACK for interrupt data");
    device->interrupt_in_toggle ^= 1u;
    device->interrupt_pending = 0;
    if (actual_length) {
        *actual_length = device->interrupt_length;
    }
    return HPE_OK;
}

/** @brief Implements the hpe_usb_enumerate operation. */
hpe_status_t hpe_usb_enumerate(hpe_sim_t *sim, size_t device_index)
{
    hpe_usb_device_t *device = usb_device_at(sim, device_index);
    uint8_t data[256];
    uint16_t actual = 0;
    hpe_status_t status;
    uint8_t address = (uint8_t)(device_index + 1);
    char message[96];
    if (!sim || !device || address > 127) {
        return HPE_ERR_ARGUMENT;
    }
    sim->active_protocol = HPE_PROTOCOL_USB;
    device->state = HPE_USB_DEFAULT;
    device->address = 0;
    device->configuration = 0;
    device->interface_alt_setting = 0;
    device->remote_wakeup_enabled = 0;
    device->bulk_in_toggle = 0;
    device->bulk_out_toggle = 0;
    device->bulk_in_halted = 0;
    device->bulk_out_halted = 0;
    device->interrupt_in_halted = 0;
    device->interrupt_in_toggle = 0;
    device->interrupt_pending = 0;
    device->interrupt_next_poll_time_ns = 0;
    device->bulk_length = 0;
    sim->time_ns += 10000000;
    sim->usb_frame_number = 0;
    sim->usb_next_sof_time_ns = sim->time_ns + 1000000u;
    snprintf(message, sizeof(message), "%s bus reset and SE0 recovery", device->name);
    hpe_emit(sim, HPE_EVENT_INFO, 0, 0, message);
    status = hpe_usb_control_transfer(sim, device_index, 0, 0x80, 6, 0x0100, 0, data, 8, &actual);
    if (status != HPE_OK || actual != 8) {
        return HPE_ERR_IO;
    }
    status = hpe_usb_control_transfer(sim, device_index, 0, 0x00, 5, address, 0, NULL, 0, &actual);
    if (status != HPE_OK) {
        return status;
    }
    sim->time_ns += 2000000; /* SET_ADDRESS recovery interval before using the new address. */
    status =
        hpe_usb_control_transfer(sim, device_index, address, 0x80, 6, 0x0100, 0, data, 18, &actual);
    if (status != HPE_OK || actual != 18) {
        return HPE_ERR_IO;
    }
    status =
        hpe_usb_control_transfer(sim, device_index, address, 0x80, 6, 0x0200, 0, data, 9, &actual);
    if (status != HPE_OK || actual != 9) {
        return HPE_ERR_IO;
    }
    status =
        hpe_usb_control_transfer(sim, device_index, address, 0x80, 6, 0x0200, 0, data, 32, &actual);
    if (status != HPE_OK || actual != 32) {
        return HPE_ERR_IO;
    }
    status =
        hpe_usb_control_transfer(sim, device_index, address, 0x80, 6, 0x0300, 0, data, 4, &actual);
    if (status != HPE_OK || actual != 4) {
        return HPE_ERR_IO;
    }
    status = hpe_usb_control_transfer(sim, device_index, address, 0x80, 6, 0x0301, 0x0409, data, 96,
                                      &actual);
    if (status != HPE_OK) {
        return status;
    }
    status = hpe_usb_control_transfer(sim, device_index, address, 0x80, 6, 0x0302, 0x0409, data, 96,
                                      &actual);
    if (status != HPE_OK) {
        return status;
    }
    status = hpe_usb_control_transfer(sim, device_index, address, 0x00, 9, 1, 0, NULL, 0, &actual);
    if (status != HPE_OK) {
        return status;
    }
    snprintf(message, sizeof(message), "%s configured at USB address %u", device->name, address);
    hpe_emit(sim, HPE_EVENT_INFO, address, 0, message);
    return HPE_OK;
}
