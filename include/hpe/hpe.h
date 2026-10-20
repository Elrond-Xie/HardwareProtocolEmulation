/*
 * File: hpe.h
 * Purpose: Project-owned implementation and interface for include / hpe / hpe.h.
 * Author: Elrond-Xie
 * Copyright (c) 2026 Elrond-Xie. SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0.
 */

#ifndef HPE_H
#define HPE_H

#include <stddef.h>
#include <stdint.h>

#ifdef _WIN32
#if defined(HPE_BUILD_SHARED)
#define HPE_API __declspec(dllexport)
#elif defined(HPE_USE_SHARED)
#define HPE_API __declspec(dllimport)
#else
#define HPE_API
#endif
#else
#define HPE_API
#endif

#ifdef __cplusplus
extern "C" {
#endif

#define HPE_VERSION "0.1.0"
#define HPE_I2C_MAX_ADDRESS 0x7fu

typedef struct hpe_sim hpe_sim_t;

typedef enum hpe_status
{
    HPE_OK = 0,
    HPE_ERR_ARGUMENT = -1,
    HPE_ERR_NOMEM = -2,
    HPE_ERR_NOT_FOUND = -3,
    HPE_ERR_NACK = -4,
    HPE_ERR_RANGE = -5,
    HPE_ERR_IO = -6,
    HPE_ERR_PARSE = -7
} hpe_status_t;

typedef enum hpe_event_kind
{
    HPE_EVENT_LINE = 0,
    HPE_EVENT_START,
    HPE_EVENT_RESTART,
    HPE_EVENT_STOP,
    HPE_EVENT_ADDRESS,
    HPE_EVENT_DATA,
    HPE_EVENT_ACK,
    HPE_EVENT_NACK,
    HPE_EVENT_INFO,
    HPE_EVENT_ERROR,
    HPE_EVENT_COMMAND,
    HPE_EVENT_RESPONSE,
    HPE_EVENT_BLOCK
} hpe_event_kind_t;

typedef enum hpe_protocol
{
    HPE_PROTOCOL_NONE = 0,
    HPE_PROTOCOL_I2C,
    HPE_PROTOCOL_SPI,
    HPE_PROTOCOL_SD,
    HPE_PROTOCOL_EMMC,
    HPE_PROTOCOL_USB,
    HPE_PROTOCOL_PCIE
} hpe_protocol_t;

typedef struct hpe_event
{
    uint64_t time_ns;
    hpe_event_kind_t kind;
    uint8_t scl;
    uint8_t sda;
    uint8_t value;
    uint8_t read;
    char message[96];
    uint64_t data_start_ns; /* start of the 8-bit field for ADDRESS/DATA events */
    hpe_protocol_t protocol;
    uint8_t clock;
    uint8_t mosi;
    uint8_t miso;
    uint8_t chip_select;
    uint8_t receive_value;
    uint8_t protocol_command;
    uint32_t protocol_argument;
    uint32_t protocol_response;
    uint16_t data_crc16; /* SD/eMMC block event: CRC16 transmitted on DAT0. */
    uint8_t usb_pid;
    uint8_t usb_address;
    uint8_t usb_endpoint;
    uint16_t payload_length;
    uint8_t pcie_tlp_fmt_type;
    uint8_t pcie_tlp_tag;
    uint8_t pcie_tlp_length_dw;
    uint8_t pcie_tlp_byte_enable;
    uint16_t pcie_requester_id;
    uint16_t pcie_completer_id;
    uint8_t data_lines[8];       /* SD/eMMC DAT[7:0], sampled at this bus clock. */
    uint8_t data_line_count;     /* Active DAT width for SD/eMMC line events. */
    uint8_t pcie_tlp_header[16]; /* Serialized PCIe TLP header, DW0 first, little-endian bytes. */
    uint8_t pcie_tlp_header_length; /* 12 or 16 bytes. */
    uint8_t pcie_tlp_payload[4];    /* Current PCIe model supports at most one payload DW. */
    uint8_t pcie_tlp_payload_length;
    uint16_t pcie_tlp_sequence; /* Simplified per-direction Data Link sequence number. */
    uint32_t pcie_lcrc;
    uint8_t pcie_dllp_type;
    uint16_t pcie_dllp_ack_sequence;
    uint8_t pcie_dllp[6]; /* Classic DLLP bytes: type, three information bytes, CRC16. */
    uint8_t pcie_dllp_length;
    uint16_t pcie_dllp_crc16;
    uint8_t pcie_tlp_corrupted; /* Fault-injected packet has an invalid LCRC. */
    uint8_t pcie_tlp_replay;
} hpe_event_t;

typedef struct hpe_device_info
{
    char name[48];
    uint8_t address;
    uint8_t present;
} hpe_device_info_t;

typedef struct hpe_virtual_device_info
{
    hpe_protocol_t protocol;
    char name[48];
    uint32_t locator;
    uint32_t identity;
    uint32_t memory_size;
    uint8_t state;
    uint8_t flags;
} hpe_virtual_device_info_t;

typedef struct hpe_pcie_bdf
{
    uint8_t bus;
    uint8_t device;
    uint8_t function;
    uint16_t vendor_id;
    uint16_t device_id;
} hpe_pcie_bdf_t;

typedef enum hpe_card_register
{
    HPE_CARD_REGISTER_CID = 0,
    HPE_CARD_REGISTER_CSD,
    HPE_CARD_REGISTER_EXT_CSD
} hpe_card_register_t;

typedef struct hpe_i2c_device_state
{
    char name[48];
    uint8_t address;
    uint8_t present;
    uint16_t register_pointer;
    size_t register_count;
} hpe_i2c_device_state_t;

typedef uint8_t (*hpe_i2c_read_callback_t)(void *user_data, uint16_t reg, uint8_t stored_value);
typedef void (*hpe_i2c_write_callback_t)(void *user_data, uint16_t reg, uint8_t value);

typedef struct hpe_i2c_device_callbacks
{
    hpe_i2c_read_callback_t on_read;
    hpe_i2c_write_callback_t on_write;
} hpe_i2c_device_callbacks_t;

/* Simulation and bus setup. All times are virtual; calls are deterministic. */
/** @brief Creates a new deterministic protocol simulation. */
HPE_API hpe_sim_t *hpe_sim_create(void);
/** @brief Releases all simulator state and allocated devices. */
HPE_API void hpe_sim_destroy(hpe_sim_t *sim);
/** @brief Clears recorded events without removing devices. */
HPE_API void hpe_sim_reset_trace(hpe_sim_t *sim);
/** @brief Returns the number of recorded bus events. */
HPE_API size_t hpe_sim_event_count(const hpe_sim_t *sim);
/** @brief Returns the recorded event at the requested index, or NULL. */
HPE_API const hpe_event_t *hpe_sim_event_at(const hpe_sim_t *sim, size_t index);
/** @brief Returns the number of I2C devices on the bus. */
HPE_API size_t hpe_sim_device_count(const hpe_sim_t *sim);
/** @brief Copies information about an I2C device at the requested index. */
HPE_API int hpe_sim_device_at(const hpe_sim_t *sim, size_t index, hpe_device_info_t *out);
/* Unified software-device inventory and read-only byte view used by inspectors. */
/** @brief Returns the number of software-modeled devices across protocols. */
HPE_API size_t hpe_sim_virtual_device_count(const hpe_sim_t *sim);
/** @brief Copies protocol and identity information for a virtual device. */
HPE_API int hpe_sim_virtual_device_at(const hpe_sim_t *sim, size_t index,
                                      hpe_virtual_device_info_t *out);
/** @brief Reads the raw memory exposed by a virtual device. */
HPE_API hpe_status_t hpe_sim_virtual_device_read_memory(const hpe_sim_t *sim, size_t index,
                                                        uint32_t offset, uint8_t *data,
                                                        size_t length);
/* State and raw register-backed memory for software-created I2C devices. */
/** @brief Returns the current register pointer and size for an I2C device. */
HPE_API int hpe_i2c_device_state_at(const hpe_sim_t *sim, size_t index,
                                    hpe_i2c_device_state_t *out);
/** @brief Reads raw register-backed bytes from the I2C device at an address. */
HPE_API hpe_status_t hpe_i2c_read_memory(const hpe_sim_t *sim, uint8_t address, uint32_t offset,
                                         uint8_t *data, size_t length);
/** @brief Reads raw register-backed bytes from an I2C device by index. */
HPE_API hpe_status_t hpe_i2c_read_memory_at(const hpe_sim_t *sim, size_t device_index,
                                            uint32_t offset, uint8_t *data, size_t length);
/** @brief Returns a human-readable description of a simulator status code. */
HPE_API const char *hpe_status_string(hpe_status_t status);
/** @brief Returns the name of a protocol identifier. */
HPE_API const char *hpe_protocol_name(hpe_protocol_t protocol);
/** @brief Provides the i2c add register device operation. */

/* Generic 8-bit register-mapped I2C device. register_count must be 1..65536. */
HPE_API hpe_status_t hpe_i2c_add_register_device(hpe_sim_t *sim, const char *name, uint8_t address,
                                                 size_t register_count, uint8_t fill);
/** @brief Writes a value directly into an I2C device register. */
HPE_API hpe_status_t hpe_i2c_set_register(hpe_sim_t *sim, uint8_t address, uint16_t reg,
                                          uint8_t value);
/** @brief Reads a value directly from an I2C device register. */
HPE_API hpe_status_t hpe_i2c_get_register(hpe_sim_t *sim, uint8_t address, uint16_t reg,
                                          uint8_t *value);
/* Optional hooks add device-specific behavior while retaining register storage. */
/** @brief Sets optional read and write callbacks for an I2C device. */
HPE_API hpe_status_t hpe_i2c_set_device_callbacks(hpe_sim_t *sim, uint8_t address,
                                                  const hpe_i2c_device_callbacks_t *callbacks,
                                                  void *user_data);
/** @brief Provides the i2c probe operation. */

/* Master-side transactions. A write's first payload byte selects the register. */
HPE_API hpe_status_t hpe_i2c_probe(hpe_sim_t *sim, uint8_t address);
/** @brief Writes a byte sequence to an I2C device. */
HPE_API hpe_status_t hpe_i2c_write(hpe_sim_t *sim, uint8_t address, const uint8_t *data,
                                   size_t length);
/** @brief Reads bytes from an I2C device and controls the final master ACK. */
HPE_API hpe_status_t hpe_i2c_read(hpe_sim_t *sim, uint8_t address, uint8_t *data, size_t length,
                                  int master_ack_last);
/** @brief Selects an 8-bit register and reads its contents over I2C. */
HPE_API hpe_status_t hpe_i2c_read_register(hpe_sim_t *sim, uint8_t address, uint8_t reg,
                                           uint8_t *data, size_t length);
/** @brief Scans valid 7-bit I2C addresses and stores discovered addresses. */
HPE_API hpe_status_t hpe_i2c_scan(hpe_sim_t *sim, uint8_t *addresses, size_t capacity,
                                  size_t *found);
/** @brief Configures an I2C device to NACK its next address attempts. */
HPE_API void hpe_i2c_inject_nack(hpe_sim_t *sim, uint8_t address, unsigned count);
/** @brief Provides the spi add flash device operation. */

/* SPI NOR flash model. Transfer length is one complete active-low CS frame. */
HPE_API hpe_status_t hpe_spi_add_flash_device(hpe_sim_t *sim, const char *name, uint8_t chip_select,
                                              size_t memory_size, uint8_t fill);
/** @brief Transfers bytes during one SPI chip-select frame. */
HPE_API hpe_status_t hpe_spi_transfer(hpe_sim_t *sim, uint8_t chip_select, uint8_t mode,
                                      const uint8_t *transmit, uint8_t *receive, size_t length);
/** @brief Returns the number of modeled SPI devices. */
HPE_API size_t hpe_spi_device_count(const hpe_sim_t *sim);
/** @brief Reads raw bytes from the SPI flash memory array. */
HPE_API hpe_status_t hpe_spi_read_memory(const hpe_sim_t *sim, uint8_t chip_select, uint32_t offset,
                                         uint8_t *data, size_t length);
/** @brief Provides the block card add operation. */

/* SD memory and eMMC cards use a command state machine and 512-byte blocks. */
HPE_API hpe_status_t hpe_block_card_add(hpe_sim_t *sim, hpe_protocol_t protocol, const char *name,
                                        uint32_t block_count, uint8_t fill);
/** @brief Returns the number of modeled SD and eMMC cards. */
HPE_API size_t hpe_block_card_count(const hpe_sim_t *sim);
/** @brief Sends a command to a card and returns its response. */
HPE_API hpe_status_t hpe_block_card_command(hpe_sim_t *sim, size_t card_index, uint8_t command,
                                            uint32_t argument, uint32_t *response);
/* Read the raw 16-byte CID/CSD or 512-byte eMMC EXT_CSD register image. */
/** @brief Reads the raw CID, CSD, or EXT_CSD register image. */
HPE_API hpe_status_t hpe_block_card_get_register(const hpe_sim_t *sim, size_t card_index,
                                                 hpe_card_register_t reg, uint8_t *data,
                                                 size_t capacity, size_t *length);
/** @brief Reads one 512-byte card block. */
HPE_API hpe_status_t hpe_block_card_read(hpe_sim_t *sim, size_t card_index, uint32_t block,
                                         uint8_t data[512]);
/** @brief Writes one 512-byte card block. */
HPE_API hpe_status_t hpe_block_card_write(hpe_sim_t *sim, size_t card_index, uint32_t block,
                                          const uint8_t data[512]);
/** @brief Provides the usb device add operation. */

/* USB 2.0 control endpoint model with standard descriptors and enumeration. */
HPE_API hpe_status_t hpe_usb_device_add(hpe_sim_t *sim, const char *name, uint16_t vendor_id,
                                        uint16_t product_id, const char *manufacturer,
                                        const char *product);
/** @brief Returns the number of modeled USB devices. */
HPE_API size_t hpe_usb_device_count(const hpe_sim_t *sim);
/** @brief Performs a USB control transfer on endpoint zero. */
HPE_API hpe_status_t hpe_usb_control_transfer(hpe_sim_t *sim, size_t device_index, uint8_t address,
                                              uint8_t request_type, uint8_t request, uint16_t value,
                                              uint16_t index, uint8_t *data, uint16_t length,
                                              uint16_t *actual_length);
/* Virtual bulk endpoint 1 echoes OUT bytes on the next IN transaction. */
/** @brief Performs a USB bulk transfer on the selected endpoint. */
HPE_API hpe_status_t hpe_usb_bulk_transfer(hpe_sim_t *sim, size_t device_index, uint8_t address,
                                           uint8_t endpoint_address, uint8_t *data, uint16_t length,
                                           uint16_t *actual_length);
/* Queue one interrupt-IN report (up to 8 bytes) and poll endpoint 2 at address 0x82. */
/** @brief Queues an interrupt-IN report for a USB device. */
HPE_API hpe_status_t hpe_usb_interrupt_queue(hpe_sim_t *sim, size_t device_index,
                                             const uint8_t *data, uint8_t length);
/** @brief Polls a USB interrupt-IN endpoint for a report. */
HPE_API hpe_status_t hpe_usb_interrupt_poll(hpe_sim_t *sim, size_t device_index, uint8_t address,
                                            uint8_t *data, uint8_t capacity,
                                            uint8_t *actual_length);
/** @brief Enumerates a USB device using standard descriptor and configuration requests. */
HPE_API hpe_status_t hpe_usb_enumerate(hpe_sim_t *sim, size_t device_index);
/** @brief Provides the pcie add endpoint operation. */

/* PCIe endpoint model with link training, Type 0 config space and one BAR0. */
HPE_API hpe_status_t hpe_pcie_add_endpoint(hpe_sim_t *sim, const char *name, uint8_t bus,
                                           uint8_t device, uint8_t function, uint16_t vendor_id,
                                           uint16_t device_id, uint8_t class_code, uint8_t subclass,
                                           uint8_t programming_interface, uint32_t bar0_size,
                                           uint8_t max_generation, uint8_t max_lanes);
/** @brief Returns the number of modeled PCIe endpoints. */
HPE_API size_t hpe_pcie_endpoint_count(const hpe_sim_t *sim);
/** @brief Trains the PCIe links and discovers endpoint BDF identities. */
HPE_API hpe_status_t hpe_pcie_enumerate(hpe_sim_t *sim, hpe_pcie_bdf_t *devices, size_t capacity,
                                        size_t *found);
/** @brief Reads a width-sized value from PCIe configuration space. */
HPE_API hpe_status_t hpe_pcie_config_read(hpe_sim_t *sim, size_t endpoint_index, uint16_t offset,
                                          uint8_t width, uint32_t *value);
/** @brief Writes a width-sized value to PCIe configuration space. */
HPE_API hpe_status_t hpe_pcie_config_write(hpe_sim_t *sim, size_t endpoint_index, uint16_t offset,
                                           uint8_t width, uint32_t value);
/** @brief Reads a width-sized value from the endpoint BAR0 region. */
HPE_API hpe_status_t hpe_pcie_mmio_read(hpe_sim_t *sim, size_t endpoint_index, uint32_t offset,
                                        uint8_t width, uint32_t *value);
/** @brief Writes a width-sized value to the endpoint BAR0 region. */
HPE_API hpe_status_t hpe_pcie_mmio_write(hpe_sim_t *sim, size_t endpoint_index, uint32_t offset,
                                         uint8_t width, uint32_t value);
/** @brief Injects an invalid LCRC into the next PCIe TLP. */
HPE_API hpe_status_t hpe_pcie_corrupt_next_tlp(hpe_sim_t *sim);
/** @brief Provides the scenario run file operation. */

/* Scenario runner supports device declarations and master-side commands. */
HPE_API hpe_status_t hpe_scenario_run_file(hpe_sim_t *sim, const char *path, char *error,
                                           size_t error_capacity);
/** @brief Parses and executes a protocol scenario from a text buffer. */
HPE_API hpe_status_t hpe_scenario_run_text(hpe_sim_t *sim, const char *text, char *error,
                                           size_t error_capacity);
/** @brief Provides the trace write text operation. */

/* Human-readable trace export. */
HPE_API hpe_status_t hpe_trace_write_text(const hpe_sim_t *sim, const char *path);
/** @brief Returns the name of an event kind identifier. */
HPE_API const char *hpe_event_kind_name(hpe_event_kind_t kind);

#ifdef __cplusplus
}
#endif
#endif
