#include "internal.h"

#include <stdlib.h>
#include <string.h>

hpe_status_t hpe_i2c_add_register_device(hpe_sim_t *sim, const char *name,
        uint8_t address, size_t register_count, uint8_t fill) {
    hpe_device_t *dev;
    size_t i;
    if (!sim || !name || !name[0] || address > HPE_I2C_MAX_ADDRESS ||
            register_count == 0 || register_count > 65536) return HPE_ERR_ARGUMENT;
    if (sim->i2c.count == sim->i2c.capacity) {
        size_t cap = sim->i2c.capacity ? sim->i2c.capacity * 2 : 8;
        hpe_device_t **next = (hpe_device_t **)realloc(sim->i2c.devices, cap * sizeof(*next));
        if (!next) return HPE_ERR_NOMEM;
        sim->i2c.devices = next;
        sim->i2c.capacity = cap;
    }
    dev = (hpe_device_t *)calloc(1, sizeof(*dev));
    if (!dev) return HPE_ERR_NOMEM;
    dev->registers = (uint8_t *)malloc(register_count);
    if (!dev->registers) { free(dev); return HPE_ERR_NOMEM; }
    memset(dev->registers, fill, register_count);
    strncpy(dev->name, name, sizeof(dev->name) - 1);
    dev->address = address;
    dev->register_count = register_count;
    for (i = 0; i < sim->i2c.count; ++i) {
        if (sim->i2c.devices[i]->address == address) {
            hpe_emit(sim, HPE_EVENT_ERROR, address, 0, "I2C address conflict: multiple devices respond");
            break;
        }
    }
    sim->i2c.devices[sim->i2c.count++] = dev;
    return HPE_OK;
}

hpe_status_t hpe_i2c_set_register(hpe_sim_t *sim, uint8_t address,
        uint16_t reg, uint8_t value) {
    hpe_device_t *dev = hpe_find_device(sim, address);
    if (!sim || address > HPE_I2C_MAX_ADDRESS) return HPE_ERR_ARGUMENT;
    if (!dev) return HPE_ERR_NOT_FOUND;
    if ((size_t)reg >= dev->register_count) return HPE_ERR_RANGE;
    dev->registers[reg] = value;
    return HPE_OK;
}

hpe_status_t hpe_i2c_get_register(hpe_sim_t *sim, uint8_t address,
        uint16_t reg, uint8_t *value) {
    hpe_device_t *dev = hpe_find_device(sim, address);
    if (!sim || !value || address > HPE_I2C_MAX_ADDRESS) return HPE_ERR_ARGUMENT;
    if (!dev) return HPE_ERR_NOT_FOUND;
    if ((size_t)reg >= dev->register_count) return HPE_ERR_RANGE;
    *value = dev->registers[reg];
    return HPE_OK;
}

hpe_status_t hpe_i2c_set_device_callbacks(hpe_sim_t *sim, uint8_t address,
        const hpe_i2c_device_callbacks_t *callbacks, void *user_data) {
    hpe_device_t *dev;
    if (!sim || address > HPE_I2C_MAX_ADDRESS) return HPE_ERR_ARGUMENT;
    dev = hpe_find_device(sim, address);
    if (!dev) return HPE_ERR_NOT_FOUND;
    if (callbacks) dev->callbacks = *callbacks;
    else memset(&dev->callbacks, 0, sizeof(dev->callbacks));
    dev->user_data = user_data;
    return HPE_OK;
}
