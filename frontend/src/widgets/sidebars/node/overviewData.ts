import { t } from 'i18next'
import { useEffect, useState } from 'react'

import { apiNodes } from '@/entities/node/api'
import type { BaseFirmwareMap } from '@/entities/node/api/getEEPROMBaseFirmware'
import type { StorageDevice, USBDevice } from '@/entities/node/api/types'
import { boardLabel, boardSoC, type BoardLabel } from '@/entities/node/board'

// ---- Device model / CPU ----------------------------------------------------

// deviceModelLabel turns a node's reported board into a short badge (CM4, CM5,
// Pi 5…). Thin wrapper over the shared classifier in entities/node/board.ts —
// this file used to carry its own copy, and a second one lived in
// bootloaderSection, so the same node could read differently in two panels.
// Pass boardRevision whenever it is available: in the recovery bootstrap the
// model string reports every BCM2712 board as a Compute Module 5.
export const deviceModelLabel = (
  model?: string,
  boardRevision?: string,
): string | null => boardLabel(model, boardRevision)

// cpuCoreName pulls the ARM core name (e.g. "Cortex-A72") out of the agent's
// cpu.info string, which is a comma-joined "Cortex-A72, Revision: 3, ARMv8".
export const cpuCoreName = (info?: string): string | null => {
  if (!info) return null
  const match = info.match(/Cortex-[A-Z]\d+[A-Z]?/i)
  return match ? match[0] : null
}

// stockMaxFreqMHz is the rated max ARM clock per Raspberry Pi model, used when
// config.txt does not override arm_freq. These are fixed hardware facts for the
// devices this platform manages; an explicit arm_freq in config.txt always wins.
// Keyed by BoardLabel: every label the classifier can return needs an entry
// here, or the Overview's CPU-clock row silently disappears for that board.
const STOCK_MAX_FREQ_MHZ: Record<BoardLabel, number> = {
  CM5: 2400,
  'CM5 Lite': 2400,
  'Pi 5': 2400,
  'Pi 500': 2400,
  CM4: 1500,
  CM4S: 1500,
  'Pi 4': 1800,
  'Pi 400': 1800,
  CM3: 1200,
  'Pi 3': 1400,
  'Pi Zero 2': 1000,
}

export const stockMaxFreqMHz = (
  model?: string,
  boardRevision?: string,
): number | null => {
  const label = boardLabel(model, boardRevision)
  return label ? (STOCK_MAX_FREQ_MHZ[label] ?? null) : null
}

// formatFreq renders MHz as a compact GHz string (e.g. 1500 -> "1.5 GHz").
export const formatFreq = (mhz?: number | null): string | null => {
  if (!mhz || mhz <= 0) return null
  return `${(mhz / 1000).toFixed(1)} GHz`
}

// ---- USB devices -----------------------------------------------------------

// usbDeviceLine renders one attached USB device for the Overview: the best name
// the device gave, falling back to its vendor:product ids, which are what a
// support conversation can actually look up.
export const usbDeviceLine = (d: USBDevice): string => {
  const name = [d.manufacturer?.trim(), d.product?.trim()]
    .filter(Boolean)
    .join(' ')
    .trim()
  return name || `${d.vendor_id}:${d.product_id}`
}

// ---- Storage classification ------------------------------------------------

// A storage device rendered as one row of the Overview's Storage list.
export type StorageLine = {
  path: string
  // Short media label: eMMC, SD, NVMe, SSD, HDD, USB…
  media: string
  // Human-readable capacity (e.g. "256 GB", "1.0 TB") or null when unknown.
  size: string | null
  // Model/vendor string, when the device reports one.
  model: string | null
  // External media (USB / removable). Kept as a fact about the device; it no
  // longer splits the UI into two lists — see buildStorageLines.
  external: boolean
  // True when this is the device the node is running from RIGHT NOW. Absent on
  // every device when the answer is not knowable — see resolveBootDevicePath.
  isBoot: boolean
}

// deviceTypeLabel renders the agent's raw device_type for a human. Mirrors the
// agent's own table (infrastructure/hardware/storage_reader.go) so the label in
// the install-target picker and the one in the storage panel agree; a bare
// toUpperCase() here produced "EMMC" and "NVME".
export const deviceTypeLabel = (deviceType?: string): string => {
  switch ((deviceType || '').toLowerCase()) {
    case 'sd':
      return 'SD'
    case 'emmc':
      return 'eMMC'
    case 'mmc':
      return 'SD/eMMC'
    case 'nvme':
      return 'NVMe'
    // Three USB values, because the kernel can only sometimes tell a stick from
    // an external disk: `usb_ssd` is positive evidence (the device reported
    // non-rotating), `usb_flash` is the removable-medium bit, and bare `usb` is
    // "fixed medium, no rotation rate reported" — unknown, not "disk".
    case 'usb_flash':
      return 'USB flash'
    case 'usb_ssd':
      return 'USB SSD'
    case 'usb':
      return 'USB'
    case 'ssd':
      return 'SSD'
    case 'hdd':
      return 'HDD'
    case 'virtio':
      return 'virtio'
    default:
      return (deviceType || '').toUpperCase()
  }
}

