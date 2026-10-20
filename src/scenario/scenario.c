/*
 * File: scenario.c
 * Purpose: Project-owned implementation and interface for src / scenario / scenario.c.
 * Author: Elrond-Xie
 * Copyright (c) 2026 Elrond-Xie. SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0.
 */

#include "internal.h"

#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

/** @brief Implements the parse_number operation. */
static int parse_number(const char *s, unsigned long max, unsigned long *out)
{
    char *end;
    unsigned long value;
    if (!s || !s[0] || s[0] == '-') {
        return 0;
    }
    errno = 0;
    value = strtoul(s, &end, 0);
    if (errno || *end || value > max) {
        return 0;
    }
    *out = value;
    return 1;
}

/** @brief Implements the split_line operation. */
static size_t split_line(char *line, char **tokens, size_t capacity)
{
    size_t count = 0;
    char *p = line;
    while (*p) {
        while (*p == ' ' || *p == '\t' || *p == '\r' || *p == '\n') {
            ++p;
        }
        if (!*p || *p == '#') {
            break;
        }
        if (count == capacity) {
            return capacity + 1;
        }
        tokens[count++] = p;
        while (*p && *p != ' ' && *p != '\t' && *p != '\r' && *p != '\n' && *p != '#') {
            ++p;
        }
        if (*p == '#') {
            *p = '\0';
            break;
        }
        if (*p) {
            *p++ = '\0';
        }
    }
    return count;
}

/** @brief Implements the line_error operation. */
static hpe_status_t line_error(char *error, size_t cap, size_t line, const char *why)
{
    if (error && cap) {
        snprintf(error, cap, "line %lu: %s", (unsigned long)line, why);
    }
    return HPE_ERR_PARSE;
}

