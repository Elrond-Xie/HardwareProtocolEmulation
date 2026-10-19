#include "internal.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define CARD_BLOCK_SIZE 512u
#define CARD_MAX_BLOCKS 4194304u
#define SD_OCR_VOLTAGE_WINDOW 0x00ff8000u
#define SD_OCR_HIGH_CAPACITY 0x40000000u
#define SD_OCR_POWER_UP_STATUS 0x80000000u

static hpe_block_card_t *card_at(const hpe_sim_t *sim, size_t index) {
    if (!sim || index >= sim->block_card_count) return NULL;
    return sim->block_cards[index];
}

static void set_register_bits(uint8_t *register_bytes, unsigned msb, unsigned lsb,
        uint64_t value) {
    unsigned bit;
    for (bit = lsb; bit <= msb; ++bit) {
        unsigned byte_index = (127u - bit) / 8u;
        unsigned bit_index = 7u - ((127u - bit) % 8u);
        uint8_t mask = (uint8_t)(1u << bit_index);
        if ((value >> (bit - lsb)) & 1u) register_bytes[byte_index] |= mask;
        else register_bytes[byte_index] &= (uint8_t)~mask;
    }
}

static uint8_t register_crc7(const uint8_t *data, size_t length) {
    uint8_t crc = 0;
    size_t i;
    unsigned bit;
    for (i = 0; i < length; ++i) {
        for (bit = 0; bit < 8; ++bit) {
            uint8_t input = (uint8_t)((data[i] >> (7u - bit)) & 1u);
            uint8_t feedback = (uint8_t)(((crc >> 6) & 1u) ^ input);
            crc = (uint8_t)((crc << 1) & 0x7fu);
            if (feedback) crc ^= 0x09u;
        }
    }
    return crc;
}

static void make_card_registers(hpe_block_card_t *card) {
    uint32_t multiplier = 0, c_size, divisor = 4;
    memset(card->cid, 0, sizeof(card->cid));
    set_register_bits(card->cid, 127, 120, 0x03); /* Fixed virtual manufacturer ID. */
    set_register_bits(card->cid, 119, 104, 0x4850); /* OID "HP". */
    set_register_bits(card->cid, 103, 64, 0x4850455344ull); /* Product name "HPESD". */
    set_register_bits(card->cid, 63, 56, 0x10); /* Product revision 1.0. */
    set_register_bits(card->cid, 55, 24, card->block_count ^ 0x48504531u);
    set_register_bits(card->cid, 19, 8, (26u << 4) | 1u); /* January 2026. */
    set_register_bits(card->cid, 7, 1, register_crc7(card->cid, 15));
    set_register_bits(card->cid, 0, 0, 1);

    memset(card->csd, 0, sizeof(card->csd));
    set_register_bits(card->csd, 119, 112, 0x0e); /* TAAC */
    set_register_bits(card->csd, 111, 104, 0x00); /* NSAC */
    set_register_bits(card->csd, 103, 96, 0x32); /* 25 MHz transfer speed. */
    set_register_bits(card->csd, 95, 84, 0x5b5); /* Common read/write command classes. */
    set_register_bits(card->csd, 83, 80, 9); /* 512-byte read blocks. */
    if (card->high_capacity) {
        set_register_bits(card->csd, 127, 126, 1); /* CSD structure 1.0 (SDHC). */
        c_size = (card->block_count + 1023u) / 1024u - 1u;
        set_register_bits(card->csd, 69, 48, c_size);
    } else {
        c_size = (card->block_count + divisor - 1u) / divisor - 1u;
        while (c_size > 4095u && multiplier < 7u) {
            ++multiplier;
            divisor = 1u << (multiplier + 2u);
            c_size = (card->block_count + divisor - 1u) / divisor - 1u;
        }
        set_register_bits(card->csd, 73, 62, c_size);
        set_register_bits(card->csd, 49, 47, multiplier);
    }
    set_register_bits(card->csd, 46, 46, 1); /* Single-block erase enabled. */
    set_register_bits(card->csd, 45, 39, 0x7f);
    set_register_bits(card->csd, 28, 26, 2); /* R2W_FACTOR */
    set_register_bits(card->csd, 25, 22, 9); /* 512-byte write blocks. */
    set_register_bits(card->csd, 7, 1, register_crc7(card->csd, 15));
    set_register_bits(card->csd, 0, 0, 1);

    memset(card->ext_csd, 0, sizeof(card->ext_csd));
    card->ext_csd[192] = 8; /* EXT_CSD_REV 1.8. */
    card->ext_csd[196] = 0x03; /* 26/52 MHz SDR device type. */
    card->ext_csd[212] = (uint8_t)card->block_count;
    card->ext_csd[213] = (uint8_t)(card->block_count >> 8);
    card->ext_csd[214] = (uint8_t)(card->block_count >> 16);
    card->ext_csd[215] = (uint8_t)(card->block_count >> 24);
}

