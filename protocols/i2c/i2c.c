/*
 * File: i2c.c
 * Purpose: Project-owned implementation and interface for protocols / i2c / i2c.c.
 * Author: Elrond-Xie
 * Copyright (c) 2026 Elrond-Xie. SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0.
 */

#include "internal.h"

#include <stdio.h>

/** @brief Implements the wait_half operation. */
static void wait_half(hpe_i2c_bus_t *bus)
{
    bus->time_ns += bus->half_period_ns;
}

/** @brief Implements the set_lines operation. */
static void set_lines(hpe_sim_t *sim, int scl, int sda)
{
    hpe_i2c_bus_t *bus = &sim->i2c;
    if (bus->scl == (uint8_t)scl && bus->sda == (uint8_t)sda) {
        return;
    }
    bus->scl = (uint8_t)scl;
    bus->sda = (uint8_t)sda;
    hpe_emit(sim, HPE_EVENT_LINE, 0, 0, "SCL/SDA transition");
}

/** @brief Implements the start_condition operation. */
static void start_condition(hpe_sim_t *sim)
{
    hpe_i2c_bus_t *bus = &sim->i2c;
    int repeated = bus->active;
    if (!bus->active) {
        set_lines(sim, 1, 1);
    }
    wait_half(bus);
    set_lines(sim, 1, 0);
    wait_half(bus);
    set_lines(sim, 0, 0);
    bus->active = 1;
    bus->selected = 0;
    bus->target = NULL;
    {
        size_t i;
        for (i = 0; i < bus->count; ++i) {
            bus->devices[i]->selected = 0;
        }
    }
    hpe_emit(sim, repeated ? HPE_EVENT_RESTART : HPE_EVENT_START, 0, 0,
             repeated ? "repeated START" : "START condition");
}

/** @brief Implements the stop_condition operation. */
static void stop_condition(hpe_sim_t *sim)
{
    hpe_i2c_bus_t *bus = &sim->i2c;
    if (!bus->active) {
        return;
    }
    set_lines(sim, 0, 0);
    wait_half(bus);
    set_lines(sim, 1, 0);
    wait_half(bus);
    set_lines(sim, 1, 1);
    bus->active = 0;
    bus->selected = 0;
    bus->target = NULL;
    hpe_emit(sim, HPE_EVENT_STOP, 0, 0, "STOP condition");
}

/** @brief Implements the clock_out_bit operation. */
static void clock_out_bit(hpe_sim_t *sim, int bit)
{
    hpe_i2c_bus_t *bus = &sim->i2c;
    set_lines(sim, 0, bit);
    wait_half(bus);
    set_lines(sim, 1, bit);
    wait_half(bus);
    set_lines(sim, 0, bit);
}

/** @brief Implements the clock_in_ack operation. */
static void clock_in_ack(hpe_sim_t *sim, int ack)
{
    hpe_i2c_bus_t *bus = &sim->i2c;
    set_lines(sim, 0, ack ? 0 : 1);
    wait_half(bus);
    set_lines(sim, 1, ack ? 0 : 1);
    wait_half(bus);
    set_lines(sim, 0, 1);
    hpe_emit(sim, ack ? HPE_EVENT_ACK : HPE_EVENT_NACK, 0, 0,
             ack ? "receiver pulled SDA low" : "receiver left SDA high");
}

/** @brief Implements the send_byte_bits operation. */
static void send_byte_bits(hpe_sim_t *sim, uint8_t byte)
{
    int bit;
    sim->i2c.byte_start_ns = sim->i2c.time_ns;
    for (bit = 7; bit >= 0; --bit) {
        clock_out_bit(sim, (byte >> bit) & 1);
    }
}

/** @brief Implements the address_ack operation. */
static int address_ack(hpe_sim_t *sim, uint8_t address)
{
    hpe_i2c_bus_t *bus = &sim->i2c;
    size_t i;
    unsigned responders = 0;
    hpe_device_t *target = NULL;
    for (i = 0; i < bus->count; ++i) {
        hpe_device_t *dev = bus->devices[i];
        dev->selected = 0;
        if (dev->address == address) {
            if (dev->injected_nacks) {
                --dev->injected_nacks;
                continue;
            }
            ++responders;
            dev->selected = 1;
            if (!target) {
                target = dev;
            }
        }
    }
    bus->target = target;
    bus->selected = responders != 0;
    if (responders > 1) {
        hpe_emit(sim, HPE_EVENT_ERROR, address, 0, "address conflict: multiple responders ACK");
    }
    return responders != 0;
}

/** @brief Implements the send_wire_byte operation. */
static int send_wire_byte(hpe_sim_t *sim, uint8_t byte, int ack, hpe_event_kind_t kind, int read,
                          const char *label)
{
    send_byte_bits(sim, byte);
    hpe_emit(sim, kind, byte, (uint8_t)read, label);
    clock_in_ack(sim, ack);
    return ack;
}