const formatCapacity = (bytes?: number): string | null => {
  if (!bytes || bytes <= 0) return null
  const gb = bytes / 1024 ** 3
  if (gb >= 1024) return `${(gb / 1024).toFixed(1)} TB`
  return `${gb >= 100 ? gb.toFixed(0) : gb.toFixed(1)} GB`
}

// classifyStorage turns a raw StorageDevice into a display line.
//
// SD vs eMMC comes from the agent's device_type ("sd" / "emmc"), which reads the
// kernel's /sys/block/mmcblkN/device/type. This used to be guessed here from the
// removable flag — "removable means a slotted card, fixed means on-board eMMC" —
// and that guess was simply wrong: the kernel reports removable=0 for an SD card
// exactly as it does for eMMC, so every SD card was labelled eMMC.
//
// An agent older than 0.5.4 reports device_type "disk" for both, and there is no
// signal here that can separate them. It gets the honest "SD/eMMC" rather than a
// confident guess: on a board carrying both, a wrong label is what makes someone
// choose the wrong install target.
export const classifyStorage = (
  path: string,
  dev: StorageDevice,
): StorageLine => {
  const p = path.toLowerCase()
  const type = (dev.device_type || '').toLowerCase()

  let media: string
  let external = false

  if (p.includes('nvme') || type === 'nvme') {
    media = 'NVMe'
  } else if (type === 'sd') {
    media = 'SD'
  } else if (type === 'emmc') {
    media = 'eMMC'
  } else if (p.includes('mmcblk') || type === 'mmc') {
    media = 'SD/eMMC'
  } else if (type.startsWith('usb') || dev.is_removable) {
    // USB flash / USB SSD / plain USB — see deviceTypeLabel for why there are
    // three. `is_removable` stays as the catch-all for agents below 0.5.4, which
    // reported ssd/hdd from a rotational flag a UAS bridge is free to invent.
    media = deviceTypeLabel(type) || 'USB'
    external = true
  } else if (type === 'ssd') {
    media = 'SSD'
  } else if (type === 'hdd') {
    media = 'HDD'
  } else if (type === 'virtio') {
    media = 'Virtio'
  } else {
    media = type ? type.toUpperCase() : 'Disk'
  }

  const model = dev.model?.trim() || dev.vendor?.trim() || null

  return {
    path,
    media,
    size: formatCapacity(dev.total_capacity),
    model,
    external,
    isBoot: false,
  }
}

// resolveBootDevicePath works out which reported device the node is running
// from, or null when that cannot be answered honestly.
//
// An agent at 0.5.5 or later answers it outright: `boot.root_device` names the
// whole disk carrying `/`, resolved on the device from /proc/mounts and sysfs.
// That is a fact and wins whenever it is present — including when it disagrees
// with the class below, since the class is the guess.
//
// The fallback signal is `current_boot_device`, and it is a CLASS
// from the ROM's boot-mode byte (`sd`, `usb_msd`, `nvme`, `http`…), not a device
// path. So this matches the class against the reported devices and marks one
// ONLY when exactly one device fits:
//
//   - Two devices of the same class (eMMC plus an inserted SD card, both
//     `mmcblk*`) is precisely the ambiguity described above. A guess there
//     would put the marker on the wrong medium in the one place it matters.
//   - `http`/`network`/`rpiboot` mean the node is running from RAM in the
//     recovery bootstrap. NOTHING local is the boot device — marking the disk
//     that merely holds an installed OS would be a lie, and it is the case where
//     an operator is most likely to act on the marker.
//
// A missing marker is honest; a wrong one is not. The exact answer needs the
// agent to report its root device (the `rootDevice` argument).
export const resolveBootDevicePath = (
  devices: Record<string, StorageDevice> | undefined,
  currentBootDevice: string | undefined,
  rootDevice?: string,
): string | null => {
  const entries = Object.entries(devices || {})
  if (entries.length === 0) return null

  // The agent's own answer, when it has one. Matched against the reported
  // devices rather than trusted blindly: a disk the node runs from but does not
  // report is not a row this list can mark, and inventing one would be worse
  // than marking nothing.
  if (rootDevice) {
    const exact = entries.find(([path]) => path === rootDevice)
    return exact ? exact[0] : null
  }

  const cls = (currentBootDevice || '').toLowerCase()
  const matches = (path: string, dev: StorageDevice): boolean => {
    const type = (dev.device_type || '').toLowerCase()
    const p = path.toLowerCase()
    switch (cls) {
      case 'sd':
        // The ROM calls the whole MMC controller "SD", eMMC included.
        return p.includes('mmcblk') || ['sd', 'emmc', 'mmc'].includes(type)
      case 'usb_msd':
      case 'bcm_usb_msd':
        return type.startsWith('usb') || dev.is_removable
      case 'nvme':
        return p.includes('nvme') || type === 'nvme'
      default:
        // http / network / rpiboot / unknown: nothing local booted this node.
        return false
    }
  }

  const hits = entries.filter(([path, dev]) => matches(path, dev))
  return hits.length === 1 ? hits[0][0] : null
}

