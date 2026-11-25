// Minimal RPIBOOT-over-WebUSB client for CM4/CM5, reimplemented in TS from the
// Apache-2.0 raspberrypi/usbboot protocol (main.c). Chromium-only (WebUSB).
//
// Two-phase adoption flow:
//   1) harvest: push a generic second-stage recovery.bin, run the in-browser
//      file-server, capture the board metadata (serial).
//   2) flash: push the per-serial recovery.bin, serve the signed pieeprom
//      package, read the EEPROM_UPDATE result.
//
// NOTE: the file-server / metadata / result wire-format is validated against
// real hardware iteratively — every step logs verbosely via the `log` callback.

const RPI_VENDOR_ID = 0x0a5c
const PID_CM4 = 0x2711 // BCM2711
const PID_CM5 = 0x2712 // BCM2712
const MAX_CHUNK = 16 * 1024
const FILE_MESSAGE_SIZE = 260 // int32 command + fname[256]

export type Soc = 'cm4' | 'cm5'
export type LogFn = (line: string) => void

export const socFromProductId = (pid: number): Soc | null => {
  if (pid === PID_CM4) return 'cm4'
  if (pid === PID_CM5) return 'cm5'
  return null
}

// requestDevice shows the browser chooser filtered to the Pi bootrom.
export const requestBootDevice = async (): Promise<USBDevice> => {
  return navigator.usb.requestDevice({
    filters: [
      { vendorId: RPI_VENDOR_ID, productId: PID_CM4 },
      { vendorId: RPI_VENDOR_ID, productId: PID_CM5 },
    ],
  })
}

// waitForReenumeratedDevice resolves the next Pi bootrom device that appears
// after we push a second stage (the device resets + re-enumerates). Listens for
// the WebUSB 'connect' event, falling back to polling already-granted devices.
export const waitForReenumeratedDevice = (
  log: LogFn,
  timeoutMs = 20000,
): Promise<USBDevice> =>
  new Promise((resolve, reject) => {
    let settled = false
    const onConnect = (e: USBConnectionEvent) => {
      if (e.device.vendorId === RPI_VENDOR_ID) {
        log(
          `re-enumerated: pid=${e.device.productId} serial=${e.device.serialNumber ?? '(none)'}`,
        )
        finish(e.device)
      }
    }
    const finish = (d: USBDevice | null) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      navigator.usb.removeEventListener('connect', onConnect)
      if (d) resolve(d)
      else reject(new Error('device did not re-enumerate in time'))
    }
    const timer = setTimeout(() => {
      log('no connect event; polling granted devices…')
      navigator.usb
        .getDevices()
        .then((devs) => {
          log(
            `getDevices: ${devs.map((d) => `pid=${d.productId}/serial=${d.serialNumber ?? '?'}`).join(', ') || '(none)'}`,
          )
          finish(devs.find((x) => x.vendorId === RPI_VENDOR_ID) ?? null)
        })
        .catch(() => finish(null))
    }, timeoutMs)
    log('listening for device re-enumeration (connect event)…')
    navigator.usb.addEventListener('connect', onConnect)
  })

// RPIBoot wraps one bootrom USB device and exposes the low-level transfers.
export class RPIBoot {
  private device: USBDevice
  private log: LogFn
  private ifaceNum = 0
  private outEp = 1

  constructor(device: USBDevice, log: LogFn) {
    this.device = device
    this.log = log
  }

  get soc(): Soc | null {
    return socFromProductId(this.device.productId)
  }

  // descriptorSerial is the board serial the CM4/CM5 bootrom exposes directly in
  // its USB serial-number descriptor (e.g. "abcd0001") — no second stage needed
  // to read it. Empty/"0" means the ROM did not expose it (older behavior).
  get descriptorSerial(): string | undefined {
    const s = this.device.serialNumber
    return s && s !== '0' ? s : undefined
  }

  async open(): Promise<void> {
    if (!this.device.opened) await this.device.open()
    if (this.device.configuration === null) {
      await this.device.selectConfiguration(1)
    }
    const ifaces = this.device.configuration?.interfaces ?? []

    this.log(
      `descriptor: ${ifaces.length} interface(s), serial=${this.device.serialNumber ?? '(none)'}`,
    )
    for (const i of ifaces) {
      const eps = i.alternate.endpoints
        .map((e) => `#${e.endpointNumber}/${e.direction}/${e.type}`)
        .join(' ')
      this.log(`  iface ${i.interfaceNumber}: ${eps || '(no endpoints)'}`)
    }

    // usbboot: 1 interface -> claim iface 0, data OUT ep 1; else iface 1, OUT ep 3.
    // Reads are done via control-IN on ep0, so no bulk-IN endpoint is needed.
    const chosen =
      ifaces.length <= 1 ? ifaces[0] : (ifaces[1] ?? ifaces[ifaces.length - 1])
    if (!chosen) throw new Error('no usable interface on device')
    this.ifaceNum = chosen.interfaceNumber
    await this.device.claimInterface(this.ifaceNum)
    const out = chosen.alternate.endpoints.find((e) => e.direction === 'out')
    this.outEp = out?.endpointNumber ?? (ifaces.length <= 1 ? 1 : 3)
    this.log(
      `opened: iface=${this.ifaceNum} outEp=${this.outEp} (reads via control-IN)`,
    )
  }

