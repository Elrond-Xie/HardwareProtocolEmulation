#include "internal.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

hpe_sim_t *hpe_sim_create(void) {
    hpe_sim_t *sim = (hpe_sim_t *)calloc(1, sizeof(*sim));
    if (!sim) return NULL;
    sim->i2c.half_period_ns = 5000; /* 100 kHz standard-mode default */
    sim->i2c.scl = 1;
    sim->i2c.sda = 1;
    sim->active_protocol = HPE_PROTOCOL_I2C;
    sim->spi_cs = 1;
    sim->usb_dplus = 1; /* Full-speed idle is J (D+ high, D- low). */
    sim->usb_next_sof_time_ns = 1000000u;
    return sim;
}

void hpe_sim_destroy(hpe_sim_t *sim) {
    size_t i;
    if (!sim) return;
    for (i = 0; i < sim->i2c.count; ++i) {
        free(sim->i2c.devices[i]->registers);
        free(sim->i2c.devices[i]);
    }
    free(sim->i2c.devices);
    for (i = 0; i < sim->spi_count; ++i) {
        free(sim->spi_devices[i]->memory);
        free(sim->spi_devices[i]);
    }
    free(sim->spi_devices);
    for (i = 0; i < sim->block_card_count; ++i) {
        hpe_card_page_t *page = sim->block_cards[i]->pages;
        while (page) {
            hpe_card_page_t *next = page->next;
            free(page);
            page = next;
        }
        free(sim->block_cards[i]);
    }
    free(sim->block_cards);
    for (i = 0; i < sim->usb_device_count; ++i) {
        free(sim->usb_devices[i]->bulk_data);
        free(sim->usb_devices[i]);
    }
    free(sim->usb_devices);
    for (i = 0; i < sim->pcie_endpoint_count; ++i) {
        free(sim->pcie_endpoints[i]->bar0);
        free(sim->pcie_endpoints[i]);
    }
    free(sim->pcie_endpoints);
    free(sim->events);
    free(sim);
}

void hpe_sim_reset_trace(hpe_sim_t *sim) {
    if (sim) sim->event_count = 0;
}

size_t hpe_sim_event_count(const hpe_sim_t *sim) {
    return sim ? sim->event_count : 0;
}

const hpe_event_t *hpe_sim_event_at(const hpe_sim_t *sim, size_t index) {
    if (!sim || index >= sim->event_count) return NULL;
    return &sim->events[index];
}

size_t hpe_sim_device_count(const hpe_sim_t *sim) {
    return sim ? sim->i2c.count : 0;
}

int hpe_sim_device_at(const hpe_sim_t *sim, size_t index, hpe_device_info_t *out) {
    const hpe_device_t *dev;
    size_t name_length = 0;
    if (!sim || !out || index >= sim->i2c.count) return 0;
    dev = sim->i2c.devices[index];
    memset(out, 0, sizeof(*out));
    while (name_length < sizeof(out->name) - 1 && dev->name[name_length]) ++name_length;
    memcpy(out->name, dev->name, name_length);
    out->address = dev->address;
    out->present = 1;
    return 1;
}

size_t hpe_sim_virtual_device_count(const hpe_sim_t *sim) {
    if (!sim) return 0;
    return sim->i2c.count + sim->spi_count + sim->block_card_count +
            sim->usb_device_count + sim->pcie_endpoint_count;
}