hpe_status_t hpe_block_card_add(hpe_sim_t *sim, hpe_protocol_t protocol,
        const char *name, uint32_t block_count, uint8_t fill) {
    hpe_block_card_t *card;
    size_t i;
    if (!sim || !name || !name[0] || !block_count || block_count > CARD_MAX_BLOCKS ||
            (protocol != HPE_PROTOCOL_SD && protocol != HPE_PROTOCOL_EMMC))
        return HPE_ERR_ARGUMENT;
    if (sim->block_card_count == sim->block_card_capacity) {
        size_t capacity = sim->block_card_capacity ? sim->block_card_capacity * 2 : 4;
        hpe_block_card_t **cards = (hpe_block_card_t **)realloc(
                sim->block_cards, capacity * sizeof(*cards));
        if (!cards) return HPE_ERR_NOMEM;
        sim->block_cards = cards;
        sim->block_card_capacity = capacity;
    }
    card = (hpe_block_card_t *)calloc(1, sizeof(*card));
    if (!card) return HPE_ERR_NOMEM;
    for (i = 0; i < sizeof(card->name) - 1 && name[i]; ++i) card->name[i] = name[i];
    card->protocol = protocol;
    card->state = HPE_CARD_IDLE;
    card->block_count = block_count;
    card->fill = fill;
    card->high_capacity = protocol == HPE_PROTOCOL_SD && block_count >= 4194304u;
    card->bus_width = 1;
    card->rca = protocol == HPE_PROTOCOL_EMMC ? 1u : 0x1234u;
    make_card_registers(card);
    sim->block_cards[sim->block_card_count++] = card;
    return HPE_OK;
}

size_t hpe_block_card_count(const hpe_sim_t *sim) {
    return sim ? sim->block_card_count : 0;
}

hpe_status_t hpe_block_card_get_register(const hpe_sim_t *sim, size_t card_index,
        hpe_card_register_t reg, uint8_t *data, size_t capacity, size_t *length) {
    hpe_block_card_t *card = card_at(sim, card_index);
    const uint8_t *source;
    size_t required;
    if (!card || !data || !length) return HPE_ERR_ARGUMENT;
    if (reg == HPE_CARD_REGISTER_CID) { source = card->cid; required = sizeof(card->cid); }
    else if (reg == HPE_CARD_REGISTER_CSD) { source = card->csd; required = sizeof(card->csd); }
    else if (reg == HPE_CARD_REGISTER_EXT_CSD && card->protocol == HPE_PROTOCOL_EMMC) {
        source = card->ext_csd; required = sizeof(card->ext_csd);
    } else return HPE_ERR_ARGUMENT;
    *length = required;
    if (capacity < required) return HPE_ERR_RANGE;
    memcpy(data, source, required);
    return HPE_OK;
}

static uint8_t crc7_first_40(uint64_t frame) {
    uint8_t crc = 0;
    int bit;
    for (bit = 39; bit >= 0; --bit) {
        uint8_t input = (uint8_t)((frame >> bit) & 1u);
        uint8_t feedback = (uint8_t)(((crc >> 6) & 1u) ^ input);
        crc = (uint8_t)((crc << 1) & 0x7fu);
        if (feedback) crc ^= 0x09u;
    }
    return (uint8_t)((crc << 1) | 1u);
}

static uint8_t command_crc7(uint8_t command, uint32_t argument) {
    return crc7_first_40(((uint64_t)(0x40u | command) << 32) | argument);
}

