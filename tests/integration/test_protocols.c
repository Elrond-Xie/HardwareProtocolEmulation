/*
 * File: test_protocols.c
 * Purpose: Project-owned implementation and interface for tests / integration / test protocols.c.
 * Author: Elrond-Xie
 * Copyright (c) 2026 Elrond-Xie. SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0.
 */

#include "hpe/hpe.h"

#include <stdio.h>
#include <string.h>

#define CHECK(condition)                                                                           \
    do {                                                                                           \
        if (!(condition)) {                                                                        \
            fprintf(stderr, "%s:%d: check failed: %s\n", __func__, __LINE__, #condition);          \
            return 1;                                                                              \
        }                                                                                          \
    }                                                                                              \
    while (0)

#define CHECK_STATUS(expression, expected) CHECK((expression) == (expected))

/** @brief Implements the test_i2c operation. */
static int test_i2c(void)
{
    hpe_sim_t *sim = hpe_sim_create();
    const uint8_t write_data[] = {0x10, 0xa5};
    uint8_t value = 0;
    CHECK(sim != NULL);
    CHECK_STATUS(hpe_i2c_add_register_device(sim, "sensor", 0x48, 32, 0), HPE_OK);
    CHECK_STATUS(hpe_i2c_probe(sim, 0x48), HPE_OK);
    CHECK_STATUS(hpe_i2c_write(sim, 0x48, write_data, sizeof(write_data)), HPE_OK);
    CHECK_STATUS(hpe_i2c_read_register(sim, 0x48, 0x10, &value, 1), HPE_OK);
    CHECK(value == 0xa5);
    hpe_sim_destroy(sim);
    return 0;
}

/** @brief Implements the spi_transfer operation. */
static int spi_transfer(hpe_sim_t *sim, const uint8_t *tx, size_t length)
{
    uint8_t rx[16];
    CHECK(length <= sizeof(rx));
    return hpe_spi_transfer(sim, 0, 0, tx, rx, length) == HPE_OK ? 0 : 1;
}

/** @brief Implements the test_spi operation. */
static int test_spi(void)
{
    hpe_sim_t *sim = hpe_sim_create();
    const uint8_t enable[] = {0x06};
    const uint8_t program[] = {0x02, 0, 0, 0x10, 0xde, 0xad, 0xbe, 0xef};
    const uint8_t write_status[] = {0x01, 0x1c};
    const uint8_t blocked_program[] = {0x02, 0, 0, 0x10, 0};
    uint8_t contents[4];
    CHECK(sim != NULL);
    CHECK_STATUS(hpe_spi_add_flash_device(sim, "flash", 0, 65536, 0xff), HPE_OK);
    CHECK(spi_transfer(sim, enable, sizeof(enable)) == 0);
    CHECK(spi_transfer(sim, program, sizeof(program)) == 0);
    CHECK_STATUS(hpe_spi_read_memory(sim, 0, 0x10, contents, sizeof(contents)), HPE_OK);
    CHECK(memcmp(contents, "\xde\xad\xbe\xef", sizeof(contents)) == 0);
    CHECK(spi_transfer(sim, enable, sizeof(enable)) == 0);
    CHECK(spi_transfer(sim, write_status, sizeof(write_status)) == 0);
    CHECK(spi_transfer(sim, enable, sizeof(enable)) == 0);
    CHECK(spi_transfer(sim, blocked_program, sizeof(blocked_program)) == 0);
    CHECK_STATUS(hpe_spi_read_memory(sim, 0, 0x10, contents, sizeof(contents)), HPE_OK);
    CHECK(memcmp(contents, "\xde\xad\xbe\xef", sizeof(contents)) == 0);
    hpe_sim_destroy(sim);
    return 0;
}

/** @brief Implements the test_sd_sdhc operation. */
static int test_sd_sdhc(void)
{
    hpe_sim_t *sim = hpe_sim_create();
    uint32_t response = 0;
    uint8_t source[512], result[512], csd[16], memory[4];
    size_t register_length = 0;
    size_t i;
    size_t trace_start;
    CHECK(sim != NULL);
    CHECK_STATUS(hpe_block_card_add(sim, HPE_PROTOCOL_SD, "sdhc", 4194304, 0xff), HPE_OK);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 0, 0, &response), HPE_OK);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 8, 0x1aa, &response), HPE_OK);
    CHECK(response == 0x1aa);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 55, 0, &response), HPE_OK);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 41, 0x40ff8000, &response), HPE_OK);
    CHECK((response & 0xc0000000u) == 0);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 55, 0, &response), HPE_OK);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 41, 0x40ff8000, &response), HPE_OK);
    CHECK((response & 0xc0000000u) == 0xc0000000u);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 2, 0, &response), HPE_OK);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 3, 0, &response), HPE_OK);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 7, 0x12340000, &response), HPE_OK);
    trace_start = hpe_sim_event_count(sim);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 17, 0, &response), HPE_OK);
    CHECK_STATUS(hpe_block_card_read(sim, 0, 0, result), HPE_OK);
    for (i = 0; i < sizeof(result); ++i) {
        CHECK(result[i] == 0xff);
    }
    {
        unsigned data_clocks = 0;
        unsigned saw_start = 0;
        uint16_t block_crc = 0;
        for (i = trace_start; i < hpe_sim_event_count(sim); ++i) {
            const hpe_event_t *event = hpe_sim_event_at(sim, i);
            if (event->kind == HPE_EVENT_LINE && event->protocol == HPE_PROTOCOL_SD &&
                event->data_line_count == 1) {
                ++data_clocks;
            }
            if (event->kind == HPE_EVENT_LINE &&
                strcmp(event->message, "SD/eMMC data start bit") == 0 && event->clock == 0) {
                saw_start = event->data_lines[0] == 0;
            }
            if (event->kind == HPE_EVENT_BLOCK) {
                block_crc = event->data_crc16;
            }
        }
        CHECK(data_clocks == (1u + 512u * 8u + 16u + 1u) * 2u);
        CHECK(saw_start);
        CHECK(block_crc == 0x7fa1u); /* CRC16 of a 512-byte all-ones data block. */
    }
    CHECK_STATUS(hpe_block_card_command(sim, 0, 24, 2, &response), HPE_OK);
    for (i = 0; i < sizeof(source); ++i) {
        source[i] = (uint8_t)(i ^ 0x6d);
    }
    trace_start = hpe_sim_event_count(sim);
    CHECK_STATUS(hpe_block_card_write(sim, 0, 2, source), HPE_OK);
    {
        static const uint8_t expected_response[] = {0, 0, 1, 0, 1};
        size_t token_bit = 0;
        unsigned busy_clocks = 0;
        int saw_ready = 0, block_after_ready = 0;
        uint64_t ready_time = 0, block_time = 0;
        for (i = trace_start; i < hpe_sim_event_count(sim); ++i) {
            const hpe_event_t *event = hpe_sim_event_at(sim, i);
            if (event->kind != HPE_EVENT_LINE || event->clock != 0) {
                continue;
            }
            if (strcmp(event->message, "SD/eMMC data response token") == 0) {
                CHECK(token_bit < sizeof(expected_response));
                CHECK(event->data_lines[0] == expected_response[token_bit++]);
            }
            else if (strcmp(event->message, "SD/eMMC write busy") == 0) {
                ++busy_clocks;
                CHECK(event->data_lines[0] == 0);
            }
            else if (strcmp(event->message, "SD/eMMC write ready") == 0) {
                saw_ready = event->data_lines[0] == 1;
                ready_time = event->time_ns;
            }
        }
        for (i = trace_start; i < hpe_sim_event_count(sim); ++i) {
            const hpe_event_t *event = hpe_sim_event_at(sim, i);
            if (event->kind == HPE_EVENT_BLOCK) {
                block_time = event->time_ns;
                block_after_ready = saw_ready && block_time > ready_time;
            }
        }
        CHECK(token_bit == sizeof(expected_response));
        CHECK(busy_clocks == 1000 && saw_ready && block_after_ready);
    }
    CHECK_STATUS(hpe_block_card_command(sim, 0, 17, 2, &response), HPE_OK);
    CHECK_STATUS(hpe_block_card_read(sim, 0, 2, result), HPE_OK);
    CHECK(memcmp(source, result, sizeof(source)) == 0);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 55, 0x12340000, &response), HPE_OK);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 6, 2, &response), HPE_OK);
    trace_start = hpe_sim_event_count(sim);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 17, 2, &response), HPE_OK);
    CHECK_STATUS(hpe_block_card_read(sim, 0, 2, result), HPE_OK);
    CHECK(memcmp(source, result, sizeof(source)) == 0);
    {
        unsigned saw_four_bit_bus = 0;
        unsigned saw_four_bit_start = 0;
        unsigned saw_first_nibble = 0;
        for (i = trace_start; i < hpe_sim_event_count(sim); ++i) {
            const hpe_event_t *event = hpe_sim_event_at(sim, i);
            if (event->kind == HPE_EVENT_LINE && event->data_line_count == 4) {
                saw_four_bit_bus = 1;
            }
            if (event->kind == HPE_EVENT_LINE && event->data_line_count == 4 && event->clock == 0 &&
                strcmp(event->message, "SD/eMMC data start bit") == 0) {
                saw_four_bit_start = event->data_lines[0] == 0 && event->data_lines[1] == 0 &&
                                     event->data_lines[2] == 0 && event->data_lines[3] == 0;
            }
            if (!saw_first_nibble && event->kind == HPE_EVENT_LINE && event->data_line_count == 4 &&
                event->clock && strcmp(event->message, "SD/eMMC data payload") == 0) {
                saw_first_nibble = event->value == (source[0] >> 4);
            }
        }
        CHECK(saw_four_bit_bus);
        CHECK(saw_four_bit_start);
        CHECK(saw_first_nibble);
    }
    CHECK_STATUS(hpe_block_card_get_register(sim, 0, HPE_CARD_REGISTER_CSD, csd, sizeof(csd),
                                             &register_length),
                 HPE_OK);
    CHECK(register_length == sizeof(csd) && (csd[0] >> 6) == 1);
    CHECK_STATUS(hpe_sim_virtual_device_read_memory(sim, 0, 2u * 512u, memory, sizeof(memory)),
                 HPE_OK);
    CHECK(memcmp(memory, source, sizeof(memory)) == 0);
    CHECK_STATUS(hpe_sim_virtual_device_read_memory(sim, 0, 0x80000000u, memory, 1), HPE_ERR_RANGE);
    hpe_sim_destroy(sim);
    return 0;
}

