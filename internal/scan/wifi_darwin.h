#ifndef RANGER_WIFI_DARWIN_H
#define RANGER_WIFI_DARWIN_H

// Every returned char* is malloc'd and must be freed by the caller.

// ranger_wifi_scan returns a JSON array describing nearby networks, or NULL on
// failure. On failure *errOut receives a malloc'd message.
char *ranger_wifi_scan(char **errOut);

// ranger_wifi_interface returns the default Wi-Fi interface name, or NULL when
// the machine has no Wi-Fi hardware.
char *ranger_wifi_interface(void);

// ranger_location_status returns the CoreLocation authorization status.
// 0 not determined, 1 restricted, 2 denied, 3 always, 4 when in use.
int ranger_location_status(void);

// ranger_location_request asks the system for location authorization, which
// macOS requires before it will reveal SSIDs and BSSIDs.
void ranger_location_request(void);

// ranger_location_stop ends the location updates started to trigger the prompt.
void ranger_location_stop(void);

#endif