int hpe_sim_virtual_device_at(const hpe_sim_t *sim, size_t index,
        hpe_virtual_device_info_t *out) {
    size_t i;
    if (!sim || !out || index >= hpe_sim_virtual_device_count(sim)) return 0;
    memset(out, 0, sizeof(*out));
    if (index < sim->i2c.count) {
        const hpe_device_t *device = sim->i2c.devices[index];
        out->protocol = HPE_PROTOCOL_I2C;
        memcpy(out->name, device->name, sizeof(out->name));
        out->locator = device->address;
        out->identity = device->pointer;
        out->memory_size = (uint32_t)device->register_count;
        out->state = (uint8_t)device->selected;
        return 1;
    }
    index -= sim->i2c.count;
    if (index < sim->spi_count) {
        const hpe_spi_device_t *device = sim->spi_devices[index];
        out->protocol = HPE_PROTOCOL_SPI;
        memcpy(out->name, device->name, sizeof(out->name));
        out->locator = device->chip_select;
        out->identity = ((uint32_t)device->jedec_id[0] << 16) |
                ((uint32_t)device->jedec_id[1] << 8) | device->jedec_id[2];
        out->memory_size = (uint32_t)device->memory_size;
        out->state = device->status;
        out->flags = device->write_enabled;
        return 1;
    }
    index -= sim->spi_count;
    if (index < sim->block_card_count) {
        const hpe_block_card_t *card = sim->block_cards[index];
        out->protocol = card->protocol;
        memcpy(out->name, card->name, sizeof(out->name));
        out->locator = (uint32_t)index;
        out->memory_size = card->block_count * 512u;
        out->identity = card->rca;
        out->state = (uint8_t)card->state;
        out->flags = card->selected;
        return 1;
    }
    index -= sim->block_card_count;
    if (index < sim->usb_device_count) {
        const hpe_usb_device_t *device = sim->usb_devices[index];
        out->protocol = HPE_PROTOCOL_USB;
        memcpy(out->name, device->name, sizeof(out->name));
        out->locator = device->address;
        out->identity = ((uint32_t)device->vendor_id << 16) | device->product_id;
        out->memory_size = (uint32_t)device->bulk_length;
        out->state = device->state;
        out->flags = device->configuration;
        return 1;
    }
    index -= sim->usb_device_count;
    for (i = 0; i < sim->pcie_endpoint_count; ++i) {
        const hpe_pcie_endpoint_t *endpoint = sim->pcie_endpoints[i];
        if (index == i) {
            out->protocol = HPE_PROTOCOL_PCIE;
            memcpy(out->name, endpoint->name, sizeof(out->name));
            out->locator = ((uint32_t)endpoint->bus << 8) |
                    ((uint32_t)endpoint->device << 3) | endpoint->function;
            out->identity = ((uint32_t)endpoint->vendor_id << 16) | endpoint->device_id;
            out->memory_size = endpoint->bar0_size;
            out->state = endpoint->link_up;
            out->flags = endpoint->negotiated_generation;
            return 1;
        }
    }
    return 0;
}

hpe_status_t hpe_sim_virtual_device_read_memory(const hpe_sim_t *sim,
        size_t index, uint32_t offset, uint8_t *data, size_t length) {
    const uint8_t *memory = NULL;
    size_t memory_size = 0;
    if (!sim || (length && !data) || index >= hpe_sim_virtual_device_count(sim))
        return HPE_ERR_ARGUMENT;
    if (index >= sim->i2c.count + sim->spi_count &&
            index < sim->i2c.count + sim->spi_count + sim->block_card_count)
        return hpe_block_card_read_memory(sim,
                index - sim->i2c.count - sim->spi_count, offset, data, length);
    if (index < sim->i2c.count) {
        memory = sim->i2c.devices[index]->registers;
        memory_size = sim->i2c.devices[index]->register_count;
    } else {
        index -= sim->i2c.count;
        if (index < sim->spi_count) {
            memory = sim->spi_devices[index]->memory;
            memory_size = sim->spi_devices[index]->memory_size;
        } else {
            index -= sim->spi_count;
            index -= sim->block_card_count;
            if (index < sim->usb_device_count) {
                memory = sim->usb_devices[index]->bulk_data;
                memory_size = sim->usb_devices[index]->bulk_length;
            } else {
                index -= sim->usb_device_count;
                if (index < sim->pcie_endpoint_count) {
                    memory = sim->pcie_endpoints[index]->bar0;
                    memory_size = sim->pcie_endpoints[index]->bar0_size;
                }
            }
        }
    }
    if (offset > memory_size || length > memory_size - offset) return HPE_ERR_RANGE;
    if (length) memcpy(data, memory + offset, length);
    return HPE_OK;
}