/** @brief Implements the command operation. */
static hpe_status_t command(hpe_sim_t *sim, char **t, size_t n, size_t line, char *error,
                            size_t cap)
{
    unsigned long v[512];
    size_t i;
    hpe_status_t status;
    char msg[96];
    if (!n) {
        return HPE_OK;
    }
    sim->active_protocol = HPE_PROTOCOL_I2C;
    if (strcmp(t[0], "pcie-device") == 0) {
        if (n != 13 || !parse_number(t[2], 255, &v[0]) || !parse_number(t[3], 31, &v[1]) ||
            !parse_number(t[4], 7, &v[2]) || !parse_number(t[5], 65535, &v[3]) ||
            !parse_number(t[6], 65535, &v[4]) || !parse_number(t[7], 255, &v[5]) ||
            !parse_number(t[8], 255, &v[6]) || !parse_number(t[9], 255, &v[7]) ||
            !parse_number(t[10], 16u * 1024u * 1024u, &v[8]) || !parse_number(t[11], 7, &v[9]) ||
            !parse_number(t[12], 32, &v[10])) {
            return line_error(error, cap, line,
                              "usage: pcie-device NAME BUS DEVICE FUNCTION VID DID CLASS SUBCLASS "
                              "PROGIF BAR0_SIZE GEN LANES");
        }
        status =
            hpe_pcie_add_endpoint(sim, t[1], (uint8_t)v[0], (uint8_t)v[1], (uint8_t)v[2],
                                  (uint16_t)v[3], (uint16_t)v[4], (uint8_t)v[5], (uint8_t)v[6],
                                  (uint8_t)v[7], (uint32_t)v[8], (uint8_t)v[9], (uint8_t)v[10]);
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        sim->active_protocol = HPE_PROTOCOL_PCIE;
        snprintf(msg, sizeof(msg), "PCIe endpoint %s at %lu:%lu.%lu", t[1], v[0], v[1], v[2]);
        hpe_emit(sim, HPE_EVENT_INFO, (uint8_t)v[1], 0, msg);
        return HPE_OK;
    }
    if (strcmp(t[0], "pcie-enumerate") == 0) {
        hpe_pcie_bdf_t devices[256];
        size_t found = 0, j;
        if (n != 1) {
            return line_error(error, cap, line, "usage: pcie-enumerate");
        }
        status = hpe_pcie_enumerate(sim, devices, sizeof(devices) / sizeof(devices[0]), &found);
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        sim->active_protocol = HPE_PROTOCOL_PCIE;
        for (j = 0; j < found; ++j) {
            snprintf(msg, sizeof(msg), "enumerated %u:%u.%u VID:DID=%04X:%04X", devices[j].bus,
                     devices[j].device, devices[j].function, devices[j].vendor_id,
                     devices[j].device_id);
            hpe_emit(sim, HPE_EVENT_INFO, (uint8_t)devices[j].device, 0, msg);
        }
        return HPE_OK;
    }
    if (strcmp(t[0], "pcie-corrupt-next-tlp") == 0) {
        if (n != 1) {
            return line_error(error, cap, line, "usage: pcie-corrupt-next-tlp");
        }
        return hpe_pcie_corrupt_next_tlp(sim);
    }
    if (strcmp(t[0], "pcie-cfg-read") == 0 || strcmp(t[0], "pcie-cfg-write") == 0 ||
        strcmp(t[0], "pcie-mmio-read") == 0 || strcmp(t[0], "pcie-mmio-write") == 0) {
        int is_config = strstr(t[0], "cfg") != NULL;
        int is_write = strstr(t[0], "write") != NULL;
        uint8_t width;
        uint32_t value = 0;
        size_t expected = is_write ? 5 : 4;
        if (n != expected || !parse_number(t[1], 255, &v[0]) ||
            !parse_number(t[2], is_config ? 255 : 0xfffffffful, &v[1]) ||
            !parse_number(t[3], 4, &v[2]) || (v[2] != 1 && v[2] != 2 && v[2] != 4) ||
            (is_write && !parse_number(t[4], 0xfffffffful, &v[3]))) {
            return line_error(error, cap, line,
                              "usage: pcie-cfg|mmio-read INDEX OFFSET WIDTH or ...-write INDEX "
                              "OFFSET WIDTH VALUE");
        }
        width = (uint8_t)v[2];
        if (is_write) {
            value = (uint32_t)v[3];
        }
        if (is_config) {
            status = is_write
                         ? hpe_pcie_config_write(sim, (size_t)v[0], (uint16_t)v[1], width, value)
                         : hpe_pcie_config_read(sim, (size_t)v[0], (uint16_t)v[1], width, &value);
        }
        else {
            status = is_write
                         ? hpe_pcie_mmio_write(sim, (size_t)v[0], (uint32_t)v[1], width, value)
                         : hpe_pcie_mmio_read(sim, (size_t)v[0], (uint32_t)v[1], width, &value);
        }
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        return HPE_OK;
    }
    if (strcmp(t[0], "usb-device") == 0) {
        if (n != 6 || !parse_number(t[2], 65535, &v[0]) || !parse_number(t[3], 65535, &v[1])) {
            return line_error(error, cap, line,
                              "usage: usb-device NAME VID PID MANUFACTURER PRODUCT");
        }
        status = hpe_usb_device_add(sim, t[1], (uint16_t)v[0], (uint16_t)v[1], t[4], t[5]);
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        sim->active_protocol = HPE_PROTOCOL_USB;
        snprintf(msg, sizeof(msg), "USB virtual device %s VID=%04lX PID=%04lX", t[1], v[0], v[1]);
        hpe_emit(sim, HPE_EVENT_INFO, (uint8_t)v[0], 0, msg);
        return HPE_OK;
    }
    if (strcmp(t[0], "usb-enumerate") == 0) {
        if (n != 2 || !parse_number(t[1], 63, &v[0])) {
            return line_error(error, cap, line, "usage: usb-enumerate DEVICE_INDEX");
        }
        status = hpe_usb_enumerate(sim, (size_t)v[0]);
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        return HPE_OK;
    }
    if (strcmp(t[0], "usb-control") == 0) {
        uint8_t data[256];
        uint16_t actual = 0;
        size_t j;
        uint8_t device_index, address, request_type, request;
        uint16_t value, descriptor_index, length;
        if (n != 8 || !parse_number(t[1], 63, &v[0]) || !parse_number(t[2], 127, &v[1]) ||
            !parse_number(t[3], 255, &v[2]) || !parse_number(t[4], 255, &v[3]) ||
            !parse_number(t[5], 65535, &v[4]) || !parse_number(t[6], 65535, &v[5]) ||
            !parse_number(t[7], sizeof(data), &v[6])) {
            return line_error(error, cap, line,
                              "usage: usb-control DEVICE_INDEX ADDRESS BMREQUESTTYPE BREQUEST "
                              "VALUE INDEX LENGTH");
        }
        device_index = (uint8_t)v[0];
        address = (uint8_t)v[1];
        request_type = (uint8_t)v[2];
        request = (uint8_t)v[3];
        value = (uint16_t)v[4];
        descriptor_index = (uint16_t)v[5];
        length = (uint16_t)v[6];
        status = hpe_usb_control_transfer(sim, device_index, address, request_type, request, value,
                                          descriptor_index, data, length, &actual);
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        sim->active_protocol = HPE_PROTOCOL_USB;
        for (j = 0; j < actual && j < 16; ++j) {
            snprintf(msg, sizeof(msg), "control data[%lu] = 0x%02X", (unsigned long)j, data[j]);
            hpe_emit(sim, HPE_EVENT_INFO, data[j], 1, msg);
        }
        return HPE_OK;
    }
    if (strcmp(t[0], "usb-bulk-out") == 0) {
        uint8_t data[508], device_index, address;
        uint16_t actual = 0;
        if (n < 4 || n > 511 || !parse_number(t[1], 63, &v[0]) || !parse_number(t[2], 127, &v[1])) {
            return line_error(error, cap, line, "usage: usb-bulk-out DEVICE_INDEX ADDRESS BYTE...");
        }
        device_index = (uint8_t)v[0];
        address = (uint8_t)v[1];
        for (i = 0; i < n - 3; ++i) {
            if (!parse_number(t[i + 3], 255, &v[2])) {
                return line_error(error, cap, line, "invalid USB bulk byte value");
            }
            data[i] = (uint8_t)v[2];
        }
        status = hpe_usb_bulk_transfer(sim, device_index, address, 0x01, data, (uint16_t)(n - 3),
                                       &actual);
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        return HPE_OK;
    }
    if (strcmp(t[0], "usb-bulk-in") == 0) {
        uint8_t data[512], device_index, address;
        uint16_t actual = 0, requested;
        if (n != 4 || !parse_number(t[1], 63, &v[0]) || !parse_number(t[2], 127, &v[1]) ||
            !parse_number(t[3], sizeof(data), &v[2])) {
            return line_error(error, cap, line, "usage: usb-bulk-in DEVICE_INDEX ADDRESS LENGTH");
        }
        device_index = (uint8_t)v[0];
        address = (uint8_t)v[1];
        requested = (uint16_t)v[2];
        status = hpe_usb_bulk_transfer(sim, device_index, address, 0x81, data, requested, &actual);
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        sim->active_protocol = HPE_PROTOCOL_USB;
        for (i = 0; i < actual && i < 16; ++i) {
            snprintf(msg, sizeof(msg), "bulk IN byte %lu = 0x%02X%s", (unsigned long)i, data[i],
                     actual > 16 ? " (first 16 shown)" : "");
            hpe_emit(sim, HPE_EVENT_INFO, data[i], 1, msg);
        }
        return HPE_OK;
    }
    if (strcmp(t[0], "usb-interrupt-queue") == 0) {
        uint8_t data[8];
        if (n < 3 || n > 10 || !parse_number(t[1], 63, &v[0])) {
            return line_error(error, cap, line, "usage: usb-interrupt-queue DEVICE_INDEX BYTE...");
        }
        for (i = 0; i < n - 2; ++i) {
            if (!parse_number(t[i + 2], 255, &v[1])) {
                return line_error(error, cap, line, "invalid USB interrupt byte value");
            }
            data[i] = (uint8_t)v[1];
        }
        status = hpe_usb_interrupt_queue(sim, (size_t)v[0], data, (uint8_t)(n - 2));
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        return HPE_OK;
    }
    if (strcmp(t[0], "usb-interrupt-poll") == 0) {
        uint8_t data[8], device_index, address, actual = 0;
        if (n != 3 || !parse_number(t[1], 63, &v[0]) || !parse_number(t[2], 127, &v[1])) {
            return line_error(error, cap, line, "usage: usb-interrupt-poll DEVICE_INDEX ADDRESS");
        }
        device_index = (uint8_t)v[0];
        address = (uint8_t)v[1];
        status = hpe_usb_interrupt_poll(sim, device_index, address, data, sizeof(data), &actual);
        if (status == HPE_ERR_NACK) {
            return HPE_OK;
        }
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        sim->active_protocol = HPE_PROTOCOL_USB;
        for (i = 0; i < actual; ++i) {
            snprintf(msg, sizeof(msg), "interrupt IN byte %lu = 0x%02X", (unsigned long)i, data[i]);
            hpe_emit(sim, HPE_EVENT_INFO, data[i], 1, msg);
        }
        return HPE_OK;
    }
    if (strcmp(t[0], "sd-card") == 0 || strcmp(t[0], "emmc-card") == 0) {
        hpe_protocol_t protocol =
            strcmp(t[0], "sd-card") == 0 ? HPE_PROTOCOL_SD : HPE_PROTOCOL_EMMC;
        if (n != 4 || !parse_number(t[2], 4194304, &v[0]) || !v[0] ||
            !parse_number(t[3], 255, &v[1])) {
            return line_error(error, cap, line, "usage: sd-card|emmc-card NAME BLOCKS FILL");
        }
        status = hpe_block_card_add(sim, protocol, t[1], (uint32_t)v[0], (uint8_t)v[1]);
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        sim->active_protocol = protocol;
        snprintf(msg, sizeof(msg), "%s card %s: %lu x 512-byte blocks", hpe_protocol_name(protocol),
                 t[1], v[0]);
        hpe_emit(sim, HPE_EVENT_INFO, 0, 0, msg);
        return HPE_OK;
    }
    if (strcmp(t[0], "card-cmd") == 0) {
        uint32_t response;
        if (n != 4 || !parse_number(t[1], 63, &v[0]) || !parse_number(t[2], 63, &v[1]) ||
            !parse_number(t[3], 0xfffffffful, &v[2])) {
            return line_error(error, cap, line, "usage: card-cmd CARD_INDEX COMMAND ARGUMENT");
        }
        status =
            hpe_block_card_command(sim, (size_t)v[0], (uint8_t)v[1], (uint32_t)v[2], &response);
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        return HPE_OK;
    }
    if (strcmp(t[0], "card-register") == 0) {
        uint8_t data[512];
        size_t length = 0, j, display_length;
        hpe_card_register_t reg;
        const char *name;
        if (n != 3 || !parse_number(t[1], 63, &v[0])) {
            return line_error(error, cap, line, "usage: card-register CARD_INDEX CID|CSD|EXT_CSD");
        }
        if (strcmp(t[2], "CID") == 0) {
            reg = HPE_CARD_REGISTER_CID;
            name = "CID";
        }
        else if (strcmp(t[2], "CSD") == 0) {
            reg = HPE_CARD_REGISTER_CSD;
            name = "CSD";
        }
        else if (strcmp(t[2], "EXT_CSD") == 0) {
            reg = HPE_CARD_REGISTER_EXT_CSD;
            name = "EXT_CSD";
        }
        else {
            return line_error(error, cap, line, "register must be CID, CSD, or EXT_CSD");
        }
        status = hpe_block_card_get_register(sim, (size_t)v[0], reg, data, sizeof(data), &length);
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        sim->active_protocol = sim->block_cards[v[0]]->protocol;
        if (reg == HPE_CARD_REGISTER_EXT_CSD) {
            uint32_t sectors = (uint32_t)data[212] | ((uint32_t)data[213] << 8) |
                               ((uint32_t)data[214] << 16) | ((uint32_t)data[215] << 24);
            snprintf(msg, sizeof(msg),
                     "EXT_CSD rev=0x%02X card_type=0x%02X BUS_WIDTH=%u HS_TIMING=%u SEC_COUNT=%u",
                     data[192], data[196], data[183], data[185], sectors);
        }
        else {
            int used = snprintf(msg, sizeof(msg), "%s =", name);
            display_length = length;
            for (j = 0; j < display_length && used > 0 && (size_t)used + 4 < sizeof(msg); ++j) {
                used += snprintf(msg + used, sizeof(msg) - (size_t)used, " %02X", data[j]);
            }
        }
        hpe_emit(sim, HPE_EVENT_INFO, 0, 0, msg);
        return HPE_OK;
    }
    if (strcmp(t[0], "card-read") == 0) {
        uint8_t data[512];
        size_t j;
        if (n != 3 || !parse_number(t[1], 63, &v[0]) || !parse_number(t[2], 0xfffffffful, &v[1])) {
            return line_error(error, cap, line, "usage: card-read CARD_INDEX BLOCK");
        }
        status = hpe_block_card_read(sim, (size_t)v[0], (uint32_t)v[1], data);
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        sim->active_protocol = sim->block_cards[v[0]]->protocol;
        for (j = 0; j < 16; ++j) {
            snprintf(msg, sizeof(msg), "block %lu byte %lu = 0x%02X (first 16 shown)", v[1],
                     (unsigned long)j, data[j]);
            hpe_emit(sim, HPE_EVENT_INFO, data[j], 1, msg);
        }
        return HPE_OK;
    }
    if (strcmp(t[0], "card-write") == 0) {
        uint8_t data[512];
        if (n != 4 || !parse_number(t[1], 63, &v[0]) || !parse_number(t[2], 0xfffffffful, &v[1]) ||
            !parse_number(t[3], 255, &v[2])) {
            return line_error(error, cap, line, "usage: card-write CARD_INDEX BLOCK FILL_BYTE");
        }
        memset(data, (uint8_t)v[2], sizeof(data));
        status = hpe_block_card_write(sim, (size_t)v[0], (uint32_t)v[1], data);
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        return HPE_OK;
    }
    if (strcmp(t[0], "spi-device") == 0) {
        if (n != 5 || !parse_number(t[2], 255, &v[0]) ||
            !parse_number(t[3], 16u * 1024u * 1024u, &v[1]) || !v[1] ||
            !parse_number(t[4], 255, &v[2])) {
            return line_error(error, cap, line, "usage: spi-device NAME CS MEMORY_SIZE FILL");
        }
        status = hpe_spi_add_flash_device(sim, t[1], (uint8_t)v[0], (size_t)v[1], (uint8_t)v[2]);
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        sim->active_protocol = HPE_PROTOCOL_SPI;
        snprintf(msg, sizeof(msg), "SPI NOR %s on CS%lu (%lu bytes)", t[1], v[0], v[1]);
        hpe_emit(sim, HPE_EVENT_INFO, (uint8_t)v[0], 0, msg);
        return HPE_OK;
    }
    if (strcmp(t[0], "spi-transfer") == 0) {
        uint8_t transmit[508], receive[508];
        size_t length;
        uint8_t chip_select, mode;
        if (n < 4 || n > 511 || !parse_number(t[1], 255, &v[0]) || !parse_number(t[2], 3, &v[1])) {
            return line_error(error, cap, line, "usage: spi-transfer CS MODE BYTE [BYTE ...]");
        }
        length = n - 3;
        chip_select = (uint8_t)v[0];
        mode = (uint8_t)v[1];
        for (i = 0; i < length; ++i) {
            if (!parse_number(t[i + 3], 255, &v[0])) {
                return line_error(error, cap, line, "invalid SPI byte value");
            }
            transmit[i] = (uint8_t)v[0];
        }
        status = hpe_spi_transfer(sim, chip_select, mode, transmit, receive, length);
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        return HPE_OK;
    }
    if (strcmp(t[0], "device") == 0) {
        if (n != 5 || !parse_number(t[2], HPE_I2C_MAX_ADDRESS, &v[0]) ||
            !parse_number(t[3], 65536, &v[1]) || !v[1] || !parse_number(t[4], 255, &v[2])) {
            return line_error(error, cap, line, "usage: device NAME ADDRESS REGISTER_COUNT FILL");
        }
        status = hpe_i2c_add_register_device(sim, t[1], (uint8_t)v[0], (size_t)v[1], (uint8_t)v[2]);
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        snprintf(msg, sizeof(msg), "device %s at 0x%02lX (%lu registers)", t[1], v[0], v[1]);
        hpe_emit(sim, HPE_EVENT_INFO, (uint8_t)v[0], 0, msg);
        return HPE_OK;
    }
    if (strcmp(t[0], "probe") == 0 || strcmp(t[0], "expect-nack") == 0) {
        int expect_nack = strcmp(t[0], "expect-nack") == 0;
        if (n != 2 || !parse_number(t[1], HPE_I2C_MAX_ADDRESS, &v[0])) {
            return line_error(error, cap, line, "usage: probe|expect-nack ADDRESS");
        }
        status = hpe_i2c_probe(sim, (uint8_t)v[0]);
        if ((status == HPE_ERR_NACK) != expect_nack) {
            return line_error(error, cap, line,
                              expect_nack ? "device unexpectedly ACKed" : "probe received NACK");
        }
        return HPE_OK;
    }
    if (strcmp(t[0], "scan") == 0) {
        uint8_t addresses[128];
        size_t found = 0;
        if (n != 1) {
            return line_error(error, cap, line, "usage: scan");
        }
        status = hpe_i2c_scan(sim, addresses, sizeof(addresses), &found);
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        snprintf(msg, sizeof(msg), "scan found %lu address(es)", (unsigned long)found);
        hpe_emit(sim, HPE_EVENT_INFO, (uint8_t)found, 0, msg);
        for (i = 0; i < found; ++i) {
            snprintf(msg, sizeof(msg), "found I2C address 0x%02X", addresses[i]);
            hpe_emit(sim, HPE_EVENT_INFO, addresses[i], 0, msg);
        }
        return HPE_OK;
    }
    if (strcmp(t[0], "write") == 0) {
        uint8_t bytes[510];
        if (n < 3 || n > 512 || !parse_number(t[1], HPE_I2C_MAX_ADDRESS, &v[0])) {
            return line_error(error, cap, line, "usage: write ADDRESS BYTE [BYTE ...]");
        }
        for (i = 2; i < n; ++i) {
            if (!parse_number(t[i], 255, &v[i])) {
                return line_error(error, cap, line, "invalid byte value");
            }
            bytes[i - 2] = (uint8_t)v[i];
        }
        status = hpe_i2c_write(sim, (uint8_t)v[0], bytes, n - 2);
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        return HPE_OK;
    }
    if (strcmp(t[0], "readreg") == 0) {
        uint8_t bytes[256];
        size_t length, j;
        if (n != 4 || !parse_number(t[1], HPE_I2C_MAX_ADDRESS, &v[0]) ||
            !parse_number(t[2], 255, &v[1]) || !parse_number(t[3], sizeof(bytes), &v[2]) || !v[2]) {
            return line_error(error, cap, line, "usage: readreg ADDRESS REGISTER LENGTH");
        }
        length = (size_t)v[2];
        status = hpe_i2c_read_register(sim, (uint8_t)v[0], (uint8_t)v[1], bytes, length);
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        for (j = 0; j < length; ++j) {
            snprintf(msg, sizeof(msg), "read [0x%02lX] = 0x%02X", v[1] + (unsigned long)j,
                     bytes[j]);
            hpe_emit(sim, HPE_EVENT_INFO, bytes[j], 1, msg);
        }
        return HPE_OK;
    }
    if (strcmp(t[0], "nack") == 0) {
        if (n != 3 || !parse_number(t[1], HPE_I2C_MAX_ADDRESS, &v[0]) ||
            !parse_number(t[2], 1000000, &v[1])) {
            return line_error(error, cap, line, "usage: nack ADDRESS COUNT");
        }
        hpe_i2c_inject_nack(sim, (uint8_t)v[0], (unsigned)v[1]);
        snprintf(msg, sizeof(msg), "next %lu address attempt(s) at 0x%02lX will NACK", v[1], v[0]);
        hpe_emit(sim, HPE_EVENT_INFO, (uint8_t)v[0], 0, msg);
        return HPE_OK;
    }
    if (strcmp(t[0], "set") == 0) {
        if (n != 4 || !parse_number(t[1], HPE_I2C_MAX_ADDRESS, &v[0]) ||
            !parse_number(t[2], 65535, &v[1]) || !parse_number(t[3], 255, &v[2])) {
            return line_error(error, cap, line, "usage: set ADDRESS REGISTER VALUE");
        }
        status = hpe_i2c_set_register(sim, (uint8_t)v[0], (uint16_t)v[1], (uint8_t)v[2]);
        if (status != HPE_OK) {
            return line_error(error, cap, line, hpe_status_string(status));
        }
        return HPE_OK;
    }
    return line_error(error, cap, line, "unknown command");
}

