expose internal state via callbacks. we need to kno when we connect/disconnect

re connect attempts should loop foever, nover stop reconnecting on disconnect
Watchdog timer on connection failures?

ntp client done. update thr rtc with it

add blutooth stack
- L2CAP (logical link control)
- ATT/GATT (attribute protocol / generic attributes)
- BLE HOGP (HID Over GATT Profile) for wireless keyboard/mouse/gamepad
- HCI transport layer done (WriteHCI/ReadHCI), need upper layers

can we eliminate sleeps? does th echip do inturupts?
