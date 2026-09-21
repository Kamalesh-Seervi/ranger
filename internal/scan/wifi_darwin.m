//go:build darwin

#import <CoreLocation/CoreLocation.h>
#import <CoreWLAN/CoreWLAN.h>
#import <Foundation/Foundation.h>

#include <stdlib.h>
#include <string.h>

#include "wifi_darwin.h"

static char *copyString(NSString *value) {
  if (value == nil) {
    return NULL;
  }
  const char *utf8 = [value UTF8String];
  if (utf8 == NULL) {
    return NULL;
  }
  size_t len = strlen(utf8) + 1;
  char *out = malloc(len);
  if (out == NULL) {
    return NULL;
  }
  memcpy(out, utf8, len);
  return out;
}

// securityLabel picks the strongest security mode a network advertises.
// CWNetwork exposes only a predicate, so the list is walked most-modern first.
static NSString *securityLabel(CWNetwork *network) {
  const struct {
    CWSecurity value;
    __unsafe_unretained NSString *name;
  } modes[] = {
      {kCWSecurityWPA3Enterprise, @"WPA3-Enterprise"},
      {kCWSecurityWPA3Transition, @"WPA3-Transition"},
      {kCWSecurityWPA3Personal, @"WPA3"},
      {kCWSecurityOWETransition, @"OWE-Transition"},
      {kCWSecurityOWE, @"OWE"},
      {kCWSecurityWPA2Enterprise, @"WPA2-Enterprise"},
      {kCWSecurityWPA2Personal, @"WPA2"},
      {kCWSecurityWPAEnterprise, @"WPA-Enterprise"},
      {kCWSecurityWPAPersonal, @"WPA"},
      {kCWSecurityDynamicWEP, @"Dynamic WEP"},
      {kCWSecurityWEP, @"WEP"},
      {kCWSecurityNone, @"Open"},
  };

  for (size_t i = 0; i < sizeof(modes) / sizeof(modes[0]); i++) {
    if ([network supportsSecurity:modes[i].value]) {
      return modes[i].name;
    }
  }
  return @"";
}

char *ranger_wifi_scan(char **errOut) {
  @autoreleasepool {
    if (errOut != NULL) {
      *errOut = NULL;
    }

    CWInterface *iface = [[CWWiFiClient sharedWiFiClient] interface];
    if (iface == nil) {
      if (errOut != NULL) {
        *errOut = copyString(@"no Wi-Fi interface available");
      }
      return NULL;
    }

    NSError *error = nil;
    NSSet<CWNetwork *> *networks = [iface scanForNetworksWithName:nil error:&error];
    if (networks == nil) {
      if (errOut != NULL) {
        NSString *message = error != nil ? [error localizedDescription] : @"scan failed";
        *errOut = copyString(message);
      }
      return NULL;
    }

    NSMutableArray *items = [NSMutableArray arrayWithCapacity:[networks count]];
    for (CWNetwork *network in networks) {
      NSMutableDictionary *item = [NSMutableDictionary dictionary];
      // Without Location Services these come back nil, which is the signal the
      // Go side uses to switch into aggregated, anonymous mode.
      item[@"ssid"] = network.ssid ?: @"";
      item[@"bssid"] = network.bssid ?: @"";
      item[@"rssi"] = @(network.rssiValue);
      item[@"noise"] = @(network.noiseMeasurement);
      item[@"country"] = network.countryCode ?: @"";
      item[@"security"] = securityLabel(network);

      CWChannel *channel = network.wlanChannel;
      if (channel != nil) {
        item[@"channel"] = @((long)channel.channelNumber);
        item[@"band"] = @((int)channel.channelBand);
        item[@"width"] = @((int)channel.channelWidth);
      }
      [items addObject:item];
    }

    NSError *jsonError = nil;
    NSData *data = [NSJSONSerialization dataWithJSONObject:items options:0 error:&jsonError];
    if (data == nil) {
      if (errOut != NULL) {
        NSString *message =
            jsonError != nil ? [jsonError localizedDescription] : @"failed to encode scan results";
        *errOut = copyString(message);
      }
      return NULL;
    }

    NSString *json = [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding];
    return copyString(json);
  }
}

char *ranger_wifi_interface(void) {
  @autoreleasepool {
    CWInterface *iface = [[CWWiFiClient sharedWiFiClient] interface];
    if (iface == nil) {
      return NULL;
    }
    return copyString([iface interfaceName]);
  }
}

static CLLocationManager *gLocationManager = nil;

static CLLocationManager *locationManager(void) {
  if (gLocationManager == nil) {
    gLocationManager = [[CLLocationManager alloc] init];
  }
  return gLocationManager;
}

int ranger_location_status(void) {
  @autoreleasepool {
    return (int)[locationManager() authorizationStatus];
  }
}

void ranger_location_request(void) {
  @autoreleasepool {
    CLLocationManager *manager = locationManager();
    [manager requestAlwaysAuthorization];
    // A command-line tool only gets the prompt once it actually asks for a
    // location, so updates are started briefly and stopped again.
    [manager startUpdatingLocation];
  }
}

void ranger_location_stop(void) {
  @autoreleasepool {
    if (gLocationManager != nil) {
      [gLocationManager stopUpdatingLocation];
    }
  }
}