/** @brief Implements the hpe_scenario_run_text operation. */
hpe_status_t hpe_scenario_run_text(hpe_sim_t *sim, const char *text, char *error,
                                   size_t error_capacity)
{
    char *copy, *line_start, *line_end;
    size_t line_no = 0;
    if (!sim || !text) {
        return HPE_ERR_ARGUMENT;
    }
    if (error && error_capacity) {
        error[0] = '\0';
    }
    copy = (char *)malloc(strlen(text) + 1);
    if (!copy) {
        return HPE_ERR_NOMEM;
    }
    strcpy(copy, text);
    line_start = copy;
    while (line_start && *line_start) {
        char *tokens[512];
        size_t count;
        hpe_status_t status;
        ++line_no;
        line_end = strchr(line_start, '\n');
        if (line_end) {
            *line_end = '\0';
        }
        count = split_line(line_start, tokens, sizeof(tokens) / sizeof(tokens[0]));
        if (count > sizeof(tokens) / sizeof(tokens[0])) {
            status = line_error(error, error_capacity, line_no, "too many tokens");
        }
        else {
            status = command(sim, tokens, count, line_no, error, error_capacity);
        }
        if (status != HPE_OK) {
            free(copy);
            return status;
        }
        line_start = line_end ? line_end + 1 : NULL;
    }
    free(copy);
    return HPE_OK;
}