static uint16_t data_line_crc16(const uint8_t data[CARD_BLOCK_SIZE],
        unsigned width, unsigned lane) {
    uint16_t crc = 0;
    size_t byte;
    unsigned bit;
    for (byte = 0; byte < CARD_BLOCK_SIZE; ++byte) {
        for (bit = 0; bit < 8; bit += width) {
            unsigned lane_bit = width - lane - 1u;
            uint8_t input = (uint8_t)((data[byte] >> (7u - bit - lane_bit)) & 1u);
            uint8_t feedback = (uint8_t)(((crc >> 15) & 1u) ^ input);
            crc = (uint16_t)(crc << 1);
            if (feedback) crc ^= 0x1021u;
        }
    }
    return crc;
}

static void emit_data_bus_line(hpe_sim_t *sim, hpe_block_card_t *card,
        uint8_t command_bit, const uint8_t lines[8], uint8_t value, const char *message) {
    size_t before;
    sim->time_ns += 125;
    before = sim->event_count;
    hpe_emit(sim, HPE_EVENT_LINE, value, 0, message);
    if (sim->event_count == before + 1) {
        hpe_event_t *event = &sim->events[before];
        event->clock = 0;
        event->mosi = command_bit;
        event->data_line_count = card->bus_width;
        memcpy(event->data_lines, lines, sizeof(event->data_lines));
    }
    sim->time_ns += 125;
    before = sim->event_count;
    hpe_emit(sim, HPE_EVENT_LINE, value, 0, message);
    if (sim->event_count == before + 1) {
        hpe_event_t *event = &sim->events[before];
        event->clock = 1;
        event->mosi = command_bit;
        event->data_line_count = card->bus_width;
        memcpy(event->data_lines, lines, sizeof(event->data_lines));
    }
}

static void emit_data_block(hpe_sim_t *sim, hpe_block_card_t *card,
        const uint8_t data[CARD_BLOCK_SIZE]) {
    uint8_t lines[8];
    uint16_t crc[8] = {0};
    unsigned width = card->bus_width;
    size_t byte;
    unsigned lane, bit;
    if (width != 1 && width != 4 && width != 8) width = 1;

    memset(lines, 1, sizeof(lines));
    memset(lines, 0, width);
    emit_data_bus_line(sim, card, 1, lines, 0, "SD/eMMC data start bit");
    for (byte = 0; byte < CARD_BLOCK_SIZE; ++byte) {
        for (bit = 0; bit < 8; bit += width) {
            uint8_t bus_value = 0;
            memset(lines, 1, sizeof(lines));
            for (lane = 0; lane < width; ++lane) {
                unsigned lane_bit = width - lane - 1u;
                unsigned source_bit = 7u - bit - lane_bit;
                uint8_t input = (uint8_t)((data[byte] >> source_bit) & 1u);
                uint8_t feedback = (uint8_t)(((crc[lane] >> 15) & 1u) ^ input);
                lines[lane] = input;
                bus_value |= (uint8_t)(input << lane);
                crc[lane] = (uint16_t)(crc[lane] << 1);
                if (feedback) crc[lane] ^= 0x1021u;
            }
            emit_data_bus_line(sim, card, 1, lines, bus_value, "SD/eMMC data payload");
        }
    }
    for (bit = 0; bit < 16; ++bit) {
        memset(lines, 1, sizeof(lines));
        for (lane = 0; lane < width; ++lane)
            lines[lane] = (uint8_t)((crc[lane] >> (15u - bit)) & 1u);
        emit_data_bus_line(sim, card, 1, lines, 0, "SD/eMMC per-line CRC16");
    }
    memset(lines, 1, sizeof(lines));
    emit_data_bus_line(sim, card, 1, lines, 0, "SD/eMMC data end bit");
}

static void emit_data_response(hpe_sim_t *sim, hpe_block_card_t *card) {
    static const uint8_t accepted_response[] = {0, 0, 1, 0, 1};
    uint8_t lines[8];
    size_t bit;
    unsigned busy_cycle;
    for (bit = 0; bit < sizeof(accepted_response); ++bit) {
        memset(lines, 1, sizeof(lines));
        lines[0] = accepted_response[bit];
        emit_data_bus_line(sim, card, 1, lines, 0, "SD/eMMC data response token");
    }
    memset(lines, 1, sizeof(lines));
    lines[0] = 0;
    for (busy_cycle = 0; busy_cycle < 1000; ++busy_cycle)
        emit_data_bus_line(sim, card, 1, lines, 0, "SD/eMMC write busy");
    lines[0] = 1;
    emit_data_bus_line(sim, card, 1, lines, 0, "SD/eMMC write ready");
}

