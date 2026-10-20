/*
 * File: pcie.c
 * Purpose: Project-owned implementation and interface for protocols / pcie / pcie.c.
 * Author: Elrond-Xie
 * Copyright (c) 2026 Elrond-Xie. SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0.
 */

#include "internal.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define PCIE_BAR_MAX_SIZE (16u * 1024u * 1024u)
#define PCIE_HOST_MAX_GENERATION 4u
#define PCIE_HOST_MAX_LANES 16u

/** @brief Implements the endpoint_at operation. */
static hpe_pcie_endpoint_t *endpoint_at(const hpe_sim_t *sim, size_t index)
{
    if (!sim || index >= sim->pcie_endpoint_count) {
        return NULL;
    }
    return sim->pcie_endpoints[index];
}

/** @brief Implements the put_le operation. */
static void put_le(uint8_t *destination, uint32_t value, uint8_t width)
{
    uint8_t i;
    for (i = 0; i < width; ++i) {
        destination[i] = (uint8_t)(value >> (i * 8u));
    }
}

/** @brief Implements the get_le operation. */
static uint32_t get_le(const uint8_t *source, uint8_t width)
{
    uint32_t value = 0;
    uint8_t i;
    for (i = 0; i < width; ++i) {
        value |= (uint32_t)source[i] << (i * 8u);
    }
    return value;
}

/** @brief Implements the valid_width operation. */
static int valid_width(uint32_t offset, uint8_t width, uint32_t limit)
{
    return (width == 1 || width == 2 || width == 4) && !(offset & (uint32_t)(width - 1)) &&
           offset <= limit && width <= limit - offset;
}

/** @brief Implements the pcie_reject operation. */
static hpe_status_t pcie_reject(hpe_sim_t *sim, const char *message, hpe_status_t status)
{
    sim->active_protocol = HPE_PROTOCOL_PCIE;
    sim->time_ns += 1000;
    hpe_emit(sim, HPE_EVENT_ERROR, (uint8_t)(-status), 0, message);
    return status;
}

/** @brief Implements the endpoint_id operation. */
static uint16_t endpoint_id(const hpe_pcie_endpoint_t *endpoint)
{
    return (uint16_t)(((uint16_t)endpoint->bus << 8) | ((uint16_t)endpoint->device << 3) |
                      endpoint->function);
}

/** @brief Implements the pcie_lcrc operation. */
static uint32_t pcie_lcrc(const uint8_t *bytes, size_t length)
{
    uint32_t crc = 0xffffffffu;
    size_t i;
    unsigned bit;
    for (i = 0; i < length; ++i) {
        crc ^= bytes[i];
        for (bit = 0; bit < 8; ++bit) {
            crc = (crc >> 1) ^ ((crc & 1u) ? 0xedb88320u : 0u);
        }
    }
    return ~crc;
}

/** @brief Implements the pcie_dllp_crc operation. */
static uint16_t pcie_dllp_crc(const uint8_t bytes[4])
{
    uint16_t crc = 0xffffu;
    size_t i;
    unsigned bit;
    for (i = 0; i < 4; ++i) {
        crc ^= bytes[i];
        for (bit = 0; bit < 8; ++bit) {
            crc = (uint16_t)((crc >> 1) ^ ((crc & 1u) ? 0xd008u : 0u));
        }
    }
    crc = (uint16_t)~crc;
    /* The complemented remainder is mapped to the DLLP CRC field MSB first. */
    return (uint16_t)((crc << 8) | (crc >> 8));
}

/** @brief Implements the emit_dllp operation. */
static void emit_dllp(hpe_sim_t *sim, const hpe_pcie_endpoint_t *endpoint, uint8_t type,
                      uint16_t sequence)
{
    char message[96];
    hpe_event_t *event;
    uint8_t dllp[6] = {type, 0, (uint8_t)((sequence >> 8) & 0x0f), (uint8_t)sequence, 0, 0};
    uint16_t crc = pcie_dllp_crc(dllp);
    dllp[4] = (uint8_t)(crc >> 8);
    dllp[5] = (uint8_t)crc;
    snprintf(message, sizeof(message), "%s %s DLLP Seq=%u CRC16=0x%04X", endpoint->name,
             type == 0 ? "ACK" : "NAK", sequence, crc);
    hpe_emit(sim, HPE_EVENT_ACK, (uint8_t)sequence, 0, message);
    event = &sim->events[sim->event_count - 1];
    event->protocol_command = 0x00;
    event->protocol_argument = sequence;
    event->protocol_response = crc;
    event->pcie_dllp_type = type;
    event->pcie_dllp_ack_sequence = sequence;
    memcpy(event->pcie_dllp, dllp, sizeof(dllp));
    event->pcie_dllp_length = sizeof(dllp);
    event->pcie_dllp_crc16 = crc;
}