// buildStorageLines returns every reported device as one list, largest first,
// with the device the node booted from marked.
//
// It used to return two lists, `internal` and `external`, which the panel showed
// as a "Storage" row and a separate "USB" row. That split was wrong in both
// directions on real hardware: a node booting from a USB disk had its actual
// system disk filed under "USB" as if it were an accessory, and a node whose only
// medium is an SD card (a normal Raspberry Pi) had nothing to distinguish it
// from one where the SD card is a spare. What an operator needs is not
// internal-versus-external — it is which of these the machine is running on.
//
// Largest first because that is the one useful default ordering: on the supported
// boards the system disk is the biggest device, and the map's own order is
// whatever /sys/block enumeration produced.
export const buildStorageLines = (
  devices: Record<string, StorageDevice> | undefined,
  currentBootDevice?: string,
  rootDevice?: string,
): StorageLine[] => {
  const bootPath = resolveBootDevicePath(devices, currentBootDevice, rootDevice)
  return Object.entries(devices || {})
    .map(([path, dev]) => ({
      ...classifyStorage(path, dev),
      isBoot: path === bootPath,
      bytes: dev.total_capacity || 0,
    }))
    .sort((a, b) => b.bytes - a.bytes)
    .map(({ bytes: _bytes, ...line }) => line)
}

// ---- Overclock detection ---------------------------------------------------

// config.txt keys that indicate a deliberate overclock. arm_boost is
// intentionally excluded: on Pi 4/5 it is a stock default, not user tuning.
// force_turbo only counts when non-zero.
const OC_KEYS = new Set([
  'arm_freq',
  'arm_freq_min',
  'over_voltage',
  'over_voltage_delta',
  'gpu_freq',
  'core_freq',
  'v3d_freq',
  'h264_freq',
  'isp_freq',
  'hevc_freq',
  'force_turbo',
])

export type OverclockInfo = {
  active: boolean
  // Matched "key=value" lines, for the tooltip.
  settings: string[]
  // The configured arm_freq in MHz, when config.txt sets one — this becomes the
  // effective CPU max clock, overriding the model's stock rating.
  //
  // ONLY when the node is running that config. It comes from the INSTALLED
  // system's config.txt, and a netbooted node is not using it (see the caller).
  armFreqMHz: number | null
  // When the stored copy was read from the device (unix seconds), or null if
  // unknown. This is a stored file, not a live probe: it can be days old while
  // the UI would present it as the machine's current state, so every
  // caller has to be able to say how fresh the claim is.
  readAt: string | null
}

// parseOverclock scans config.txt content for active (uncommented) overclock
// directives. Section headers ([pi4], [all]…) and comments are ignored.
export const parseOverclock = (content: string): OverclockInfo => {
  const settings: string[] = []
  let armFreqMHz: number | null = null
  for (const raw of content.split('\n')) {
    const line = raw.trim()
    if (!line || line.startsWith('#') || line.startsWith('[')) continue
    const eq = line.indexOf('=')
    if (eq <= 0) continue
    const key = line.slice(0, eq).trim().toLowerCase()
    const value = line.slice(eq + 1).trim()
    if (!OC_KEYS.has(key)) continue
    if (key === 'force_turbo' && (value === '0' || value === '')) continue
    if (key === 'arm_freq') {
      const n = parseInt(value, 10)
      if (!Number.isNaN(n) && n > 0) armFreqMHz = n
    }
    settings.push(`${key}=${value}`)
  }
  return { active: settings.length > 0, settings, armFreqMHz, readAt: null }
}