/** @brief Implements the address_phase operation. */
static hpe_status_t address_phase(hpe_sim_t *sim, uint8_t address, int read)
{
    uint8_t wire = (uint8_t)((address << 1) | (read ? 1 : 0));
    int ack;
    char msg[96];
    if (address > HPE_I2C_MAX_ADDRESS) {
        return HPE_ERR_ARGUMENT;
    }
    snprintf(msg, sizeof(msg), "7-bit address 0x%02X %s", address, read ? "read" : "write");
    ack = address_ack(sim, address);
    if (!send_wire_byte(sim, wire, ack, HPE_EVENT_ADDRESS, (uint8_t)read, msg)) {
        hpe_emit(sim, HPE_EVENT_INFO, address, 0, "address was not acknowledged");
        return HPE_ERR_NACK;
    }
    sim->i2c.reading = read;
    if (!read) {
        size_t i;
        for (i = 0; i < sim->i2c.count; ++i) {
            if (sim->i2c.devices[i]->selected) {
                sim->i2c.devices[i]->expect_register = 1;
            }
        }
    }
    return HPE_OK;
}

/** @brief Implements the deliver_write operation. */
static void deliver_write(hpe_device_t *dev, uint8_t byte)
{
    if (!dev) {
        return;
    }
    if (dev->expect_register) {
        dev->pointer = (uint16_t)(byte % dev->register_count);
        dev->expect_register = 0;
    }
    else {
        dev->registers[dev->pointer] = byte;
        if (dev->callbacks.on_write) {
            dev->callbacks.on_write(dev->user_data, dev->pointer, byte);
        }
        dev->pointer = (uint16_t)((dev->pointer + 1) % dev->register_count);
    }
}

/** @brief Implements the send_data_byte operation. */
static hpe_status_t send_data_byte(hpe_sim_t *sim, uint8_t byte)
{
    hpe_i2c_bus_t *bus = &sim->i2c;
    size_t i;
    int ack = 0;
    char msg[96];
    for (i = 0; i < bus->count; ++i) {
        hpe_device_t *dev = bus->devices[i];
        if (dev->selected) {
            deliver_write(dev, byte);
            ack = 1;
        }
    }
    snprintf(msg, sizeof(msg), "master wrote 0x%02X", byte);
    return send_wire_byte(sim, byte, ack, HPE_EVENT_DATA, 0, msg) ? HPE_OK : HPE_ERR_NACK;
}

/** @brief Implements the receive_data_byte operation. */
static uint8_t receive_data_byte(hpe_sim_t *sim)
{
    hpe_i2c_bus_t *bus = &sim->i2c;
    uint8_t value = 0xff;
    size_t i;
    int bit;
    bus->byte_start_ns = bus->time_ns;
    if (bus->target) {
        value = 0xff;
        for (i = 0; i < bus->count; ++i) {
            hpe_device_t *dev = bus->devices[i];
            if (dev->selected) {
                uint8_t byte = dev->registers[dev->pointer];
                if (dev->callbacks.on_read) {
                    byte = dev->callbacks.on_read(dev->user_data, dev->pointer, byte);
                }
                value &= byte;
            }
        }
    }
    for (bit = 7; bit >= 0; --bit) {
        int b = (value >> bit) & 1;
        set_lines(sim, 0, b);
        wait_half(bus);
        set_lines(sim, 1, b);
        wait_half(bus);
        set_lines(sim, 0, b);
    }
    if (bus->target) {
        for (i = 0; i < bus->count; ++i) {
            if (bus->devices[i]->selected) {
                bus->devices[i]->pointer =
                    (uint16_t)((bus->devices[i]->pointer + 1) % bus->devices[i]->register_count);
            }
        }
    }
    hpe_emit(sim, HPE_EVENT_DATA, value, 1, "slave returned data byte");
    return value;
}

/** @brief Implements the master_ack_bit operation. */
static void master_ack_bit(hpe_sim_t *sim, int ack)
{
    hpe_i2c_bus_t *bus = &sim->i2c;
    set_lines(sim, 0, ack ? 0 : 1);
    wait_half(bus);
    set_lines(sim, 1, ack ? 0 : 1);
    wait_half(bus);
    set_lines(sim, 0, 1);
    hpe_emit(sim, ack ? HPE_EVENT_ACK : HPE_EVENT_NACK, 0, 1,
             ack ? "master ACK" : "master NACK (end of read)");
}

/** @brief Implements the hpe_i2c_probe operation. */
hpe_status_t hpe_i2c_probe(hpe_sim_t *sim, uint8_t address)
{
    hpe_status_t status;
    if (!sim || address > HPE_I2C_MAX_ADDRESS) {
        return HPE_ERR_ARGUMENT;
    }
    sim->active_protocol = HPE_PROTOCOL_I2C;
    if (sim->i2c.time_ns < sim->time_ns) {
        sim->i2c.time_ns = sim->time_ns;
    }
    start_condition(sim);
    status = address_phase(sim, address, 0);
    stop_condition(sim);
    return status;
}