int hpe_i2c_device_state_at(const hpe_sim_t *sim, size_t index,
        hpe_i2c_device_state_t *out) {
    const hpe_device_t *dev;
    size_t name_length = 0;
    if (!sim || !out || index >= sim->i2c.count) return 0;
    dev = sim->i2c.devices[index];
    memset(out, 0, sizeof(*out));
    while (name_length < sizeof(out->name) - 1 && dev->name[name_length]) ++name_length;
    memcpy(out->name, dev->name, name_length);
    out->address = dev->address;
    out->present = 1;
    out->register_pointer = dev->pointer;
    out->register_count = dev->register_count;
    return 1;
}

hpe_status_t hpe_i2c_read_memory(const hpe_sim_t *sim, uint8_t address,
        uint32_t offset, uint8_t *data, size_t length) {
    const hpe_device_t *dev;
    if (!sim || address > HPE_I2C_MAX_ADDRESS || (length && !data))
        return HPE_ERR_ARGUMENT;
    dev = NULL;
    for (size_t i = 0; i < sim->i2c.count; ++i) {
        if (sim->i2c.devices[i]->address == address) {
            dev = sim->i2c.devices[i];
            break;
        }
    }
    if (!dev) return HPE_ERR_NOT_FOUND;
    if (offset > dev->register_count || length > dev->register_count - offset)
        return HPE_ERR_RANGE;
    if (length) memcpy(data, dev->registers + offset, length);
    return HPE_OK;
}

hpe_status_t hpe_i2c_read_memory_at(const hpe_sim_t *sim, size_t device_index,
        uint32_t offset, uint8_t *data, size_t length) {
    const hpe_device_t *dev;
    if (!sim || (length && !data)) return HPE_ERR_ARGUMENT;
    if (device_index >= sim->i2c.count) return HPE_ERR_NOT_FOUND;
    dev = sim->i2c.devices[device_index];
    if (offset > dev->register_count || length > dev->register_count - offset)
        return HPE_ERR_RANGE;
    if (length) memcpy(data, dev->registers + offset, length);
    return HPE_OK;
}

hpe_device_t *hpe_find_device(hpe_sim_t *sim, uint8_t address) {
    size_t i;
    if (!sim) return NULL;
    for (i = 0; i < sim->i2c.count; ++i)
        if (sim->i2c.devices[i]->address == address) return sim->i2c.devices[i];
    return NULL;
}

void hpe_emit(hpe_sim_t *sim, hpe_event_kind_t kind, uint8_t value,
              uint8_t read, const char *message) {
    hpe_event_t *event;
    if (!sim) return;
    if (sim->event_count == sim->event_capacity) {
        size_t cap = sim->event_capacity ? sim->event_capacity * 2 : 256;
        hpe_event_t *next = (hpe_event_t *)realloc(sim->events, cap * sizeof(*next));
        if (!next) return;
        sim->events = next;
        sim->event_capacity = cap;
    }
    event = &sim->events[sim->event_count++];
    memset(event, 0, sizeof(*event));
    event->time_ns = sim->active_protocol == HPE_PROTOCOL_I2C ?
                     (sim->i2c.time_ns > sim->time_ns ? sim->i2c.time_ns : sim->time_ns) :
                     sim->time_ns;
    event->data_start_ns = sim->active_protocol == HPE_PROTOCOL_I2C ?
                           sim->i2c.byte_start_ns :
                           (sim->active_protocol == HPE_PROTOCOL_SPI ? sim->spi_byte_start_ns : sim->time_ns);
    event->kind = kind;
    event->scl = sim->i2c.scl;
    event->sda = sim->i2c.sda;
    event->value = value;
    event->read = read;
    event->protocol = sim->active_protocol;
    event->clock = sim->active_protocol == HPE_PROTOCOL_I2C ? sim->i2c.scl : sim->spi_clock;
    event->mosi = sim->spi_mosi;
    event->miso = sim->spi_miso;
    event->chip_select = sim->spi_cs;
    if (message) strncpy(event->message, message, sizeof(event->message) - 1);
    if (event->time_ns > sim->time_ns) sim->time_ns = event->time_ns;
}