static hpe_status_t emit_command(hpe_sim_t *sim, hpe_block_card_t *card,
        uint8_t command, uint32_t argument, uint32_t response, hpe_status_t status) {
    char message[96];
    hpe_event_t *event;
    uint64_t frame;
    uint64_t response_frame;
    uint64_t command_start;
    uint64_t response_start;
    int bit;
    sim->active_protocol = card->protocol;
    command_start = sim->time_ns;
    frame = ((uint64_t)(0x40u | command) << 40) | ((uint64_t)argument << 8) |
            command_crc7(command, argument);
    sim->spi_cs = 1;
    for (bit = 47; bit >= 0; --bit) {
        sim->spi_clock = 0;
        sim->spi_mosi = (uint8_t)((frame >> bit) & 1u);
        sim->time_ns += 1250;
        hpe_emit(sim, HPE_EVENT_LINE, sim->spi_mosi, 0, "card command clock low");
        sim->spi_clock = 1;
        sim->time_ns += 1250;
        hpe_emit(sim, HPE_EVENT_LINE, sim->spi_mosi, 0, "card command clock high");
    }
    snprintf(message, sizeof(message), "%s CMD%u CRC7=%02X", card->name,
             command, command_crc7(command, argument));
    hpe_emit(sim, HPE_EVENT_COMMAND, command, 0, message);
    event = &sim->events[sim->event_count - 1];
    event->data_start_ns = command_start;
    event->protocol_command = command;
    event->protocol_argument = argument;
    event->protocol_response = response;
    response_frame = ((uint64_t)(status == HPE_OK ? response : (1u << 22)) << 8) |
            crc7_first_40((uint64_t)(status == HPE_OK ? response : (1u << 22)) << 8);
    response_start = sim->time_ns;
    for (bit = 47; bit >= 0; --bit) {
        sim->spi_clock = 0;
        sim->spi_miso = (uint8_t)((response_frame >> bit) & 1u);
        sim->time_ns += 1250;
        hpe_emit(sim, HPE_EVENT_LINE, sim->spi_miso, 1, "card response clock low");
        sim->spi_clock = 1;
        sim->time_ns += 1250;
        hpe_emit(sim, HPE_EVENT_LINE, sim->spi_miso, 1, "card response clock high");
    }
    snprintf(message, sizeof(message), "%s state=%u response%s", card->name,
             (unsigned)card->state, status == HPE_OK ? " accepted" : " rejected");
    hpe_emit(sim, status == HPE_OK ? HPE_EVENT_RESPONSE : HPE_EVENT_ERROR,
             (uint8_t)response, 0, message);
    event = &sim->events[sim->event_count - 1];
    event->data_start_ns = response_start;
    event->protocol_command = command;
    event->protocol_argument = argument;
    event->protocol_response = response;
    return status;
}

