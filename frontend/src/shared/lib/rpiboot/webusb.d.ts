// Minimal WebUSB ambient types (subset used by the RPIBOOT client). The project
// has no @types/w3c-web-usb; this declares only what rpiboot.ts references.

interface USBEndpoint {
  endpointNumber: number
  direction: 'in' | 'out'
  type: 'bulk' | 'interrupt' | 'isochronous'
}

interface USBAlternateInterface {
  alternateSetting: number
  endpoints: USBEndpoint[]
}

interface USBInterface {
  interfaceNumber: number
  alternate: USBAlternateInterface
  claimed: boolean
}

interface USBConfiguration {
  configurationValue: number
  interfaces: USBInterface[]
}

interface USBInTransferResult {
  data?: DataView
  status: 'ok' | 'stall' | 'babble'
}

interface USBOutTransferResult {
  bytesWritten: number
  status: 'ok' | 'stall'
}

interface USBControlTransferParameters {
  requestType: 'standard' | 'class' | 'vendor'
  recipient: 'device' | 'interface' | 'endpoint' | 'other'
  request: number
  value: number
  index: number
}

interface USBDevice {
  readonly vendorId: number
  readonly productId: number
  readonly serialNumber?: string
  readonly opened: boolean
  readonly configuration: USBConfiguration | null
  open(): Promise<void>
  close(): Promise<void>
  selectConfiguration(configurationValue: number): Promise<void>
  claimInterface(interfaceNumber: number): Promise<void>
  releaseInterface(interfaceNumber: number): Promise<void>
  controlTransferOut(
    setup: USBControlTransferParameters,
    data?: BufferSource,
  ): Promise<USBOutTransferResult>
  controlTransferIn(
    setup: USBControlTransferParameters,
    length: number,
  ): Promise<USBInTransferResult>
  transferOut(
    endpointNumber: number,
    data: BufferSource,
  ): Promise<USBOutTransferResult>
  transferIn(
    endpointNumber: number,
    length: number,
  ): Promise<USBInTransferResult>
}

interface USBDeviceFilter {
  vendorId?: number
  productId?: number
}

interface USBDeviceRequestOptions {
  filters: USBDeviceFilter[]
}

interface USBConnectionEvent extends Event {
  readonly device: USBDevice
}

interface USB extends EventTarget {
  requestDevice(options: USBDeviceRequestOptions): Promise<USBDevice>
  getDevices(): Promise<USBDevice[]>
  addEventListener(
    type: 'connect' | 'disconnect',
    listener: (event: USBConnectionEvent) => void,
  ): void
  removeEventListener(
    type: 'connect' | 'disconnect',
    listener: (event: USBConnectionEvent) => void,
  ): void
}

interface Navigator {
  readonly usb: USB
}
