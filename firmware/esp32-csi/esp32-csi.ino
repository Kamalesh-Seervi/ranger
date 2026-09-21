// Ranger CSI sensor firmware for ESP32.
//
// Captures 802.11 Channel State Information from the Wi-Fi radio and streams it
// to a running `ranger -csi` host over UDP, in the exact "RCSI" wire format the
// Go parser in internal/csi expects. That stream is what lets ranger sense a
// still body through a wall: presence, motion, and breathing.
//
// Boards: any ESP32 dev board works — a plain ESP32-DevKitC or an ESP32-S3.
// The classic ESP32 is the cheapest and most tested for CSI.
//
// Build: Arduino IDE with the "esp32" board package (Espressif), or arduino-cli.
//   Select your board, set the four CONFIG values below, upload.
//
// How the CSI stream is produced: an associated station only measures CSI on
// frames it receives. This sketch sends a small UDP keep-alive to the ranger
// host several times a second; the access point ACKs each one, and CSI is
// measured on those ACKs. If your frame rate is low, generate extra traffic —
// for example run `ping -i 0.01 <esp-ip>` from another machine on the LAN.
//
// Legal note: this only *receives* radio data already present in the air and
// measures your own link. It transmits nothing but ordinary Wi-Fi frames.

#include <WiFi.h>
#include <WiFiUdp.h>
#include "esp_wifi.h"

// ---- CONFIG ---------------------------------------------------------------
static const char *WIFI_SSID = "your-network";
static const char *WIFI_PASS = "your-password";
static const char *RANGER_HOST = "192.168.1.50"; // the machine running ranger -csi
static const uint16_t RANGER_PORT = 5566;
// ---------------------------------------------------------------------------

// Wire format, little-endian, matching internal/csi.
//   magic "RCSI", version 1, flags, seq u16, sensorMAC[6], rssi i8, channel u8,
//   rate u8, secondary u8, timestamp u32 (micros), subcarriers u16,
//   then (imaginary, real) int8 pairs — one per subcarrier.
static const uint8_t WIRE_VERSION = 1;
static const size_t HEADER_LEN = 24;
static const size_t MAX_CSI = 256 * 2; // bytes of CSI payload we will forward

struct CsiPacket {
  uint16_t len; // number of CSI payload bytes
  int8_t rssi;
  uint8_t channel;
  uint8_t rate;
  uint8_t secondary;
  uint32_t timestampUs;
  uint8_t data[MAX_CSI];
};

static QueueHandle_t csiQueue;
static WiFiUDP udp;
static uint8_t staMac[6];
static volatile uint16_t seqCounter = 0;

// onCSI runs in the Wi-Fi task. It must be fast and must not touch the network
// stack, so it only copies the record into a queue for the sender task.
static void onCSI(void *ctx, wifi_csi_info_t *info) {
  if (!info || !info->buf || info->len <= 0) {
    return;
  }
  CsiPacket pkt;
  pkt.len = info->len > (int)MAX_CSI ? MAX_CSI : (uint16_t)info->len;
  pkt.rssi = info->rx_ctrl.rssi;
  pkt.channel = info->rx_ctrl.channel;
  pkt.rate = info->rx_ctrl.rate;
  pkt.secondary = info->rx_ctrl.secondary_channel;
  pkt.timestampUs = info->rx_ctrl.timestamp;
  // The buffer is signed (imaginary, real) byte pairs. Amplitude is computed on
  // the ranger side as hypot(re, im), which is order-independent, so a raw copy
  // is correct regardless of which byte is which.
  memcpy(pkt.data, info->buf, pkt.len);

  BaseType_t woken = pdFALSE;
  xQueueSendFromISR(csiQueue, &pkt, &woken);
  if (woken) {
    portYIELD_FROM_ISR();
  }
}

// senderTask drains the queue, frames each record, and forwards it over UDP.
static void senderTask(void *arg) {
  uint8_t out[HEADER_LEN + MAX_CSI];
  CsiPacket pkt;
  for (;;) {
    if (xQueueReceive(csiQueue, &pkt, portMAX_DELAY) != pdTRUE) {
      continue;
    }
    uint16_t subcarriers = pkt.len / 2;
    if (subcarriers == 0) {
      continue;
    }
    uint16_t seq = seqCounter++;

    out[0] = 'R'; out[1] = 'C'; out[2] = 'S'; out[3] = 'I';
    out[4] = WIRE_VERSION;
    out[5] = 0; // flags
    out[6] = seq & 0xFF; out[7] = seq >> 8;
    memcpy(out + 8, staMac, 6);
    out[14] = (uint8_t)pkt.rssi;
    out[15] = pkt.channel;
    out[16] = pkt.rate;
    out[17] = pkt.secondary;
    out[18] = pkt.timestampUs & 0xFF;
    out[19] = (pkt.timestampUs >> 8) & 0xFF;
    out[20] = (pkt.timestampUs >> 16) & 0xFF;
    out[21] = (pkt.timestampUs >> 24) & 0xFF;
    out[22] = subcarriers & 0xFF; out[23] = subcarriers >> 8;
    memcpy(out + HEADER_LEN, pkt.data, subcarriers * 2);

    udp.beginPacket(RANGER_HOST, RANGER_PORT);
    udp.write(out, HEADER_LEN + subcarriers * 2);
    udp.endPacket();
  }
}

static void startCSI() {
  wifi_csi_config_t cfg = {};
  cfg.lltf_en = true;
  cfg.htltf_en = true;
  cfg.stbc_htltf2_en = true;
  cfg.ltf_merge_en = true;
  cfg.channel_filter_en = true;
  cfg.manu_scale = false;
  cfg.shift = false;

  esp_wifi_set_csi_config(&cfg);
  esp_wifi_set_csi_rx_cb(onCSI, nullptr);
  esp_wifi_set_csi(true);
}

void setup() {
  Serial.begin(115200);

  csiQueue = xQueueCreate(64, sizeof(CsiPacket));

  WiFi.mode(WIFI_STA);
  WiFi.begin(WIFI_SSID, WIFI_PASS);
  Serial.print("connecting to Wi-Fi");
  while (WiFi.status() != WL_CONNECTED) {
    delay(300);
    Serial.print('.');
  }
  Serial.println();
  Serial.print("ip: ");
  Serial.println(WiFi.localIP());

  WiFi.macAddress(staMac);
  startCSI();

  xTaskCreatePinnedToCore(senderTask, "csiSender", 4096, nullptr, 1, nullptr, 0);
  Serial.println("streaming CSI");
}

void loop() {
  // Elicit ACKs so CSI is measured steadily. Each keep-alive draws one ACK from
  // the access point; ~15 ms spacing yields roughly 60 records per second.
  udp.beginPacket(RANGER_HOST, RANGER_PORT + 1);
  udp.write((const uint8_t *)"k", 1);
  udp.endPacket();
  delay(15);
}