/** @brief Implements the emit_ack_dllp operation. */
static void emit_ack_dllp(hpe_sim_t *sim, const hpe_pcie_endpoint_t *endpoint, uint16_t sequence)
{
    emit_dllp(sim, endpoint, 0x00, sequence);
}

/** @brief Implements the emit_tlp operation. */
static void emit_tlp(hpe_sim_t *sim, const hpe_pcie_endpoint_t *endpoint, hpe_event_kind_t kind,
                     uint8_t fmt_type, uint8_t tag, uint8_t width, uint32_t address,
                     uint8_t byte_lane, uint32_t response, int config_request,
                     const char *description)
{
    char message[96];
    uint8_t header[12], payload[4], header_length = 12, payload_length = 0;
    uint8_t first_be = (uint8_t)(((1u << width) - 1u) << (byte_lane & 3u));
    uint8_t last_be = 0; /* A single-DW request uses FirstBE and clears LastBE. */
    uint32_t dw0, dw1, dw2;
    uint32_t data = response;
    uint16_t sequence;
    uint8_t crc_input[2 + sizeof(header) + sizeof(payload)];
    size_t crc_length;
    int inject_error = sim->pcie_corrupt_next_tlp;
    hpe_event_t replay_event;
    int completion = fmt_type == 0x0a || fmt_type == 0x4a;
    int with_data = fmt_type == 0x4a || fmt_type == 0x44 || fmt_type == 0x40;
    int from_endpoint = completion;
    uint8_t byte_enable = (uint8_t)(((1u << width) - 1u) << (byte_lane & 3u));
    uint16_t completer = endpoint_id(endpoint);
    uint32_t length_dw = with_data ? 1u : (completion ? 0u : 1u);
    sim->pcie_corrupt_next_tlp = 0;
    if (fmt_type == 0x44 || fmt_type == 0x40) {
        data <<= (byte_lane & 3u) * 8u;
        payload_length = 4;
        put_le(payload, data, 4);
    }
    else if (fmt_type == 0x4a) {
        data <<= (byte_lane & 3u) * 8u;
        payload_length = 4;
        put_le(payload, data, 4);
    }
    if (completion) {
        /* Cpl/CplD: completer, successful status, byte count, requester/tag/lower address. */
        dw1 = ((uint32_t)completer << 16) | (fmt_type == 0x4a ? width : 0u);
        dw2 = ((uint32_t)tag << 8) | (address & 0x7fu);
    }
    else {
        /* Requester ID is the simulated root complex 0000:00:00.0. */
        dw1 = ((uint32_t)tag << 8) | ((uint32_t)last_be << 4) | first_be;
        if (config_request) {
            uint8_t bus = (uint8_t)(address >> 16);
            uint8_t device = (uint8_t)((address >> 11) & 0x1fu);
            uint8_t function = (uint8_t)((address >> 8) & 0x7u);
            uint16_t reg = (uint16_t)(address & 0xfcu);
            dw2 =
                ((uint32_t)bus << 20) | ((uint32_t)device << 15) | ((uint32_t)function << 12) | reg;
        }
        else {
            dw2 = address & ~3u;
        }
    }
    dw0 = ((uint32_t)fmt_type << 24) | (length_dw & 0x3ffu);
    put_le(header, dw0, 4);
    put_le(header + 4, dw1, 4);
    put_le(header + 8, dw2, 4);
    if (from_endpoint) {
        sequence = sim->pcie_endpoint_sequence;
        sim->pcie_endpoint_sequence = (uint16_t)((sim->pcie_endpoint_sequence + 1u) & 0x0fffu);
    }
    else {
        sequence = sim->pcie_host_sequence;
        sim->pcie_host_sequence = (uint16_t)((sim->pcie_host_sequence + 1u) & 0x0fffu);
    }
    /* Four reserved bits precede sequence[11:8] in byte 0; byte 1 carries [7:0]. */
    crc_input[0] = (uint8_t)((sequence >> 8) & 0x0fu);
    crc_input[1] = (uint8_t)sequence;
    memcpy(crc_input + 2, header, header_length);
    if (payload_length) {
        memcpy(crc_input + 2 + header_length, payload, payload_length);
    }
    crc_length = 2 + header_length + payload_length;
    snprintf(message, sizeof(message), "%s TLP %s Fmt/Type=0x%02X Tag=%u Len=%uDW ReqBE=0x%X",
             endpoint->name, description, fmt_type, tag, length_dw, byte_enable);
    hpe_emit(sim, kind, (uint8_t)response, 0, message);
    hpe_event_t *event = &sim->events[sim->event_count - 1];
    event->protocol_command = fmt_type;
    event->protocol_argument = address;
    event->protocol_response = response;
    event->pcie_tlp_fmt_type = fmt_type;
    event->pcie_tlp_tag = tag;
    event->pcie_tlp_length_dw = fmt_type == 0x0A ? 0 : 1;
    event->pcie_tlp_byte_enable = byte_enable;
    event->pcie_requester_id = 0;
    event->pcie_completer_id = endpoint_id(endpoint);
    memcpy(event->pcie_tlp_header, header, header_length);
    event->pcie_tlp_header_length = header_length;
    if (payload_length) {
        memcpy(event->pcie_tlp_payload, payload, payload_length);
    }
    event->pcie_tlp_payload_length = payload_length;
    event->pcie_tlp_sequence = sequence;
    event->pcie_lcrc = pcie_lcrc(crc_input, crc_length);
    if (inject_error) {
        event->pcie_tlp_corrupted = 1;
        event->pcie_tlp_header[0] ^= 0x01u;
        snprintf(event->message + strlen(event->message),
                 sizeof(event->message) - strlen(event->message), " [injected LCRC error]");
        replay_event = *event;
        replay_event.pcie_tlp_corrupted = 0;
        replay_event.pcie_tlp_replay = 1;
        memcpy(replay_event.pcie_tlp_header, header, header_length);
        snprintf(replay_event.message, sizeof(replay_event.message),
                 "%s TLP replay after NAK Seq=%u", endpoint->name, sequence);
        emit_dllp(sim, endpoint, 0x10, (uint16_t)((sequence - 1u) & 0x0fffu));
        hpe_emit(sim, kind, (uint8_t)response, 0, replay_event.message);
        sim->events[sim->event_count - 1] = replay_event;
    }
    emit_ack_dllp(sim, endpoint, sequence);
}

