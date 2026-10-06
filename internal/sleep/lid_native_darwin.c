//go:build darwin && cgo

#include "lid_native_darwin.h"
#include <stdbool.h>
#include <stdlib.h>
#include <CoreFoundation/CoreFoundation.h>
#include <IOKit/IOKitLib.h>
#include <IOKit/pwr_mgt/IOPM.h>
#include <IOKit/pwr_mgt/IOPMLib.h>
#include <IOKit/pwr_mgt/IOPMLibDefs.h>
#include <IOKit/ps/IOPowerSources.h>

_Static_assert(kPMSetClamshellSleepState == 12, "unexpected lid selector ABI");

uint32_t dashboard_lid_open(void) {
    return IOPMFindPowerManagement(MACH_PORT_NULL);
}
uint32_t dashboard_lid_call(uint32_t connection, uint32_t selector, uint64_t input, uint32_t count) {
    return IOConnectCallScalarMethod(connection, selector, &input, count, NULL, NULL);
}
uint32_t dashboard_lid_close(uint32_t connection) {
    return IOServiceClose(connection);
}
uint32_t dashboard_lid_read(int *effective) {
    io_service_t root = IOServiceGetMatchingService(MACH_PORT_NULL, IOServiceMatching("IOPMrootDomain"));
    if (!root) return kIOReturnNotFound;
    CFTypeRef value = IORegistryEntryCreateCFProperty(root, CFSTR(kAppleClamshellCausesSleepKey), kCFAllocatorDefault, 0);
    IOObjectRelease(root);
    if (!value) return kIOReturnNotFound;
    uint32_t result = kIOReturnSuccess;
    if (CFGetTypeID(value) != CFBooleanGetTypeID()) result = kIOReturnBadArgument;
    else *effective = !CFBooleanGetValue((CFBooleanRef)value);
    CFRelease(value);
    return result;
}

struct dashboard_lid_watch {
    CFRunLoopRef loop;
    IONotificationPortRef port;
    CFRunLoopSourceRef source;
    CFRunLoopSourceRef power_source;
    io_object_t notification;
    int changed;
};

static void lid_interest(void *context, io_service_t service, uint32_t message, void *argument) {
    (void)service;
    dashboard_lid_watch *watch = context;
    // General-interest observation never acknowledges or vetoes sleep. UUID
    // removal observes wake completion; clamshell includes effective policy.
    if (message == kIOPMMessageClamshellStateChange ||
        (message == kIOPMMessageSleepWakeUUIDChange && argument == kIOPMMessageSleepWakeUUIDCleared) ||
        message == kIOPMMessageSystemPowerEventOccurred) watch->changed = 1;
}
static void lid_power_source(void *context) {
    ((dashboard_lid_watch *)context)->changed = 1;
}

void dashboard_lid_watch_stop(dashboard_lid_watch *watch) {
    if (!watch) return;
    if (watch->power_source) {
        CFRunLoopRemoveSource(watch->loop, watch->power_source, kCFRunLoopDefaultMode);
        CFRunLoopSourceInvalidate(watch->power_source);
        CFRelease(watch->power_source);
    }
    if (watch->notification) IOObjectRelease(watch->notification);
    if (watch->source) CFRunLoopRemoveSource(watch->loop, watch->source, kCFRunLoopDefaultMode);
    if (watch->port) IONotificationPortDestroy(watch->port);
    free(watch);
}

dashboard_lid_watch *dashboard_lid_watch_start(uint32_t *result) {
    *result = kIOReturnNoMemory;
    dashboard_lid_watch *watch = calloc(1, sizeof(*watch));
    if (!watch) return NULL;
    watch->loop = CFRunLoopGetCurrent();
    watch->port = IONotificationPortCreate(MACH_PORT_NULL);
    if (!watch->port) goto fail;
    watch->source = IONotificationPortGetRunLoopSource(watch->port);
    if (!watch->source) goto fail;
    CFRunLoopAddSource(watch->loop, watch->source, kCFRunLoopDefaultMode);
    io_service_t root = IOServiceGetMatchingService(MACH_PORT_NULL, IOServiceMatching("IOPMrootDomain"));
    if (!root) { *result = kIOReturnNotFound; goto fail; }
    *result = IOServiceAddInterestNotification(watch->port, root, kIOGeneralInterest, lid_interest, watch, &watch->notification);
    IOObjectRelease(root);
    if (*result != kIOReturnSuccess) goto fail;
    watch->power_source = IOPSNotificationCreateRunLoopSource(lid_power_source, watch);
    if (!watch->power_source) { *result = kIOReturnNotReady; goto fail; }
    CFRunLoopAddSource(watch->loop, watch->power_source, kCFRunLoopDefaultMode);
    return watch;
fail:
    dashboard_lid_watch_stop(watch);
    return NULL;
}
int dashboard_lid_watch_poll(dashboard_lid_watch *watch) {
    CFRunLoopRunInMode(kCFRunLoopDefaultMode, 0.25, true);
    int changed = watch->changed;
    watch->changed = 0;
    return changed;
}
