#include "bridge.h"
#include "hpe/hpe.h"
#include <stdlib.h>
#include <string.h>

struct hpe_gui_sim { hpe_sim_t *core; };
hpe_gui_sim *hpe_gui_sim_create(void) {
    hpe_gui_sim *s = (hpe_gui_sim *)calloc(1, sizeof(*s));
    if (s) { s->core = hpe_sim_create(); if (!s->core) { free(s); return NULL; } }
    return s;
}
void hpe_gui_sim_destroy(hpe_gui_sim *s) { if (s) { hpe_sim_destroy(s->core); free(s); } }
int32_t hpe_gui_scenario_run(hpe_gui_sim *s, const char *text, char *error, size_t n) {
    if (!s) return HPE_ERR_ARGUMENT;
    return hpe_scenario_run_text(s->core, text, error, n);
}
size_t hpe_gui_event_count(const hpe_gui_sim *s) { return s ? hpe_sim_event_count(s->core) : 0; }
int32_t hpe_gui_event_get(const hpe_gui_sim *s, size_t i, hpe_gui_event *o) {
    const hpe_event_t *e;
    if (!s || !o || !(e = hpe_sim_event_at(s->core, i))) return HPE_ERR_RANGE;
    memset(o, 0, sizeof(*o));
    o->time_ns=e->time_ns; o->data_start_ns=e->data_start_ns; o->kind=e->kind; o->protocol=e->protocol;
    o->scl=e->scl; o->sda=e->sda; o->clock=e->clock; o->mosi=e->mosi; o->miso=e->miso;
    o->chip_select=e->chip_select; o->value=e->value; o->read=e->read;
    memcpy(o->data_lines,e->data_lines,sizeof(o->data_lines)); o->data_line_count=e->data_line_count;
    memcpy(o->message,e->message,sizeof(o->message)); return HPE_OK;
}
size_t hpe_gui_device_count(const hpe_gui_sim *s) { return s ? hpe_sim_virtual_device_count(s->core) : 0; }
int32_t hpe_gui_device_get(const hpe_gui_sim *s, size_t i, hpe_gui_device *o) {
    hpe_virtual_device_info_t d;
    if (!s || !o || !hpe_sim_virtual_device_at(s->core,i,&d)) return HPE_ERR_RANGE;
    memset(o,0,sizeof(*o)); o->protocol=d.protocol; memcpy(o->name,d.name,sizeof(o->name));
    o->locator=d.locator; o->identity=d.identity; o->memory_size=d.memory_size; o->state=d.state; o->flags=d.flags; return HPE_OK;
}
int32_t hpe_gui_device_read(const hpe_gui_sim *s,size_t i,uint32_t off,uint8_t *data,size_t n) {
    return s ? hpe_sim_virtual_device_read_memory(s->core,i,off,data,n) : HPE_ERR_ARGUMENT;
}