/** @brief Implements the hpe_pcie_add_endpoint operation. */
hpe_status_t hpe_pcie_add_endpoint(hpe_sim_t *sim, const char *name, uint8_t bus, uint8_t device,
                                   uint8_t function, uint16_t vendor_id, uint16_t device_id,
                                   uint8_t class_code, uint8_t subclass,
                                   uint8_t programming_interface, uint32_t bar0_size,
                                   uint8_t max_generation, uint8_t max_lanes)
{
    hpe_pcie_endpoint_t *endpoint;
    size_t i, name_length = 0;
    if (!sim || !name || !name[0] || device > 31 || function > 7 || vendor_id == 0xffffu ||
        bar0_size < 256 || bar0_size > PCIE_BAR_MAX_SIZE || (bar0_size & (bar0_size - 1u)) ||
        !max_generation || max_generation > 7 || !max_lanes || max_lanes > 32 ||
        (max_lanes & (max_lanes - 1u))) {
        return HPE_ERR_ARGUMENT;
    }
    for (i = 0; i < sim->pcie_endpoint_count; ++i) {
        hpe_pcie_endpoint_t *existing = sim->pcie_endpoints[i];
        if (existing->bus == bus && existing->device == device && existing->function == function) {
            return HPE_ERR_ARGUMENT;
        }
    }
    if (sim->pcie_endpoint_count == sim->pcie_endpoint_capacity) {
        size_t capacity = sim->pcie_endpoint_capacity ? sim->pcie_endpoint_capacity * 2 : 4;
        hpe_pcie_endpoint_t **endpoints =
            (hpe_pcie_endpoint_t **)realloc(sim->pcie_endpoints, capacity * sizeof(*endpoints));
        if (!endpoints) {
            return HPE_ERR_NOMEM;
        }
        sim->pcie_endpoints = endpoints;
        sim->pcie_endpoint_capacity = capacity;
    }
    endpoint = (hpe_pcie_endpoint_t *)calloc(1, sizeof(*endpoint));
    if (!endpoint) {
        return HPE_ERR_NOMEM;
    }
    endpoint->bar0 = (uint8_t *)calloc(bar0_size, 1);
    if (!endpoint->bar0) {
        free(endpoint);
        return HPE_ERR_NOMEM;
    }
    while (name_length < sizeof(endpoint->name) - 1 && name[name_length]) {
        ++name_length;
    }
    memcpy(endpoint->name, name, name_length);
    endpoint->bus = bus;
    endpoint->device = device;
    endpoint->function = function;
    endpoint->vendor_id = vendor_id;
    endpoint->device_id = device_id;
    endpoint->bar0_size = bar0_size;
    endpoint->max_generation = max_generation;
    endpoint->max_lanes = max_lanes;
    put_le(endpoint->config + 0x00, vendor_id, 2);
    put_le(endpoint->config + 0x02, device_id, 2);
    put_le(endpoint->config + 0x04, 0, 2);      /* Command: memory and bus mastering disabled. */
    put_le(endpoint->config + 0x06, 0x0010, 2); /* Capabilities list present. */
    endpoint->config[0x08] = 0x01;
    endpoint->config[0x09] = programming_interface;
    endpoint->config[0x0a] = subclass;
    endpoint->config[0x0b] = class_code;
    endpoint->config[0x0e] = 0x00; /* Type 0 endpoint header. */
    put_le(endpoint->config + 0x2c, vendor_id, 2);
    put_le(endpoint->config + 0x2e, device_id, 2);
    endpoint->config[0x34] = 0x40;
    endpoint->config[0x40] = 0x10; /* PCI Express Capability. */
    endpoint->config[0x41] = 0;
    endpoint->config[0x42] = 0x02; /* Capability version 2, endpoint type. */
    endpoint->config[0x43] = 0;
    put_le(endpoint->config + 0x4c, (uint32_t)max_generation | ((uint32_t)max_lanes << 4), 4);
    sim->pcie_endpoints[sim->pcie_endpoint_count++] = endpoint;
    return HPE_OK;
}

