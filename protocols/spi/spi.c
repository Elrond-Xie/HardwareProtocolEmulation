#include "internal.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define SPI_MAX_FLASH_SIZE (16u * 1024u * 1024u)
#define SPI_PAGE_SIZE 256u
#define SPI_SECTOR_SIZE 4096u

hpe_spi_device_t *hpe_find_spi_device(hpe_sim_t *sim, uint8_t chip_select) {
    size_t i;
    if (!sim) return NULL;
    for (i = 0; i < sim->spi_count; ++i)
        if (sim->spi_devices[i]->chip_select == chip_select) return sim->spi_devices[i];
    return NULL;
}

size_t hpe_spi_device_count(const hpe_sim_t *sim) {
    return sim ? sim->spi_count : 0;
}

hpe_status_t hpe_spi_add_flash_device(hpe_sim_t *sim, const char *name,
        uint8_t chip_select, size_t memory_size, uint8_t fill) {
    hpe_spi_device_t *dev;
    size_t i;
    if (!sim || !name || !name[0] || !memory_size || memory_size > SPI_MAX_FLASH_SIZE)
        return HPE_ERR_ARGUMENT;
    if (hpe_find_spi_device(sim, chip_select)) return HPE_ERR_ARGUMENT;
    if (sim->spi_count == sim->spi_capacity) {
        size_t capacity = sim->spi_capacity ? sim->spi_capacity * 2 : 4;
        hpe_spi_device_t **devices = (hpe_spi_device_t **)realloc(
                sim->spi_devices, capacity * sizeof(*devices));
        if (!devices) return HPE_ERR_NOMEM;
        sim->spi_devices = devices;
        sim->spi_capacity = capacity;
    }
    dev = (hpe_spi_device_t *)calloc(1, sizeof(*dev));
    if (!dev) return HPE_ERR_NOMEM;
    dev->memory = (uint8_t *)malloc(memory_size);
    if (!dev->memory) { free(dev); return HPE_ERR_NOMEM; }
    memset(dev->memory, fill, memory_size);
    for (i = 0; i < sizeof(dev->name) - 1 && name[i]; ++i) dev->name[i] = name[i];
    dev->chip_select = chip_select;
    dev->memory_size = memory_size;
    /* Generic Winbond-like JEDEC ID; density code is log2(capacity in bits). */
    dev->jedec_id[0] = 0xEF;
    dev->jedec_id[1] = 0x40;
    dev->jedec_id[2] = 0;
    for (i = 0; i < 24; ++i)
        if (((size_t)1 << i) == memory_size) { dev->jedec_id[2] = (uint8_t)(i + 3); break; }
    sim->spi_devices[sim->spi_count++] = dev;
    return HPE_OK;
}

static uint32_t frame_address(const uint8_t *tx) {
    return ((uint32_t)tx[1] << 16) | ((uint32_t)tx[2] << 8) | tx[3];
}

static void protected_range(const hpe_spi_device_t *dev, size_t *start, size_t *end) {
    uint8_t bp = (uint8_t)((dev->status >> 2) & 7u);
    size_t protected_size = bp ? dev->memory_size >> (7u - bp) : 0;
    if (protected_size > dev->memory_size) protected_size = dev->memory_size;
    if (dev->status & 0x20u) {
        *start = 0;
        *end = protected_size;
    } else {
        *start = dev->memory_size - protected_size;
        *end = dev->memory_size;
    }
}

static int address_is_protected(const hpe_spi_device_t *dev, size_t address) {
    size_t start, end;
    protected_range(dev, &start, &end);
    return address >= start && address < end;
}

static void decode_frame(hpe_spi_device_t *dev, const uint8_t *tx,
        uint8_t *rx, size_t length) {
    size_t i;
    uint8_t command;
    uint32_t address = 0;
    if (!length) return;
    command = tx[0];
    memset(rx, 0xff, length);
    switch (command) {
    case 0x06: /* Write Enable */
        if (length == 1) dev->write_enabled = 1;
        break;
    case 0x04: /* Write Disable */
        if (length == 1) dev->write_enabled = 0;
        break;
    case 0x05: /* Read Status Register-1 */
        for (i = 1; i < length; ++i) rx[i] = (uint8_t)((dev->status & 0x01) |
                (dev->write_enabled ? 0x02 : 0));
        break;
    case 0x01: /* Write Status Register-1: SRWD, TB, BP2..BP0. */
        if (length == 2 && dev->write_enabled) {
            dev->status = (uint8_t)(tx[1] & 0xfcu);
            dev->write_enabled = 0;
        }
        break;
    case 0x9f: /* JEDEC ID */
        for (i = 1; i < length; ++i) rx[i] = dev->jedec_id[(i - 1) % 3];
        break;
    case 0x03: /* Read Data, 24-bit address, continuous read */
        if (length < 5) break;
        address = frame_address(tx);
        for (i = 4; i < length; ++i) {
            rx[i] = dev->memory[address % dev->memory_size];
            ++address;
        }
        break;
    case 0x02: /* Page Program; page address wraps within the selected page */
        if (!dev->write_enabled || length < 5) break;
        address = frame_address(tx);
        for (i = 4; i < length; ++i) {
            uint32_t target = (address & ~(SPI_PAGE_SIZE - 1u)) |
                    ((address + (uint32_t)(i - 4)) & (SPI_PAGE_SIZE - 1u));
            target %= (uint32_t)dev->memory_size;
            if (!address_is_protected(dev, target)) dev->memory[target] &= tx[i];
        }
        dev->write_enabled = 0;
        break;
    case 0x20: /* 4 KiB Sector Erase */
        if (dev->write_enabled && length == 4) {
            size_t start = (size_t)(frame_address(tx) % dev->memory_size);
            size_t sector = start & ~(size_t)(SPI_SECTOR_SIZE - 1u);
            size_t end = sector + SPI_SECTOR_SIZE;
            if (end > dev->memory_size) end = dev->memory_size;
            size_t protect_start, protect_end;
            protected_range(dev, &protect_start, &protect_end);
            if (!(sector < protect_end && end > protect_start))
                memset(dev->memory + sector, 0xff, end - sector);
            dev->write_enabled = 0;
        }
        break;
    default:
        break;
    }
}