/** @brief Implements the hpe_i2c_write operation. */
hpe_status_t hpe_i2c_write(hpe_sim_t *sim, uint8_t address, const uint8_t *data, size_t length)
{
    hpe_status_t status = HPE_OK;
    size_t i;
    if (!sim || address > HPE_I2C_MAX_ADDRESS || (length && !data)) {
        return HPE_ERR_ARGUMENT;
    }
    sim->active_protocol = HPE_PROTOCOL_I2C;
    if (sim->i2c.time_ns < sim->time_ns) {
        sim->i2c.time_ns = sim->time_ns;
    }
    start_condition(sim);
    status = address_phase(sim, address, 0);
    if (status != HPE_OK) {
        stop_condition(sim);
        return status;
    }
    for (i = 0; i < length; ++i) {
        status = send_data_byte(sim, data[i]);
        if (status != HPE_OK) {
            break;
        }
    }
    stop_condition(sim);
    return status;
}

/** @brief Implements the hpe_i2c_read_register operation. */
hpe_status_t hpe_i2c_read_register(hpe_sim_t *sim, uint8_t address, uint8_t reg, uint8_t *data,
                                   size_t length)
{
    hpe_status_t status;
    size_t i;
    if (!sim || address > HPE_I2C_MAX_ADDRESS || (length && !data)) {
        return HPE_ERR_ARGUMENT;
    }
    sim->active_protocol = HPE_PROTOCOL_I2C;
    if (sim->i2c.time_ns < sim->time_ns) {
        sim->i2c.time_ns = sim->time_ns;
    }
    if (!length) {
        return HPE_OK;
    }
    start_condition(sim);
    status = address_phase(sim, address, 0);
    if (status == HPE_OK) {
        status = send_data_byte(sim, reg);
    }
    if (status != HPE_OK) {
        stop_condition(sim);
        return status;
    }
    start_condition(sim);
    status = address_phase(sim, address, 1);
    if (status != HPE_OK) {
        stop_condition(sim);
        return status;
    }
    for (i = 0; i < length; ++i) {
        data[i] = receive_data_byte(sim);
        master_ack_bit(sim, i + 1 < length);
    }
    stop_condition(sim);
    return HPE_OK;
}

/** @brief Implements the hpe_i2c_read operation. */
hpe_status_t hpe_i2c_read(hpe_sim_t *sim, uint8_t address, uint8_t *data, size_t length,
                          int master_ack_last)
{
    hpe_status_t status;
    size_t i;
    if (!sim || address > HPE_I2C_MAX_ADDRESS || (length && !data)) {
        return HPE_ERR_ARGUMENT;
    }
    sim->active_protocol = HPE_PROTOCOL_I2C;
    if (sim->i2c.time_ns < sim->time_ns) {
        sim->i2c.time_ns = sim->time_ns;
    }
    if (!length) {
        return HPE_OK;
    }
    start_condition(sim);
    status = address_phase(sim, address, 1);
    if (status != HPE_OK) {
        stop_condition(sim);
        return status;
    }
    for (i = 0; i < length; ++i) {
        data[i] = receive_data_byte(sim);
        master_ack_bit(sim, (i + 1 < length) || master_ack_last);
    }
    stop_condition(sim);
    return HPE_OK;
}

/** @brief Implements the hpe_i2c_scan operation. */
hpe_status_t hpe_i2c_scan(hpe_sim_t *sim, uint8_t *addresses, size_t capacity, size_t *found)
{
    size_t n = 0;
    unsigned address;
    if (!sim || !found || (capacity && !addresses)) {
        return HPE_ERR_ARGUMENT;
    }
    sim->active_protocol = HPE_PROTOCOL_I2C;
    for (address = 0x03; address <= 0x77; ++address) {
        if (hpe_i2c_probe(sim, (uint8_t)address) == HPE_OK) {
            if (n < capacity) {
                addresses[n] = (uint8_t)address;
            }
            ++n;
        }
    }
    *found = n;
    return n > capacity ? HPE_ERR_RANGE : HPE_OK;
}

/** @brief Implements the hpe_i2c_inject_nack operation. */
void hpe_i2c_inject_nack(hpe_sim_t *sim, uint8_t address, unsigned count)
{
    size_t i;
    if (!sim || address > HPE_I2C_MAX_ADDRESS) {
        return;
    }
    for (i = 0; i < sim->i2c.count; ++i) {
        if (sim->i2c.devices[i]->address == address) {
            sim->i2c.devices[i]->injected_nacks = count;
        }
    }
}