const char *hpe_protocol_name(hpe_protocol_t protocol) {
    switch (protocol) {
    case HPE_PROTOCOL_I2C: return "I2C";
    case HPE_PROTOCOL_SPI: return "SPI";
    case HPE_PROTOCOL_SD: return "SD";
    case HPE_PROTOCOL_EMMC: return "eMMC";
    case HPE_PROTOCOL_USB: return "USB";
    case HPE_PROTOCOL_PCIE: return "PCIe";
    default: return "None";
    }
}

const char *hpe_status_string(hpe_status_t status) {
    switch (status) {
    case HPE_OK: return "success";
    case HPE_ERR_ARGUMENT: return "invalid argument";
    case HPE_ERR_NOMEM: return "out of memory";
    case HPE_ERR_NOT_FOUND: return "device not found";
    case HPE_ERR_NACK: return "NACK";
    case HPE_ERR_RANGE: return "out of range";
    case HPE_ERR_IO: return "I/O error";
    case HPE_ERR_PARSE: return "scenario parse error";
    default: return "unknown error";
    }
}

const char *hpe_event_kind_name(hpe_event_kind_t kind) {
    switch (kind) {
    case HPE_EVENT_LINE: return "LINE";
    case HPE_EVENT_START: return "START";
    case HPE_EVENT_RESTART: return "RESTART";
    case HPE_EVENT_STOP: return "STOP";
    case HPE_EVENT_ADDRESS: return "ADDRESS";
    case HPE_EVENT_DATA: return "DATA";
    case HPE_EVENT_ACK: return "ACK";
    case HPE_EVENT_NACK: return "NACK";
    case HPE_EVENT_INFO: return "INFO";
    case HPE_EVENT_ERROR: return "ERROR";
    case HPE_EVENT_COMMAND: return "COMMAND";
    case HPE_EVENT_RESPONSE: return "RESPONSE";
    case HPE_EVENT_BLOCK: return "BLOCK";
    default: return "UNKNOWN";
    }
}

hpe_status_t hpe_trace_write_text(const hpe_sim_t *sim, const char *path) {
    FILE *file;
    size_t i;
    if (!sim || !path) return HPE_ERR_ARGUMENT;
    file = fopen(path, "w");
    if (!file) return HPE_ERR_IO;
    fputs("time_ns  protocol clock data1 data2 cs event      value  details\n", file);
    for (i = 0; i < sim->event_count; ++i) {
        const hpe_event_t *e = &sim->events[i];
        if (e->kind == HPE_EVENT_COMMAND || e->kind == HPE_EVENT_RESPONSE)
            fprintf(file, "%8llu  %-8s CMD%02u ARG=%08X RESP=%08X %s\n",
                    (unsigned long long)e->time_ns, hpe_protocol_name(e->protocol),
                    e->protocol_command, e->protocol_argument, e->protocol_response,
                    e->message);
        else if (e->kind == HPE_EVENT_BLOCK)
            fprintf(file, "%8llu  %-8s BLOCK=%08X CRC16=%04X %s\n",
                    (unsigned long long)e->time_ns, hpe_protocol_name(e->protocol),
                    e->protocol_argument, e->data_crc16, e->message);
        else if (e->protocol == HPE_PROTOCOL_USB)
            fprintf(file, "%8llu  USB      addr=%03u ep=%u PID=%02X len=%u %-10s 0x%02X %s\n",
                    (unsigned long long)e->time_ns, e->usb_address, e->usb_endpoint,
                    e->usb_pid, e->payload_length, hpe_event_kind_name(e->kind), e->value, e->message);
        else if (e->protocol == HPE_PROTOCOL_I2C)
            fprintf(file, "%8llu  %-8s %5u %5u %5s %2s %-10s 0x%02X   %s\n",
                    (unsigned long long)e->time_ns, hpe_protocol_name(e->protocol),
                    e->scl, e->sda, "-", "-", hpe_event_kind_name(e->kind), e->value, e->message);
        else
            fprintf(file, "%8llu  %-8s %5u %5u %5u %2u %-10s 0x%02X   %s\n",
                    (unsigned long long)e->time_ns, hpe_protocol_name(e->protocol),
                    e->clock, e->mosi, e->miso, e->chip_select,
                    hpe_event_kind_name(e->kind), e->value, e->message);
    }
    if (fclose(file) != 0) return HPE_ERR_IO;
    return HPE_OK;
}