/** @brief Implements the hpe_scenario_run_file operation. */
hpe_status_t hpe_scenario_run_file(hpe_sim_t *sim, const char *path, char *error,
                                   size_t error_capacity)
{
    FILE *file;
    long size;
    char *buffer;
    size_t n;
    hpe_status_t status;
    if (!sim || !path) {
        return HPE_ERR_ARGUMENT;
    }
    file = fopen(path, "rb");
    if (!file) {
        if (error && error_capacity) {
            snprintf(error, error_capacity, "cannot open scenario: %s", path);
        }
        return HPE_ERR_IO;
    }
    if (fseek(file, 0, SEEK_END) != 0 || (size = ftell(file)) < 0 ||
        fseek(file, 0, SEEK_SET) != 0) {
        fclose(file);
        return HPE_ERR_IO;
    }
    buffer = (char *)malloc((size_t)size + 1);
    if (!buffer) {
        fclose(file);
        return HPE_ERR_NOMEM;
    }
    n = fread(buffer, 1, (size_t)size, file);
    {
        int close_error = fclose(file);
        if (n != (size_t)size || close_error != 0) {
            free(buffer);
            return HPE_ERR_IO;
        }
    }
    buffer[n] = '\0';
    status = hpe_scenario_run_text(sim, buffer, error, error_capacity);
    free(buffer);
    return status;
}
