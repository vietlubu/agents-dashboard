#ifndef AGENTS_DASHBOARD_LID_NATIVE_H
#define AGENTS_DASHBOARD_LID_NATIVE_H
#include <stdint.h>

typedef struct dashboard_lid_watch dashboard_lid_watch;
uint32_t dashboard_lid_open(void);
uint32_t dashboard_lid_call(uint32_t connection, uint32_t selector, uint64_t input, uint32_t count);
uint32_t dashboard_lid_close(uint32_t connection);
uint32_t dashboard_lid_read(int *effective);
dashboard_lid_watch *dashboard_lid_watch_start(uint32_t *result);
int dashboard_lid_watch_poll(dashboard_lid_watch *watch);
void dashboard_lid_watch_stop(dashboard_lid_watch *watch);
#endif