hpe_status_t hpe_block_card_command(hpe_sim_t *sim, size_t card_index,
        uint8_t command, uint32_t argument, uint32_t *response) {
    hpe_block_card_t *card = card_at(sim, card_index);
    hpe_status_t status = HPE_OK;
    uint32_t result = 0;
    int app_command;
    if (!sim || !card) return HPE_ERR_NOT_FOUND;
    sim->active_protocol = card->protocol;
    app_command = card->application_command_pending && card->protocol == HPE_PROTOCOL_SD;
    card->application_command_pending = 0;
    if (command != 12) card->pending_data_command = 0;

    if (command == 0) {
        card->state = HPE_CARD_IDLE;
        card->initialization_attempts = 0;
        card->selected = 0;
        card->pending_data_command = 0;
        card->negotiated_high_capacity = 0;
        card->requested_ocr = 0;
        card->bus_width = 1;
        card->high_speed = 0;
    } else if (card->protocol == HPE_PROTOCOL_SD && command == 8) {
        if (card->state != HPE_CARD_IDLE) status = HPE_ERR_IO;
        else if (((argument >> 8) & 0x0fu) != 1u || (argument & 0xffu) != 0xaau)
            status = HPE_ERR_RANGE;
        else result = 0x000001aau; /* VHS=2.7–3.6 V and check pattern 0xAA. */
    } else if (card->protocol == HPE_PROTOCOL_SD && command == 55) {
        if (card->state != HPE_CARD_IDLE && (argument >> 16) != card->rca)
            status = HPE_ERR_IO;
        else {
            card->application_command_pending = 1;
            result = card->state == HPE_CARD_IDLE ? 0x00000001u : 0x00000000u;
        }
    } else if (card->protocol == HPE_PROTOCOL_SD && app_command && command == 41) {
        if (card->state != HPE_CARD_IDLE && card->state != HPE_CARD_READY) status = HPE_ERR_IO;
        else if (!(argument & SD_OCR_VOLTAGE_WINDOW)) status = HPE_ERR_RANGE;
        else {
            uint32_t voltage = argument & SD_OCR_VOLTAGE_WINDOW;
            if (card->requested_ocr && !(card->requested_ocr & voltage)) {
                card->initialization_attempts = 0;
                status = HPE_ERR_RANGE;
            } else {
                card->requested_ocr = voltage;
                if (card->initialization_attempts < 2) ++card->initialization_attempts;
            }
            if (status == HPE_OK && card->initialization_attempts >= 2 &&
                    (!card->high_capacity || (argument & SD_OCR_HIGH_CAPACITY))) {
                card->state = HPE_CARD_READY;
                card->negotiated_high_capacity = card->high_capacity &&
                        (argument & SD_OCR_HIGH_CAPACITY) != 0;
                result = SD_OCR_POWER_UP_STATUS | voltage |
                        (card->negotiated_high_capacity ? SD_OCR_HIGH_CAPACITY : 0u);
            } else if (status == HPE_OK) result = voltage;
        }
    } else if (card->protocol == HPE_PROTOCOL_EMMC && command == 1) {
        if (card->state != HPE_CARD_IDLE && card->state != HPE_CARD_READY) status = HPE_ERR_IO;
        else {
            if (card->initialization_attempts < 2) ++card->initialization_attempts;
            if (card->initialization_attempts >= 2) {
                card->state = HPE_CARD_READY;
                result = 0xc0ff8000u;
            } else result = 0x00ff8000u;
        }
    } else if (command == 2) {
        if (card->state != HPE_CARD_READY) status = HPE_ERR_IO;
        else card->state = HPE_CARD_IDENTIFICATION;
    } else if (card->protocol == HPE_PROTOCOL_SD && command == 3) {
        if (card->state != HPE_CARD_IDENTIFICATION) status = HPE_ERR_IO;
        else { card->state = HPE_CARD_STANDBY; result = (uint32_t)card->rca << 16; }
    } else if (card->protocol == HPE_PROTOCOL_EMMC && command == 3) {
        if (card->state != HPE_CARD_IDENTIFICATION) status = HPE_ERR_IO;
        else { card->state = HPE_CARD_STANDBY; result = (uint32_t)card->rca << 16; }
    } else if (command == 7) {
        if (card->state == HPE_CARD_STANDBY && (argument >> 16) == card->rca) {
            card->state = HPE_CARD_TRANSFER;
            card->selected = 1;
        } else if (card->state == HPE_CARD_TRANSFER && argument == 0) {
            card->state = HPE_CARD_STANDBY;
            card->selected = 0;
        } else status = HPE_ERR_IO;
    } else if (command == 12) {
        if (card->state == HPE_CARD_TRANSFER &&
                (card->pending_data_command == 18 || card->pending_data_command == 25)) {
            card->pending_data_command = 0;
            card->blocks_remaining = 0;
            card->predefined_block_count = 0;
        } else status = HPE_ERR_IO;
    } else if (card->protocol == HPE_PROTOCOL_SD && app_command && command == 6) {
        if (card->state != HPE_CARD_TRANSFER || !card->selected) status = HPE_ERR_IO;
        else if (argument == 0) card->bus_width = 1;
        else if (argument == 2) card->bus_width = 4;
        else status = HPE_ERR_RANGE;
    } else if (card->protocol == HPE_PROTOCOL_EMMC && command == 6) {
        uint8_t access = (uint8_t)((argument >> 24) & 0x03u);
        uint8_t index = (uint8_t)(argument >> 16);
        uint8_t value = (uint8_t)(argument >> 8);
        if (card->state != HPE_CARD_TRANSFER || !card->selected) status = HPE_ERR_IO;
        else if (access != 3u) status = HPE_ERR_RANGE; /* Write byte access only. */
        else if (index == 183u) { /* EXT_CSD BUS_WIDTH */
            if (value == 0u) card->bus_width = 1;
            else if (value == 1u) card->bus_width = 4;
            else if (value == 2u) card->bus_width = 8;
            else status = HPE_ERR_RANGE;
            if (status == HPE_OK) card->ext_csd[index] = value;
        } else if (index == 185u) { /* EXT_CSD HS_TIMING */
            if (value > 1u) status = HPE_ERR_RANGE;
            else {
                card->high_speed = value;
                card->ext_csd[index] = value;
            }
        } else status = HPE_ERR_RANGE;
    } else if (command == 23) {
        if (card->state != HPE_CARD_TRANSFER || !card->selected || !(argument & 0xffffu))
            status = HPE_ERR_IO;
        else card->predefined_block_count = argument & 0xffffu;
    } else if ((command == 9 || command == 10) &&
            card->state == HPE_CARD_STANDBY && (argument >> 16) == card->rca) {
        const uint8_t *register_bytes = command == 9 ? card->csd : card->cid;
        result = ((uint32_t)register_bytes[0] << 24) |
                ((uint32_t)register_bytes[1] << 16) |
                ((uint32_t)register_bytes[2] << 8) | register_bytes[3];
    } else if (command == 13) {
        result = ((uint32_t)card->state << 9) |
                (card->state == HPE_CARD_TRANSFER ? (1u << 8) : 0u);
    } else if (command == 16) {
        if (card->state != HPE_CARD_TRANSFER || argument != CARD_BLOCK_SIZE) status = HPE_ERR_IO;
    } else if (command == 17 || command == 18 || command == 24 || command == 25) {
        uint32_t block = argument;
        if (card->state != HPE_CARD_TRANSFER || !card->selected) status = HPE_ERR_IO;
        else if (card->protocol == HPE_PROTOCOL_SD && !card->negotiated_high_capacity) {
            if (argument % CARD_BLOCK_SIZE) status = HPE_ERR_RANGE;
            else block = argument / CARD_BLOCK_SIZE;
        }
        if (status == HPE_OK && block >= card->block_count) status = HPE_ERR_RANGE;
        if (status == HPE_OK) {
            card->pending_data_command = command;
            card->pending_data_block = block;
            card->blocks_remaining = (command == 18 || command == 25)
                    ? card->predefined_block_count : 1u;
            if (command == 18 || command == 25) card->predefined_block_count = 0;
        }
    } else if (card->protocol == HPE_PROTOCOL_EMMC && command == 8) {
        if (card->state != HPE_CARD_TRANSFER || !card->selected) status = HPE_ERR_IO;
        else {
            result = 0;
            card->pending_data_command = 8;
            card->pending_data_block = 0;
        }
    } else {
        status = HPE_ERR_IO;
    }
    if (response) *response = result;
    return emit_command(sim, card, command, argument, result, status);
}

