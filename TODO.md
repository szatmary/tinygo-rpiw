reconnect attempts should loop foever, nover stop reconnecting on disconnect
Watchdog timer on connection failures?
can we eliminate sleeps? does the chip do inturupts?


add blutooth stack
- L2CAP (logical link control)
- ATT/GATT (attribute protocol / generic attributes)
- BLE HOGP (HID Over GATT Profile) for wireless keyboard/mouse/gamepad
- HCI transport layer done (WriteHCI/ReadHCI), need upper layers



mDNS responder advertising _hap._tcp with the HAP TXT
  records (c#, ff, id, md, pv, s#, sf, ci)


  1. BLE stack — L2CAP → ATT → GATT (tinygo-rpiw has HCI
  transport via WriteHCI/ReadHCI but the upper layers are
  TODO)