// formatAgo renders how old a stored reading is, in the coarsest unit that is
// still honest. Used wherever a config-derived fact is shown: the number itself
// is only meaningful next to when it was taken.
export const formatAgo = (at: number | string): string => {
  const unixSeconds =
    typeof at === 'number' ? at : new Date(at).getTime() / 1000
  const seconds = Math.max(0, Math.floor(Date.now() / 1000 - unixSeconds))
  if (seconds < 90) return t('widgets.sidebars.node.overviewData.justNow')
  const minutes = Math.floor(seconds / 60)
  if (minutes < 90) {
    return t('widgets.sidebars.node.overviewData.minutesAgo', {
      value: minutes,
    })
  }
  const hours = Math.floor(minutes / 60)
  if (hours < 48) {
    return t('widgets.sidebars.node.overviewData.hoursAgo', { value: hours })
  }
  return t('widgets.sidebars.node.overviewData.daysAgo', {
    value: Math.floor(hours / 24),
  })
}

// useOverclock reads the node's config.txt and reports whether it carries
// overclock directives. It fetches the file's CONTENT explicitly: the tracked-
// file list is metadata only, and reading `content` off a list entry silently
// yielded "stock clocks" for an overclocked node. A failed read stays null
// (unknown) rather than collapsing to "not overclocked".
export const useOverclock = (
  nodeId: string,
  enabled: boolean,
): OverclockInfo | null => {
  const [oc, setOc] = useState<OverclockInfo | null>(null)

  useEffect(() => {
    if (!enabled) {
      setOc(null)
      return
    }
    let alive = true
    apiNodes
      .getConfigFiles(nodeId)
      .then(async (res) => {
        if (!alive) return
        const cfg = (res.files || []).find((f) =>
          f.path?.toLowerCase().endsWith('config.txt'),
        )
        if (!cfg?.path) {
          setOc({ active: false, settings: [], armFreqMHz: null, readAt: null })
          return
        }
        const file = await apiNodes.getConfigFile(nodeId, cfg.path)
        if (!alive) return
        setOc(
          file?.content
            ? {
                ...parseOverclock(file.content),
                readAt: file.created_at ?? null,
              }
            : { active: false, settings: [], armFreqMHz: null, readAt: null },
        )
      })
      .catch(() => {
        if (alive) setOc(null)
      })
    return () => {
      alive = false
    }
  }, [nodeId, enabled])

  return oc
}

// ---- Bootloader (EEPROM) freshness -----------------------------------------

// useBootloaderFreshness compares the node's reported EEPROM firmware build to
// the signer's latest base for its SoC family. Returns 'current'/'outdated', or
// null when there is nothing to compare (no signer, no reported version). An
// install that runs no signer answers no base, so the badge is quietly absent
// rather than an error.
export const useBootloaderFreshness = (
  cpuModel?: string,
  bootloaderVersion?: string,
): 'current' | 'outdated' | null => {
  const [base, setBase] = useState<BaseFirmwareMap | null>(null)

  useEffect(() => {
    let alive = true
    apiNodes
      .getEEPROMBaseFirmware()
      .then((fw) => {
        if (alive) setBase(fw)
      })
      .catch(() => {
        if (alive) setBase(null)
      })
    return () => {
      alive = false
    }
  }, [])

  const soc = boardSoC(cpuModel)
  const latest = base && soc ? base[soc]?.version : undefined
  if (!latest || !bootloaderVersion) return null
  // Compare by hash token + prefix: the signer reports a SHORT hash while the
  // agent reports the FULL git hash (older captures may carry a " (release)"
  // suffix), so exact equality falsely reads a current node as outdated.
  const base16 = bootloaderHashToken(latest)
  const node16 = bootloaderHashToken(bootloaderVersion)
  if (!base16 || !node16) return null
  return node16.startsWith(base16) || base16.startsWith(node16)
    ? 'current'
    : 'outdated'
}

// bootloaderHashToken reduces a raw firmware version to its lowercased hash
// token (first whitespace-separated field), dropping any " (release)" channel
// suffix vcgencmd appends.
export const bootloaderHashToken = (v?: string): string =>
  (v ?? '').trim().split(/\s+/)[0].toLowerCase()

// shortBootloaderVersion is the display form of a firmware version: the first
// 8 hex of the hash token — the same short form the signer's base firmware
// and the historical ROM-UA capture use, so a current node reads identically
// to the "latest" it is compared against.
export const shortBootloaderVersion = (v?: string): string => {
  const t = bootloaderHashToken(v)
  return t ? t.slice(0, 8) : (v ?? '')
}