hpe_status_t hpe_block_card_read(hpe_sim_t *sim, size_t card_index,
        uint32_t block, uint8_t data[CARD_BLOCK_SIZE]) {
    hpe_block_card_t *card = card_at(sim, card_index);
    if (!sim || !card || !data) return HPE_ERR_ARGUMENT;
    sim->active_protocol = card->protocol;
    if (card->state != HPE_CARD_TRANSFER || !card->selected ||
            (card->pending_data_command != 17 && card->pending_data_command != 18 &&
             !(card->protocol == HPE_PROTOCOL_EMMC && card->pending_data_command == 8)) ||
            card->pending_data_block != block) return HPE_ERR_IO;
    if (block >= card->block_count) return HPE_ERR_RANGE;
    if (card->protocol == HPE_PROTOCOL_EMMC && card->pending_data_command == 8) {
        memcpy(data, card->ext_csd, sizeof(card->ext_csd));
        emit_data_block(sim, card, data);
        hpe_emit(sim, HPE_EVENT_BLOCK, 8, 1, "eMMC EXT_CSD 512-byte data phase");
        sim->events[sim->event_count - 1].protocol_command = 8;
        sim->events[sim->event_count - 1].data_crc16 = data_line_crc16(data, card->bus_width, 0);
        card->pending_data_command = 0;
        return HPE_OK;
    }
    memset(data, card->fill, CARD_BLOCK_SIZE);
    for (hpe_card_page_t *page = card->pages; page; page = page->next) {
        if (page->block == block) { memcpy(data, page->data, CARD_BLOCK_SIZE); break; }
    }
    emit_data_block(sim, card, data);
    hpe_emit(sim, HPE_EVENT_BLOCK, (uint8_t)block, 1,
             card->pending_data_command == 18 ? "multiple-block read data phase with CRC16" :
             "single 512-byte block read with CRC16 phase");
    sim->events[sim->event_count - 1].protocol_argument = block;
    sim->events[sim->event_count - 1].data_crc16 = data_line_crc16(data, card->bus_width, 0);
    if (card->pending_data_command == 18) {
        ++card->pending_data_block;
        if (card->blocks_remaining && --card->blocks_remaining == 0)
            card->pending_data_command = 0;
    } else card->pending_data_command = 0;
    return HPE_OK;
}