/** @brief Implements the test_emmc_switch operation. */
static int test_emmc_switch(void)
{
    hpe_sim_t *sim = hpe_sim_create();
    uint32_t response = 0;
    uint8_t ext_csd[512];
    uint8_t data[512];
    size_t length = 0;
    size_t i;
    CHECK(sim != NULL);
    CHECK_STATUS(hpe_block_card_add(sim, HPE_PROTOCOL_EMMC, "emmc", 8, 0), HPE_OK);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 0, 0, &response), HPE_OK);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 1, 0x40ff8000, &response), HPE_OK);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 1, 0x40ff8000, &response), HPE_OK);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 2, 0, &response), HPE_OK);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 3, 0x00010000, &response), HPE_OK);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 7, 0x00010000, &response), HPE_OK);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 6, 0x03b70200, &response), HPE_OK);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 6, 0x03b90100, &response), HPE_OK);
    CHECK_STATUS(hpe_block_card_get_register(sim, 0, HPE_CARD_REGISTER_EXT_CSD, ext_csd,
                                             sizeof(ext_csd), &length),
                 HPE_OK);
    CHECK(length == sizeof(ext_csd) && ext_csd[183] == 2 && ext_csd[185] == 1);
    CHECK_STATUS(hpe_block_card_command(sim, 0, 8, 0, &response), HPE_OK);
    CHECK_STATUS(hpe_block_card_read(sim, 0, 0, data), HPE_OK);
    CHECK(memcmp(data, ext_csd, sizeof(data)) == 0);
    {
        unsigned saw_eight_bit_bus = 0;
        unsigned saw_eight_bit_start = 0;
        for (i = 0; i < hpe_sim_event_count(sim); ++i) {
            const hpe_event_t *event = hpe_sim_event_at(sim, i);
            if (event->protocol == HPE_PROTOCOL_EMMC && event->kind == HPE_EVENT_LINE &&
                event->data_line_count == 8) {
                saw_eight_bit_bus = 1;
                if (event->clock == 0 && strcmp(event->message, "SD/eMMC data start bit") == 0) {
                    unsigned lane;
                    saw_eight_bit_start = 1;
                    for (lane = 0; lane < 8; ++lane) {
                        if (event->data_lines[lane]) {
                            saw_eight_bit_start = 0;
                        }
                    }
                }
            }
        }
        CHECK(saw_eight_bit_bus);
        CHECK(saw_eight_bit_start);
    }
    CHECK_STATUS(hpe_block_card_command(sim, 0, 6, 0x03b70600, &response), HPE_ERR_RANGE);
    hpe_sim_destroy(sim);
    return 0;
}