static void emit_spi_byte(hpe_sim_t *sim, uint8_t tx, uint8_t rx, uint8_t mode) {
    unsigned bit;
    uint8_t idle = (uint8_t)((mode >> 1) & 1u);
    uint8_t leading = (uint8_t)!idle;
    uint8_t trailing = idle;
    sim->spi_clock = idle;
    for (bit = 0; bit < 8; ++bit) {
        unsigned shift = 7u - bit;
        if ((mode & 1u) == 0) { /* CPHA=0: data is sampled on leading edge. */
            sim->spi_mosi = (uint8_t)((tx >> shift) & 1u);
            sim->spi_miso = (uint8_t)((rx >> shift) & 1u);
            sim->spi_clock = leading;
            sim->time_ns += 500;
            hpe_emit(sim, HPE_EVENT_LINE, (uint8_t)(sim->spi_mosi << 1 | sim->spi_miso), 0,
                    "SPI leading clock edge");
            sim->spi_clock = trailing;
            sim->time_ns += 500;
            hpe_emit(sim, HPE_EVENT_LINE, (uint8_t)(sim->spi_mosi << 1 | sim->spi_miso), 0,
                    "SPI trailing clock edge");
        } else { /* CPHA=1: data is sampled on trailing edge. */
            sim->spi_clock = leading;
            sim->time_ns += 500;
            hpe_emit(sim, HPE_EVENT_LINE, (uint8_t)(sim->spi_mosi << 1 | sim->spi_miso), 0,
                    "SPI leading clock edge");
            sim->spi_mosi = (uint8_t)((tx >> shift) & 1u);
            sim->spi_miso = (uint8_t)((rx >> shift) & 1u);
            hpe_emit(sim, HPE_EVENT_LINE, (uint8_t)(sim->spi_mosi << 1 | sim->spi_miso), 0,
                    "SPI data changed after leading edge");
            sim->spi_clock = trailing;
            sim->time_ns += 500;
            hpe_emit(sim, HPE_EVENT_LINE, (uint8_t)(sim->spi_mosi << 1 | sim->spi_miso), 0,
                    "SPI trailing clock edge");
        }
    }
    sim->spi_clock = idle;
    hpe_emit(sim, HPE_EVENT_DATA, tx, 0, "SPI full-duplex byte (MOSI -> MISO)");
    sim->events[sim->event_count - 1].receive_value = rx;
}

hpe_status_t hpe_spi_transfer(hpe_sim_t *sim, uint8_t chip_select, uint8_t mode,
        const uint8_t *transmit, uint8_t *receive, size_t length) {
    hpe_spi_device_t *dev;
    uint8_t *rx;
    size_t i;
    char message[96];
    if (!sim || mode > 3 || !length || !transmit || !receive) return HPE_ERR_ARGUMENT;
    sim->active_protocol = HPE_PROTOCOL_SPI;
    dev = hpe_find_spi_device(sim, chip_select);
    rx = (uint8_t *)malloc(length);
    if (!rx) return HPE_ERR_NOMEM;
    sim->spi_clock = (uint8_t)((mode >> 1) & 1u);
    sim->spi_cs = 0;
    sim->time_ns += 1000;
    if (!dev) {
        hpe_emit(sim, HPE_EVENT_ERROR, chip_select, 0, "no SPI device on selected chip select");
        sim->spi_cs = 1;
        free(rx);
        return HPE_ERR_NACK;
    }
    decode_frame(dev, transmit, rx, length);
    snprintf(message, sizeof(message), "%s selected (CS%u, mode %u)", dev->name, chip_select, mode);
    hpe_emit(sim, HPE_EVENT_START, chip_select, 0, message);
    for (i = 0; i < length; ++i) {
        sim->spi_byte_start_ns = sim->time_ns;
        emit_spi_byte(sim, transmit[i], rx[i], mode);
    }
    memcpy(receive, rx, length);
    sim->spi_cs = 1;
    sim->time_ns += 1000;
    hpe_emit(sim, HPE_EVENT_STOP, chip_select, 0, "SPI chip select deasserted");
    free(rx);
    return HPE_OK;
}

hpe_status_t hpe_spi_read_memory(const hpe_sim_t *sim, uint8_t chip_select,
        uint32_t offset, uint8_t *data, size_t length) {
    const hpe_spi_device_t *dev;
    if (!sim || (length && !data)) return HPE_ERR_ARGUMENT;
    dev = NULL;
    for (size_t i = 0; i < sim->spi_count; ++i)
        if (sim->spi_devices[i]->chip_select == chip_select) { dev = sim->spi_devices[i]; break; }
    if (!dev) return HPE_ERR_NOT_FOUND;
    if (offset > dev->memory_size || length > dev->memory_size - offset) return HPE_ERR_RANGE;
    if (length) memcpy(data, dev->memory + offset, length);
    return HPE_OK;
}
