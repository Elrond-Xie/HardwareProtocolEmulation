#include "hpe/hpe.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static void usage(const char *program) {
    printf("Hardware Protocol Emulation %s\n", HPE_VERSION);
    printf("Usage: %s [--demo | --scenario FILE] [--trace FILE]\n", program);
    printf("       %s --help\n\n", program);
    puts("The demo creates two I2C register devices, scans the bus, writes and reads data,");
    puts("then injects a NACK. Scenario files also support SPI, SD/eMMC, USB 2.0 and PCIe models.");
}

static void format_pcie_packet(const hpe_event_t *event, char *buffer, size_t capacity) {
    size_t used = 0;
    uint8_t i;
    if (!capacity) return;
    buffer[0] = '\0';
    if (!event->pcie_tlp_header_length) return;
    used += (size_t)snprintf(buffer + used, capacity - used, " DW=");
    for (i = 0; i + 3 < event->pcie_tlp_header_length && used < capacity; i += 4) {
        uint32_t word = (uint32_t)event->pcie_tlp_header[i] |
                ((uint32_t)event->pcie_tlp_header[i + 1] << 8) |
                ((uint32_t)event->pcie_tlp_header[i + 2] << 16) |
                ((uint32_t)event->pcie_tlp_header[i + 3] << 24);
        used += (size_t)snprintf(buffer + used, capacity - used, "%s%08X",
                i ? "/" : "", word);
    }
    if (used < capacity)
        used += (size_t)snprintf(buffer + used, capacity - used,
                " SEQ=%03X LCRC=%08X DATA=", event->pcie_tlp_sequence, event->pcie_lcrc);
    for (i = 0; i < event->pcie_tlp_payload_length && used < capacity; ++i)
        used += (size_t)snprintf(buffer + used, capacity - used, "%s%02X",
                i ? " " : "", event->pcie_tlp_payload[i]);
}

static void print_events(const hpe_sim_t *sim) {
    size_t i;
    puts("time(us)  protocol event       value  receive details");
    for (i = 0; i < hpe_sim_event_count(sim); ++i) {
        const hpe_event_t *e = hpe_sim_event_at(sim, i);
        if (!e || e->kind == HPE_EVENT_LINE) continue;
        if (e->protocol == HPE_PROTOCOL_PCIE &&
                (e->kind == HPE_EVENT_COMMAND || e->kind == HPE_EVENT_RESPONSE ||
                 e->kind == HPE_EVENT_ACK)) {
            if (e->pcie_dllp_length) {
                printf("%8.3f  PCIe     DLLP %-3s   Seq=%03X CRC16=0x%04X Bytes=%02X %02X %02X %02X %02X %02X  %s\n",
                        (double)e->time_ns / 1000.0,
                        e->pcie_dllp_type == 0x10 ? "NAK" : "ACK", e->pcie_dllp_ack_sequence,
                        e->pcie_dllp_crc16, e->pcie_dllp[0], e->pcie_dllp[1],
                        e->pcie_dllp[2], e->pcie_dllp[3], e->pcie_dllp[4],
                        e->pcie_dllp[5], e->message);
                continue;
            }
            char packet[160];
            format_pcie_packet(e, packet, sizeof(packet));
            printf("%8.3f  PCIe     %-10s Fmt/Type=0x%02X Tag=%u Len=%uDW ReqBE=0x%X ReqID=0x%04X CplID=0x%04X arg=0x%08X data=0x%08X%s %s%s  %s\n",
                    (double)e->time_ns / 1000.0, hpe_event_kind_name(e->kind),
                    e->pcie_tlp_fmt_type, e->pcie_tlp_tag, e->pcie_tlp_length_dw,
                    e->pcie_tlp_byte_enable, e->pcie_requester_id, e->pcie_completer_id,
                    e->protocol_argument, e->protocol_response, packet,
                    e->pcie_tlp_corrupted ? "LCRC_ERROR " : "",
                    e->pcie_tlp_replay ? "REPLAY" : "", e->message);
            continue;
        }
        if (e->kind == HPE_EVENT_COMMAND || e->kind == HPE_EVENT_RESPONSE) {
            if (e->protocol == HPE_PROTOCOL_USB)
                printf("%8.3f  USB      %-10s addr=%u ep=%u PID=0x%02X req=%u arg=0x%08X len=%u  %s\n",
                        (double)e->time_ns / 1000.0, hpe_event_kind_name(e->kind),
                        e->usb_address, e->usb_endpoint, e->usb_pid, e->protocol_command,
                        e->protocol_argument, e->protocol_response, e->message);
            else
                printf("%8.3f  %-8s %-10s CMD%-2u arg=0x%08X response=0x%08X  %s\n",
                        (double)e->time_ns / 1000.0, hpe_protocol_name(e->protocol),
                        hpe_event_kind_name(e->kind), e->protocol_command,
                        e->protocol_argument, e->protocol_response, e->message);
        } else if (e->kind == HPE_EVENT_BLOCK) {
            printf("%8.3f  %-8s BLOCK      index=%u crc16=0x%04X  %s\n",
                    (double)e->time_ns / 1000.0, hpe_protocol_name(e->protocol),
                    e->protocol_argument, e->data_crc16, e->message);
        } else if (e->protocol == HPE_PROTOCOL_USB) {
            printf("%8.3f  USB      %-10s addr=%u ep=%u PID=0x%02X len=%u crc16=0x%04X  %s\n",
                    (double)e->time_ns / 1000.0, hpe_event_kind_name(e->kind),
                    e->usb_address, e->usb_endpoint, e->usb_pid, e->payload_length,
                    e->data_crc16, e->message);
        } else {
            printf("%8.3f  %-8s %-10s 0x%02X   0x%02X    %s\n", (double)e->time_ns / 1000.0,
                    hpe_protocol_name(e->protocol), hpe_event_kind_name(e->kind),
                    e->value, e->receive_value, e->message);
        }
    }
}