  async close(): Promise<void> {
    try {
      await this.device.releaseInterface(this.ifaceNum)
    } catch {
      /* ignore */
    }
    try {
      await this.device.close()
    } catch {
      /* ignore */
    }
  }

  // epWrite announces the length via a vendor control transfer (no data stage),
  // then streams the bytes in <=16KiB bulk chunks — mirrors usbboot ep_write.
  private async epWrite(data: Uint8Array): Promise<void> {
    await this.device.controlTransferOut({
      requestType: 'vendor',
      recipient: 'device',
      request: 0,
      value: data.length & 0xffff,
      index: (data.length >>> 16) & 0xffff,
    })
    // usbboot ep_write(NULL,0) is a control announce with NO bulk stage; only
    // send bulk data when there is data. (An extra zero-length bulk packet
    // desynchronizes the file-server exchange.)
    for (let off = 0; off < data.length; off += MAX_CHUNK) {
      const chunk = data.subarray(off, Math.min(off + MAX_CHUNK, data.length))
      const res = await this.device.transferOut(this.outEp, chunk)
      if (res.status !== 'ok') {
        throw new Error(`bulk out failed: ${res.status}`)
      }
    }
  }

  // epRead mirrors usbboot ep_read: reads `len` bytes via a vendor control-IN
  // transfer on ep0 (the device exposes NO bulk-IN endpoint — all reads,
  // including the boot return code and file_message requests, come this way).
  private async epRead(len: number): Promise<DataView> {
    const res = await this.device.controlTransferIn(
      {
        requestType: 'vendor',
        recipient: 'device',
        request: 0,
        value: len & 0xffff,
        index: (len >>> 16) & 0xffff,
      },
      len,
    )
    if (res.status !== 'ok' || !res.data) {
      throw new Error(`control-in read failed: ${res.status}`)
    }
    return res.data
  }

  // pushSecondStage sends the boot_message header (length + 20-byte signature,
  // zeroed for unsigned) then the recovery.bin payload, and reads the 4-byte
  // return code (0 = ok). The device then re-enumerates.
  async pushSecondStage(payload: Uint8Array): Promise<number> {
    const header = new Uint8Array(24)
    new DataView(header.buffer).setInt32(0, payload.length, true)
    this.log(`push second stage: ${payload.length} bytes`)
    await this.epWrite(header)
    await this.epWrite(payload)
    try {
      const rc = await this.epRead(4)
      const code = rc.getInt32(0, true)
      this.log(`second stage return code: ${code}`)
      return code
    } catch {
      // Expected: the device resets + re-enumerates right after accepting the
      // second stage, so the return-code IN transfer fails. Caller should now
      // wait for re-enumeration and reconnect.
      this.log('second stage accepted; device is re-enumerating…')
      return 0
    }
  }

  // fileServer answers the device's file requests until it sends Done. `files`
  // maps requested names to bytes; `onMetadata` receives any metadata blob.
  // The exact request struct is validated on hardware — this logs each request.
  async fileServer(
    files: Record<string, Uint8Array>,
    onMetadata?: (name: string, data: Uint8Array) => void,
  ): Promise<void> {
    for (;;) {
      let req: DataView
      try {
        req = await this.epRead(FILE_MESSAGE_SIZE)
      } catch (e) {
        this.log(`file-server: read ended: ${String(e)}`)
        return
      }
      const command = req.getInt32(0, true)
      const nameBytes = new Uint8Array(req.buffer, req.byteOffset + 4)
      let end = nameBytes.indexOf(0)
      if (end < 0) end = nameBytes.length
      const fname = new TextDecoder().decode(nameBytes.subarray(0, end))

      // Empty name = the device is done.
      if (fname.length === 0) {
        this.log('file-server: empty name — done')
        await this.epWrite(new Uint8Array(0))
        return
      }
      this.log(`file-server request: cmd=${command} name="${fname}"`)

      if (command === 2) {
        this.log('file-server: done')
        return
      }
      // Metadata: the device encodes one key/value per request name as
      // "*PROPERTY*VALUE" (recovery_metadata=1). Ack with a zero-length write.
      if (fname.startsWith('*')) {
        if (onMetadata) onMetadata(fname, new Uint8Array(0))
        await this.epWrite(new Uint8Array(0))
        continue
      }
      const data = files[fname] ?? files[stripSoc(fname)]
      if (command === 0) {
        // GetFileSize -> announce size via the vendor control encoding.
        const size = data ? data.length : 0
        await this.device.controlTransferOut({
          requestType: 'vendor',
          recipient: 'device',
          request: 0,
          value: size & 0xffff,
          index: (size >>> 16) & 0xffff,
        })
        this.log(`file-server: size("${fname}") = ${size}`)
      } else if (command === 1) {
        // ReadFile -> stream the bytes (or zero-length if we don't have it).
        if (!data) {
          this.log(`file-server: MISSING "${fname}" — sending empty`)
          await this.epWrite(new Uint8Array(0))
        } else {
          await this.epWrite(data)
          this.log(`file-server: sent "${fname}" (${data.length} bytes)`)
        }
      }
    }
  }
}

// The ROM requests the second stage as bootcode4.bin/bootcode5.bin; our package
// names it recovery.bin. Map both ways so the file-server matches.
const stripSoc = (name: string): string => {
  if (name === 'bootcode4.bin' || name === 'bootcode5.bin')
    return 'recovery.bin'
  return name
}