/** @brief Implements the test_usb operation. */
static int test_usb(void)
{
    hpe_sim_t *sim = hpe_sim_create();
    uint8_t payload[] = {0x48, 0x50, 0x45};
    uint8_t result[3] = {0};
    uint8_t irq_payload[] = {0x12, 0x34, 0x56};
    uint8_t irq_result[8] = {0};
    uint8_t configuration[64] = {0};
    uint8_t status[2] = {0};
    uint8_t one_byte = 0xff;
    uint16_t actual = 0;
    uint8_t irq_actual = 0;
    size_t i, first_line = (size_t)-1;
    size_t sof_count = 0;
    uint64_t last_sof_time = 0;
    uint16_t last_sof_frame = 0;
    int found_stuffed_zero = 0, found_eop = 0, found_sof = 0;
    CHECK(sim != NULL);
    CHECK_STATUS(hpe_usb_device_add(sim, "echo", 0x1209, 1, "HPE", "Echo"), HPE_OK);
    CHECK_STATUS(hpe_usb_enumerate(sim, 0), HPE_OK);
    CHECK_STATUS(hpe_usb_control_transfer(sim, 0, 1, 0x80, 6, 0x0200, 0, configuration,
                                          sizeof(configuration), &actual),
                 HPE_OK);
    CHECK(actual == 39 && configuration[13] == 3 && configuration[34] == 0x82 &&
          configuration[35] == 3 && configuration[36] == 8 && configuration[38] == 10);
    for (i = 0; i < hpe_sim_event_count(sim); ++i) {
        const hpe_event_t *event = hpe_sim_event_at(sim, i);
        if (event->protocol != HPE_PROTOCOL_USB) {
            continue;
        }
        if (event->kind == HPE_EVENT_LINE && event->usb_pid == 0x2du && first_line == (size_t)-1) {
            first_line = i;
        }
        if (strcmp(event->message, "USB stuffed zero bit") == 0) {
            found_stuffed_zero = 1;
        }
        if (strcmp(event->message, "USB EOP SE0 1/2") == 0) {
            found_eop = 1;
        }
        if (event->kind == HPE_EVENT_INFO && event->usb_pid == 0xa5u &&
            event->protocol_argument <= 0x07ffu && event->time_ns >= 1000000u) {
            found_sof = 1;
            if (sof_count) {
                CHECK(event->protocol_argument == ((last_sof_frame + 1u) & 0x07ffu));
                CHECK(event->time_ns - last_sof_time >= 990000u &&
                      event->time_ns - last_sof_time <= 1010000u);
            }
            ++sof_count;
            last_sof_time = event->time_ns;
            last_sof_frame = (uint16_t)event->protocol_argument;
        }
    }
    CHECK(first_line != (size_t)-1 && found_stuffed_zero && found_eop && found_sof &&
          sof_count >= 2);
    {
        const uint8_t expected_dplus[] = {0, 1, 0, 1, 0, 1, 0, 0};
        const uint8_t expected_dminus[] = {1, 0, 1, 0, 1, 0, 1, 1};
        for (i = 0; i < sizeof(expected_dplus); ++i) {
            const hpe_event_t *event = hpe_sim_event_at(sim, first_line + i);
            CHECK(event->kind == HPE_EVENT_LINE && event->clock == expected_dplus[i] &&
                  event->mosi == expected_dminus[i]);
            if (i) {
                const hpe_event_t *previous = hpe_sim_event_at(sim, first_line + i - 1);
                CHECK(event->time_ns - previous->time_ns >= 83 &&
                      event->time_ns - previous->time_ns <= 84);
            }
        }
    }
    {
        uint8_t decoded[64];
        size_t decoded_count = 0;
        uint8_t previous_dplus = 1, previous_dminus = 0;
        for (i = first_line; i < hpe_sim_event_count(sim); ++i) {
            const hpe_event_t *event = hpe_sim_event_at(sim, i);
            if (strstr(event->message, "USB EOP SE0") != NULL) {
                break;
            }
            if (strcmp(event->message, "USB stuffed zero bit") == 0) {
                continue;
            }
            CHECK(event->kind == HPE_EVENT_LINE && decoded_count < sizeof(decoded));
            decoded[decoded_count++] =
                event->clock == previous_dplus && event->mosi == previous_dminus;
            previous_dplus = event->clock;
            previous_dminus = event->mosi;
        }
        CHECK(decoded_count >= 32);
        /* SETUP token to address zero, endpoint zero has CRC5 value 0x02. */
        CHECK(decoded[0] == 0 && decoded[1] == 0 && decoded[2] == 0 && decoded[3] == 0 &&
              decoded[4] == 0 && decoded[5] == 0 && decoded[6] == 0 && decoded[7] == 1);
        CHECK(decoded[8] == 1 && decoded[9] == 0 && decoded[10] == 1 && decoded[11] == 1 &&
              decoded[12] == 0 && decoded[13] == 1 && decoded[14] == 0 && decoded[15] == 0);
        for (i = 16; i < 27; ++i) {
            CHECK(decoded[i] == 0);
        }
        CHECK(decoded[27] == 0 && decoded[28] == 1 && decoded[29] == 0 && decoded[30] == 0 &&
              decoded[31] == 0);
    }
    CHECK_STATUS(hpe_usb_control_transfer(sim, 0, 1, 0x80, 8, 0, 0, &one_byte, 1, &actual), HPE_OK);
    CHECK(actual == 1 && one_byte == 1);
    CHECK_STATUS(hpe_usb_control_transfer(sim, 0, 1, 0x81, 10, 0, 0, &one_byte, 1, &actual),
                 HPE_OK);
    CHECK(actual == 1 && one_byte == 0);
    CHECK_STATUS(hpe_usb_control_transfer(sim, 0, 1, 0x01, 11, 1, 0, NULL, 0, &actual),
                 HPE_ERR_ARGUMENT);
    CHECK_STATUS(hpe_usb_control_transfer(sim, 0, 1, 0x01, 11, 0, 0, NULL, 0, &actual), HPE_OK);
    CHECK_STATUS(
        hpe_usb_control_transfer(sim, 0, 1, 0x82, 0, 0, 0, status, sizeof(status), &actual),
        HPE_OK);
    CHECK(actual == 2 && status[0] == 0 && status[1] == 0);
    CHECK_STATUS(
        hpe_usb_control_transfer(sim, 0, 1, 0x82, 0, 0, 0x80, status, sizeof(status), &actual),
        HPE_OK);
    CHECK(actual == 2 && status[0] == 0 && status[1] == 0);
    CHECK_STATUS(hpe_usb_control_transfer(sim, 0, 1, 0x00, 3, 1, 0, NULL, 0, &actual), HPE_OK);
    CHECK_STATUS(
        hpe_usb_control_transfer(sim, 0, 1, 0x80, 0, 0, 0, status, sizeof(status), &actual),
        HPE_OK);
    CHECK(actual == 2 && (status[0] & 0x02) != 0);
    CHECK_STATUS(hpe_usb_control_transfer(sim, 0, 1, 0x00, 1, 1, 0, NULL, 0, &actual), HPE_OK);
    CHECK_STATUS(
        hpe_usb_control_transfer(sim, 0, 1, 0x80, 0, 0, 0, status, sizeof(status), &actual),
        HPE_OK);
    CHECK(status[0] == 0 && status[1] == 0);
    CHECK_STATUS(hpe_usb_bulk_transfer(sim, 0, 1, 0x01, payload, sizeof(payload), &actual), HPE_OK);
    CHECK(actual == sizeof(payload));
    CHECK_STATUS(hpe_usb_bulk_transfer(sim, 0, 1, 0x81, result, sizeof(result), &actual), HPE_OK);
    CHECK(actual == sizeof(result) && memcmp(payload, result, sizeof(payload)) == 0);
    CHECK_STATUS(hpe_usb_interrupt_poll(sim, 0, 1, irq_result, sizeof(irq_result), &irq_actual),
                 HPE_ERR_NACK);
    CHECK(irq_actual == 0);
    CHECK_STATUS(hpe_usb_interrupt_queue(sim, 0, irq_payload, sizeof(irq_payload)), HPE_OK);
    CHECK_STATUS(hpe_usb_interrupt_poll(sim, 0, 1, irq_result, sizeof(irq_result), &irq_actual),
                 HPE_OK);
    CHECK(irq_actual == sizeof(irq_payload) &&
          memcmp(irq_payload, irq_result, sizeof(irq_payload)) == 0);
    CHECK_STATUS(hpe_usb_interrupt_queue(sim, 0, irq_payload, sizeof(irq_payload)), HPE_OK);
    CHECK_STATUS(hpe_usb_interrupt_poll(sim, 0, 1, irq_result, sizeof(irq_result), &irq_actual),
                 HPE_OK);
    CHECK_STATUS(hpe_usb_interrupt_queue(sim, 0, irq_payload, sizeof(irq_payload)), HPE_OK);
    CHECK_STATUS(hpe_usb_control_transfer(sim, 0, 1, 0x02, 3, 0, 0x82, NULL, 0, &actual), HPE_OK);
    CHECK_STATUS(
        hpe_usb_control_transfer(sim, 0, 1, 0x82, 0, 0, 0x82, status, sizeof(status), &actual),
        HPE_OK);
    CHECK(status[0] == 1);
    CHECK_STATUS(hpe_usb_interrupt_poll(sim, 0, 1, irq_result, sizeof(irq_result), &irq_actual),
                 HPE_ERR_IO);
    CHECK_STATUS(hpe_usb_control_transfer(sim, 0, 1, 0x02, 1, 0, 0x82, NULL, 0, &actual), HPE_OK);
    CHECK_STATUS(hpe_usb_interrupt_poll(sim, 0, 1, irq_result, sizeof(irq_result), &irq_actual),
                 HPE_OK);
    {
        unsigned data0 = 0, data1 = 0;
        for (i = 0; i < hpe_sim_event_count(sim); ++i) {
            const hpe_event_t *event = hpe_sim_event_at(sim, i);
            if (event->kind == HPE_EVENT_DATA && event->usb_endpoint == 2 &&
                event->usb_pid == 0xc3u) {
                ++data0;
            }
            if (event->kind == HPE_EVENT_DATA && event->usb_endpoint == 2 &&
                event->usb_pid == 0x4bu) {
                ++data1;
            }
        }
        CHECK(data0 == 2 && data1 == 1);
    }
    CHECK_STATUS(hpe_usb_control_transfer(sim, 0, 1, 0x02, 3, 0, 0x81, NULL, 0, &actual), HPE_OK);
    CHECK_STATUS(
        hpe_usb_control_transfer(sim, 0, 1, 0x82, 0, 0, 0x81, status, sizeof(status), &actual),
        HPE_OK);
    CHECK(actual == 2 && status[0] == 1);
    CHECK_STATUS(hpe_usb_control_transfer(sim, 0, 1, 0x02, 1, 0, 0x81, NULL, 0, &actual), HPE_OK);
    CHECK_STATUS(
        hpe_usb_control_transfer(sim, 0, 1, 0x82, 0, 0, 0x81, status, sizeof(status), &actual),
        HPE_OK);
    CHECK(status[0] == 0);
    CHECK_STATUS(hpe_usb_control_transfer(sim, 0, 1, 0x00, 9, 0, 0, NULL, 0, &actual), HPE_OK);
    CHECK_STATUS(hpe_usb_control_transfer(sim, 0, 1, 0x80, 8, 0, 0, &one_byte, 1, &actual), HPE_OK);
    CHECK(actual == 1 && one_byte == 0);
    hpe_sim_destroy(sim);
    return 0;
}