hpe_status_t hpe_block_card_write(hpe_sim_t *sim, size_t card_index,
        uint32_t block, const uint8_t data[CARD_BLOCK_SIZE]) {
    hpe_block_card_t *card = card_at(sim, card_index);
    if (!sim || !card || !data) return HPE_ERR_ARGUMENT;
    sim->active_protocol = card->protocol;
    if (card->state != HPE_CARD_TRANSFER || !card->selected ||
            (card->pending_data_command != 24 && card->pending_data_command != 25) ||
            card->pending_data_block != block) return HPE_ERR_IO;
    if (block >= card->block_count) return HPE_ERR_RANGE;
    hpe_card_page_t *page;
    for (page = card->pages; page && page->block != block; page = page->next) {}
    if (!page) {
        page = (hpe_card_page_t *)malloc(sizeof(*page));
        if (!page) return HPE_ERR_NOMEM;
        page->block = block;
        memset(page->data, card->fill, sizeof(page->data));
        page->next = card->pages;
        card->pages = page;
    }
    memcpy(page->data, data, CARD_BLOCK_SIZE);
    emit_data_block(sim, card, data);
    emit_data_response(sim, card);
    hpe_emit(sim, HPE_EVENT_BLOCK, (uint8_t)block, 0,
             card->pending_data_command == 25 ? "multiple-block write data phase with CRC16 and data response" :
             "single 512-byte block write with CRC16 and data response");
    sim->events[sim->event_count - 1].protocol_argument = block;
    sim->events[sim->event_count - 1].data_crc16 = data_line_crc16(data, card->bus_width, 0);
    if (card->pending_data_command == 25) {
        ++card->pending_data_block;
        if (card->blocks_remaining && --card->blocks_remaining == 0)
            card->pending_data_command = 0;
    } else card->pending_data_command = 0;
    return HPE_OK;
}

hpe_status_t hpe_block_card_read_memory(const hpe_sim_t *sim, size_t card_index,
        uint32_t offset, uint8_t *data, size_t length) {
    hpe_block_card_t *card = card_at(sim, card_index);
    size_t i;
    if (!card || (length && !data)) return HPE_ERR_ARGUMENT;
    if (offset > (size_t)card->block_count * CARD_BLOCK_SIZE ||
            length > (size_t)card->block_count * CARD_BLOCK_SIZE - offset)
        return HPE_ERR_RANGE;
    for (i = 0; i < length; ++i) {
        uint32_t byte_offset = offset + (uint32_t)i;
        uint32_t block = byte_offset / CARD_BLOCK_SIZE;
        size_t in_block = byte_offset % CARD_BLOCK_SIZE;
        hpe_card_page_t *page;
        data[i] = card->fill;
        for (page = card->pages; page; page = page->next) {
            if (page->block == block) { data[i] = page->data[in_block]; break; }
        }
    }
    return HPE_OK;
}