/** @brief Implements the hpe_pcie_endpoint_count operation. */
size_t hpe_pcie_endpoint_count(const hpe_sim_t *sim)
{
    return sim ? sim->pcie_endpoint_count : 0;
}

/** @brief Implements the link_state operation. */
static void link_state(hpe_sim_t *sim, hpe_pcie_endpoint_t *endpoint, const char *state,
                       uint64_t duration_ns)
{
    char message[96];
    sim->active_protocol = HPE_PROTOCOL_PCIE;
    sim->time_ns += duration_ns;
    snprintf(message, sizeof(message), "%s LTSSM -> %s", endpoint->name, state);
    hpe_emit(sim, HPE_EVENT_INFO, 0, 0, message);
}

/** @brief Implements the train_link operation. */
static void train_link(hpe_sim_t *sim, hpe_pcie_endpoint_t *endpoint)
{
    if (endpoint->link_up) {
        return;
    }
    link_state(sim, endpoint, "Detect", 100000);
    link_state(sim, endpoint, "Polling.Active / Polling.Configuration", 1000000);
    link_state(sim, endpoint, "Configuration.Linkwidth.Start / Accept", 1000000);
    endpoint->negotiated_generation = endpoint->max_generation < PCIE_HOST_MAX_GENERATION
                                          ? endpoint->max_generation
                                          : PCIE_HOST_MAX_GENERATION;
    endpoint->negotiated_lanes =
        endpoint->max_lanes < PCIE_HOST_MAX_LANES ? endpoint->max_lanes : PCIE_HOST_MAX_LANES;
    put_le(endpoint->config + 0x52,
           (uint32_t)endpoint->negotiated_generation | ((uint32_t)endpoint->negotiated_lanes << 4),
           2);
    link_state(sim, endpoint, "L0", 100000);
    endpoint->link_up = 1;
    {
        char message[96];
        snprintf(message, sizeof(message), "%s link up: Gen%u x%u", endpoint->name,
                 endpoint->negotiated_generation, endpoint->negotiated_lanes);
        hpe_emit(sim, HPE_EVENT_INFO, endpoint->negotiated_generation, 0, message);
    }
}