/** @brief Implements the test_pcie operation. */
static int test_pcie(void)
{
    hpe_sim_t *sim = hpe_sim_create();
    hpe_pcie_bdf_t bdf;
    size_t found = 0;
    uint32_t value = 0;
    CHECK(sim != NULL);
    CHECK_STATUS(
        hpe_pcie_add_endpoint(sim, "endpoint", 0, 1, 0, 0x1234, 0x5678, 2, 0, 0, 4096, 4, 4),
        HPE_OK);
    CHECK_STATUS(hpe_pcie_enumerate(sim, &bdf, 1, &found), HPE_OK);
    CHECK(found == 1 && bdf.vendor_id == 0x1234 && bdf.device_id == 0x5678);
    CHECK_STATUS(hpe_pcie_config_write(sim, 0, 0x10, 4, 0xffffffffu), HPE_OK);
    CHECK_STATUS(hpe_pcie_config_read(sim, 0, 0x10, 4, &value), HPE_OK);
    CHECK(value == 0xfffff000u);
    CHECK_STATUS(hpe_pcie_config_write(sim, 0, 0x04, 2, 0x0002), HPE_OK);
    CHECK_STATUS(hpe_pcie_mmio_write(sim, 0, 4, 4, 0xdeadbeef), HPE_OK);
    CHECK_STATUS(hpe_pcie_mmio_read(sim, 0, 4, 4, &value), HPE_OK);
    CHECK(value == 0xdeadbeef);
    for (size_t i = 0; i < 12; ++i) {
        CHECK_STATUS(hpe_pcie_config_read(sim, 0, 0, 4, &value), HPE_OK);
    }
    {
        size_t i;
        int found_write = 0, found_read_completion = 0, found_dllp_vector = 0;
        for (i = 0; i < hpe_sim_event_count(sim); ++i) {
            const hpe_event_t *event = hpe_sim_event_at(sim, i);
            if (event->protocol != HPE_PROTOCOL_PCIE) {
                continue;
            }
            if (event->kind == HPE_EVENT_ACK && event->pcie_dllp_length &&
                event->pcie_dllp_ack_sequence == 17) {
                found_dllp_vector = 1;
                CHECK(event->pcie_dllp[0] == 0 && event->pcie_dllp[1] == 0);
                CHECK(event->pcie_dllp[2] == 0 && event->pcie_dllp[3] == 17);
                CHECK(event->pcie_dllp_crc16 == 0x1363);
                CHECK(event->pcie_dllp[4] == 0x13 && event->pcie_dllp[5] == 0x63);
            }
            if (event->kind == HPE_EVENT_COMMAND && event->pcie_tlp_fmt_type == 0x40) {
                found_write = 1;
                CHECK(event->pcie_tlp_header_length == 12);
                CHECK(event->pcie_tlp_header[0] == 0x01 && event->pcie_tlp_header[3] == 0x40);
                CHECK(event->pcie_tlp_payload_length == 4);
                CHECK(event->pcie_tlp_payload[0] == 0xef && event->pcie_tlp_payload[3] == 0xde);
                CHECK(event->pcie_tlp_sequence == 4);
                CHECK(event->pcie_lcrc == 0x7aefb5b2u);
            }
            if (event->kind == HPE_EVENT_RESPONSE && event->pcie_tlp_fmt_type == 0x4a &&
                event->protocol_argument == 4 && event->protocol_response == 0xdeadbeefu) {
                found_read_completion = 1;
                CHECK(event->pcie_tlp_header_length == 12);
                CHECK(event->pcie_tlp_payload_length == 4);
                CHECK(event->pcie_tlp_sequence != 0);
                CHECK(event->pcie_lcrc != 0);
            }
        }
        CHECK(found_write && found_read_completion && found_dllp_vector);
    }
    CHECK_STATUS(hpe_pcie_corrupt_next_tlp(sim), HPE_OK);
    CHECK_STATUS(hpe_pcie_mmio_write(sim, 0, 8, 4, 0x12345678), HPE_OK);
    {
        size_t i, bad_index = (size_t)-1, nak_index = (size_t)-1;
        size_t replay_index = (size_t)-1, ack_index = (size_t)-1;
        for (i = 0; i < hpe_sim_event_count(sim); ++i) {
            const hpe_event_t *event = hpe_sim_event_at(sim, i);
            if (event->protocol != HPE_PROTOCOL_PCIE) {
                continue;
            }
            if (event->kind == HPE_EVENT_COMMAND && event->pcie_tlp_corrupted) {
                bad_index = i;
            }
            if (event->kind == HPE_EVENT_ACK && event->pcie_dllp_type == 0x10 &&
                event->pcie_dllp_ack_sequence == 17) {
                nak_index = i;
            }
            if (event->kind == HPE_EVENT_COMMAND && event->pcie_tlp_replay) {
                replay_index = i;
            }
            if (event->kind == HPE_EVENT_ACK && event->pcie_dllp_type == 0x00 &&
                event->pcie_dllp_ack_sequence == 18) {
                ack_index = i;
            }
        }
        CHECK(bad_index < nak_index && nak_index < replay_index && replay_index < ack_index);
        CHECK(hpe_sim_event_at(sim, bad_index)->pcie_tlp_header[0] == 0x00);
        CHECK(hpe_sim_event_at(sim, replay_index)->pcie_tlp_header[0] == 0x01);
        CHECK(hpe_sim_event_at(sim, bad_index)->pcie_lcrc ==
              hpe_sim_event_at(sim, replay_index)->pcie_lcrc);
    }
    hpe_sim_destroy(sim);
    return 0;
}

/** @brief Implements the main operation. */
int main(void)
{
    CHECK(test_i2c() == 0);
    CHECK(test_spi() == 0);
    CHECK(test_sd_sdhc() == 0);
    CHECK(test_emmc_switch() == 0);
    CHECK(test_usb() == 0);
    CHECK(test_pcie() == 0);
    puts("HPE protocol model tests passed");
    return 0;
}
