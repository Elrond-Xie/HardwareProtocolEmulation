#ifndef HPE_INTERNAL_H
#define HPE_INTERNAL_H

#include "hpe/hpe.h"

typedef struct hpe_device hpe_device_t;
typedef struct hpe_i2c_bus hpe_i2c_bus_t;
typedef struct hpe_spi_device hpe_spi_device_t;
typedef struct hpe_block_card hpe_block_card_t;
typedef struct hpe_card_page hpe_card_page_t;
typedef struct hpe_usb_device hpe_usb_device_t;
typedef struct hpe_pcie_endpoint hpe_pcie_endpoint_t;

struct hpe_device {
    char name[48];
    uint8_t address;
    uint8_t *registers;
    size_t register_count;
    uint16_t pointer;
    unsigned injected_nacks;
    int selected;
    int expect_register;
    hpe_i2c_device_callbacks_t callbacks;
    void *user_data;
};

struct hpe_i2c_bus {
    hpe_device_t **devices;
    size_t count;
    size_t capacity;
    uint64_t time_ns;
    uint64_t byte_start_ns;
    uint32_t half_period_ns;
    uint8_t scl;
    uint8_t sda;
    int active;
    int selected;
    int reading;
    int master_ack_last;
    hpe_device_t *target;
};

struct hpe_spi_device {
    char name[48];
    uint8_t chip_select;
    uint8_t *memory;
    size_t memory_size;
    uint8_t status;
    uint8_t write_enabled;
    uint8_t jedec_id[3];
};

typedef enum hpe_card_state {
    HPE_CARD_IDLE = 0,
    HPE_CARD_READY,
    HPE_CARD_IDENTIFICATION,
    HPE_CARD_STANDBY,
    HPE_CARD_TRANSFER
} hpe_card_state_t;

struct hpe_block_card {
    char name[48];
    hpe_protocol_t protocol;
    hpe_card_state_t state;
    uint32_t block_count;
    uint16_t rca;
    hpe_card_page_t *pages;
    uint8_t fill;
    uint8_t application_command_pending;
    uint8_t initialization_attempts;
    uint8_t selected;
    uint8_t high_capacity;
    uint8_t negotiated_high_capacity;
    uint8_t bus_width;
    uint8_t high_speed;
    uint32_t requested_ocr;
    uint8_t pending_data_command;
    uint32_t pending_data_block;
    uint32_t predefined_block_count;
    uint32_t blocks_remaining;
    uint8_t cid[16];
    uint8_t csd[16];
    uint8_t ext_csd[512];
};

struct hpe_card_page {
    uint32_t block;
    uint8_t data[512];
    hpe_card_page_t *next;
};

typedef enum hpe_usb_state {
    HPE_USB_DEFAULT = 0,
    HPE_USB_ADDRESSED,
    HPE_USB_CONFIGURED
} hpe_usb_state_t;

struct hpe_usb_device {
    char name[48];
    char manufacturer[48];
    char product[48];
    uint16_t vendor_id;
    uint16_t product_id;
    uint16_t release;
    uint8_t address;
    uint8_t configuration;
    uint8_t interface_alt_setting;
    uint8_t state;
    uint8_t remote_wakeup_enabled;
    uint8_t bulk_out_toggle;
    uint8_t bulk_in_toggle;
    uint8_t interrupt_in_toggle;
    uint8_t bulk_out_halted;
    uint8_t bulk_in_halted;
    uint8_t interrupt_in_halted;
    uint8_t interrupt_pending;
    uint8_t interrupt_length;
    uint8_t interrupt_data[8];
    uint64_t interrupt_next_poll_time_ns;
    uint8_t *bulk_data;
    size_t bulk_length;
    size_t bulk_capacity;
};

struct hpe_pcie_endpoint {
    char name[48];
    uint8_t bus, device, function;
    uint8_t max_generation, max_lanes, negotiated_generation, negotiated_lanes;
    uint16_t vendor_id, device_id;
    uint32_t bar0_size;
    uint8_t link_up;
    uint8_t bar0_probe;
    uint8_t config[256];
    uint8_t *bar0;
};

struct hpe_sim {
    hpe_i2c_bus_t i2c;
    hpe_spi_device_t **spi_devices;
    size_t spi_count;
    size_t spi_capacity;
    uint64_t time_ns;
    uint64_t spi_byte_start_ns;
    hpe_protocol_t active_protocol;
    uint8_t spi_clock;
    uint8_t spi_mosi;
    uint8_t spi_miso;
    uint8_t spi_cs;
    uint8_t usb_dplus;
    uint8_t usb_dminus;
    uint8_t usb_bit_time_remainder;
    uint64_t usb_next_sof_time_ns;
    uint16_t usb_frame_number;
    hpe_block_card_t **block_cards;
    size_t block_card_count;
    size_t block_card_capacity;
    hpe_usb_device_t **usb_devices;
    size_t usb_device_count;
    size_t usb_device_capacity;
    hpe_pcie_endpoint_t **pcie_endpoints;
    size_t pcie_endpoint_count;
    size_t pcie_endpoint_capacity;
    uint8_t pcie_next_tag;
    uint16_t pcie_host_sequence;
    uint16_t pcie_endpoint_sequence;
    uint8_t pcie_corrupt_next_tlp;
    hpe_event_t *events;
    size_t event_count;
    size_t event_capacity;
};

void hpe_emit(hpe_sim_t *sim, hpe_event_kind_t kind, uint8_t value,
              uint8_t read, const char *message);
hpe_device_t *hpe_find_device(hpe_sim_t *sim, uint8_t address);
hpe_spi_device_t *hpe_find_spi_device(hpe_sim_t *sim, uint8_t chip_select);
hpe_status_t hpe_block_card_read_memory(const hpe_sim_t *sim, size_t card_index,
        uint32_t offset, uint8_t *data, size_t length);

#endif