/** @brief Implements the config_access operation. */
static hpe_status_t config_access(hpe_sim_t *sim, hpe_pcie_endpoint_t *endpoint, uint16_t offset,
                                  uint8_t width, uint32_t *value, int write)
{
    uint32_t result;
    uint8_t tag, request_type, completion_type;
    if (!valid_width(offset, width, sizeof(endpoint->config))) {
        return pcie_reject(sim, "configuration request rejected: invalid offset or width",
                           HPE_ERR_RANGE);
    }
    if (!endpoint->link_up) {
        return pcie_reject(sim, "configuration request completion abort: link is not in L0",
                           HPE_ERR_IO);
    }
    sim->active_protocol = HPE_PROTOCOL_PCIE;
    if (write) {
        if (offset < 4 || (offset >= 4 && offset < 8 && !(offset == 4 && width == 2)) ||
            (offset >= 8 && offset < 0x10) || offset > 0x10 || (offset == 0x10 && width != 4)) {
            return pcie_reject(
                sim, "configuration write rejected: read-only or unsupported register", HPE_ERR_IO);
        }
        if (offset == 0x10) {
            if (*value == 0xffffffffu) {
                endpoint->bar0_probe = 1;
            }
            else {
                endpoint->bar0_probe = 0;
                put_le(endpoint->config + offset, *value & ~(endpoint->bar0_size - 1u), width);
            }
        }
        else {
            put_le(endpoint->config + offset, *value, width);
        }
        result = *value;
    }
    else {
        if (offset == 0x10 && width == 4 && endpoint->bar0_probe) {
            result = ~(endpoint->bar0_size - 1u) & 0xfffffff0u;
        }
        else {
            result = get_le(endpoint->config + offset, width);
        }
        *value = result;
    }
    tag = sim->pcie_next_tag++;
    request_type = write ? 0x44 : 0x04;    /* CfgWr0 / CfgRd0, 3DW header. */
    completion_type = write ? 0x0A : 0x4A; /* Cpl / CplD. */
    sim->time_ns += 500;
    emit_tlp(sim, endpoint, HPE_EVENT_COMMAND, request_type, tag, width,
             ((uint32_t)endpoint->bus << 16) | ((uint32_t)endpoint->device << 11) |
                 ((uint32_t)endpoint->function << 8) | (offset & 0xFCu),
             (uint8_t)(offset & 3u), result, 1, write ? "CfgWr0 request" : "CfgRd0 request");
    sim->time_ns += 500;
    emit_tlp(sim, endpoint, HPE_EVENT_RESPONSE, completion_type, tag, width, offset,
             (uint8_t)(offset & 3u), result, 0, write ? "Cpl completion" : "CplD completion");
    if (write) {
        sim->events[sim->event_count - 1].protocol_response = 0;
    }
    return HPE_OK;
}

/** @brief Implements the hpe_pcie_enumerate operation. */
hpe_status_t hpe_pcie_enumerate(hpe_sim_t *sim, hpe_pcie_bdf_t *devices, size_t capacity,
                                size_t *found)
{
    size_t i, count = 0;
    if (!sim || !found || (capacity && !devices)) {
        return HPE_ERR_ARGUMENT;
    }
    sim->active_protocol = HPE_PROTOCOL_PCIE;
    for (i = 0; i < sim->pcie_endpoint_count; ++i) {
        hpe_pcie_endpoint_t *endpoint = sim->pcie_endpoints[i];
        uint32_t id;
        hpe_status_t status;
        train_link(sim, endpoint);
        status = config_access(sim, endpoint, 0, 4, &id, 0);
        if (status != HPE_OK || (id & 0xffffu) == 0xffffu) {
            continue;
        }
        if (count < capacity) {
            devices[count].bus = endpoint->bus;
            devices[count].device = endpoint->device;
            devices[count].function = endpoint->function;
            devices[count].vendor_id = (uint16_t)id;
            devices[count].device_id = (uint16_t)(id >> 16);
        }
        ++count;
    }
    *found = count;
    return count > capacity ? HPE_ERR_RANGE : HPE_OK;
}