int main(int argc, char **argv) {
    const char *scenario = NULL, *trace = NULL;
    int demo = 0, i;
    hpe_sim_t *sim;
    hpe_status_t status;
    char error[256];
    static const char demo_text[] =
        "# I2C register devices: name, 7-bit address, register count, fill byte\n"
        "device eeprom 0x50 256 0xFF\n"
        "device sensor 0x48 16 0x00\n"
        "set 0x48 0x00 0x5A\n"
        "scan\n"
        "write 0x50 0x10 0xDE 0xAD 0xBE 0xEF\n"
        "readreg 0x50 0x10 4\n"
        "readreg 0x48 0x00 1\n"
        "nack 0x50 1\n"
        "expect-nack 0x50\n"
        "probe 0x50\n";

    for (i = 1; i < argc; ++i) {
        if (strcmp(argv[i], "--help") == 0 || strcmp(argv[i], "-h") == 0) {
            usage(argv[0]); return 0;
        } else if (strcmp(argv[i], "--demo") == 0) demo = 1;
        else if (strcmp(argv[i], "--scenario") == 0 && i + 1 < argc) scenario = argv[++i];
        else if (strcmp(argv[i], "--trace") == 0 && i + 1 < argc) trace = argv[++i];
        else { usage(argv[0]); return 2; }
    }
    if (demo && scenario) { fprintf(stderr, "choose either --demo or --scenario\n"); return 2; }
    if (!demo && !scenario) demo = 1;
    sim = hpe_sim_create();
    if (!sim) { fprintf(stderr, "could not create simulator\n"); return 1; }
    if (demo) status = hpe_scenario_run_text(sim, demo_text, error, sizeof(error));
    else status = hpe_scenario_run_file(sim, scenario, error, sizeof(error));
    if (status != HPE_OK) {
        fprintf(stderr, "scenario failed: %s (%s)\n", error, hpe_status_string(status));
        hpe_sim_destroy(sim); return 1;
    }
    printf("HPE %s | virtual devices: I2C %zu, SPI %zu, SD/eMMC %zu, USB %zu, PCIe %zu | %zu trace events\n\n",
            HPE_VERSION, hpe_sim_device_count(sim), hpe_spi_device_count(sim),
            hpe_block_card_count(sim), hpe_usb_device_count(sim),
            hpe_pcie_endpoint_count(sim), hpe_sim_event_count(sim));
    print_events(sim);
    if (trace) {
        status = hpe_trace_write_text(sim, trace);
        if (status != HPE_OK) {
            fprintf(stderr, "could not write trace '%s': %s\n", trace, hpe_status_string(status));
            hpe_sim_destroy(sim); return 1;
        }
        printf("\nFull protocol trace written to %s\n", trace);
    }
    hpe_sim_destroy(sim);
    return 0;
}