/** @brief Implements the hpe_pcie_config_read operation. */
hpe_status_t hpe_pcie_config_read(hpe_sim_t *sim, size_t endpoint_index, uint16_t offset,
                                  uint8_t width, uint32_t *value)
{
    hpe_pcie_endpoint_t *endpoint = endpoint_at(sim, endpoint_index);
    if (!sim || !endpoint || !value) {
        return HPE_ERR_ARGUMENT;
    }
    return config_access(sim, endpoint, offset, width, value, 0);
}

/** @brief Implements the hpe_pcie_config_write operation. */
hpe_status_t hpe_pcie_config_write(hpe_sim_t *sim, size_t endpoint_index, uint16_t offset,
                                   uint8_t width, uint32_t value)
{
    hpe_pcie_endpoint_t *endpoint = endpoint_at(sim, endpoint_index);
    if (!sim || !endpoint) {
        return HPE_ERR_ARGUMENT;
    }
    return config_access(sim, endpoint, offset, width, &value, 1);
}

/** @brief Implements the mmio_access operation. */
static hpe_status_t mmio_access(hpe_sim_t *sim, hpe_pcie_endpoint_t *endpoint, uint32_t offset,
                                uint8_t width, uint32_t *value, int write)
{
    uint16_t command = (uint16_t)get_le(endpoint->config + 0x04, 2);
    uint8_t tag, request_type;
    if (!valid_width(offset, width, endpoint->bar0_size)) {
        return pcie_reject(sim,
                           "Memory Read/Write completion abort: BAR0 range or width is invalid",
                           HPE_ERR_RANGE);
    }
    if (!endpoint->link_up || !(command & 0x0002u)) {
        return pcie_reject(sim,
                           "Memory Read/Write completion abort: link down or Memory Space disabled",
                           HPE_ERR_IO);
    }
    sim->active_protocol = HPE_PROTOCOL_PCIE;
    if (write) {
        put_le(endpoint->bar0 + offset, *value, width);
    }
    else {
        *value = get_le(endpoint->bar0 + offset, width);
    }
    tag = sim->pcie_next_tag++;
    request_type = write ? 0x40 : 0x00; /* MemWr32 / MemRd32. */
    sim->time_ns += 500;
    emit_tlp(sim, endpoint, HPE_EVENT_COMMAND, request_type, tag, width, offset,
             (uint8_t)(offset & 3u), *value, 0,
             write ? "Memory Write request (posted)" : "Memory Read request");
    sim->time_ns += 500;
    if (write) {
        /* Posted writes complete at the Data Link Layer ACK; no Completion TLP follows. */
    }
    else {
        emit_tlp(sim, endpoint, HPE_EVENT_RESPONSE, 0x4A, tag, width, offset,
                 (uint8_t)(offset & 3u), *value, 0, "CplD completion");
    }
    return HPE_OK;
}

/** @brief Implements the hpe_pcie_mmio_read operation. */
hpe_status_t hpe_pcie_mmio_read(hpe_sim_t *sim, size_t endpoint_index, uint32_t offset,
                                uint8_t width, uint32_t *value)
{
    hpe_pcie_endpoint_t *endpoint = endpoint_at(sim, endpoint_index);
    if (!sim || !endpoint || !value) {
        return HPE_ERR_ARGUMENT;
    }
    return mmio_access(sim, endpoint, offset, width, value, 0);
}

/** @brief Implements the hpe_pcie_mmio_write operation. */
hpe_status_t hpe_pcie_mmio_write(hpe_sim_t *sim, size_t endpoint_index, uint32_t offset,
                                 uint8_t width, uint32_t value)
{
    hpe_pcie_endpoint_t *endpoint = endpoint_at(sim, endpoint_index);
    if (!sim || !endpoint) {
        return HPE_ERR_ARGUMENT;
    }
    return mmio_access(sim, endpoint, offset, width, &value, 1);
}

/** @brief Implements the hpe_pcie_corrupt_next_tlp operation. */
hpe_status_t hpe_pcie_corrupt_next_tlp(hpe_sim_t *sim)
{
    if (!sim) {
        return HPE_ERR_ARGUMENT;
    }
    sim->pcie_corrupt_next_tlp = 1;
    return HPE_OK;
}
