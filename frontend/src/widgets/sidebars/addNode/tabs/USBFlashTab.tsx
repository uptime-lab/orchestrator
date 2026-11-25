import { FC, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  deviceCapRefusal,
  isDeviceCapUnverified,
  type DeviceCapRefusal,
} from '@/entities/entitlement/deviceCap'
import { DeviceCapNotice } from '@/entities/entitlement/ui/DeviceCapNotice'
import { nodesModel } from '@/entities/node'
import {
  fetchPackageFile,
  webusbCommitFlash,
  webusbProvision,
} from '@/entities/node/api/webusb'
import { useServerCapabilities } from '@/entities/serverCapabilities'

import {
  currentWebUSBEnvironment,
  webusbAvailability,
} from '@/shared/lib/rpiboot/availability'
import {
  requestBootDevice,
  RPIBoot,
  waitForReenumeratedDevice,
  type Soc,
} from '@/shared/lib/rpiboot/rpiboot'
import { Button } from '@/shared/ui/button/button.tsx'
import { InfoHint } from '@/shared/ui/infoHint'
import { Input } from '@/shared/ui/input'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { serialLeaseHint } from '../serialLease'

// On Windows, WebUSB can only see a device whose interface is bound to the WinUSB
// driver, and the Pi bootROM advertises no WCID descriptors, so Windows never
// binds it automatically — the requestDevice() picker comes up empty. macOS and
// Linux have no such requirement, which is why the same flow works there. Nothing
// in this code can make the raw bootROM appear; the fix is a one-time driver
// install on the machine, so the tab tells the operator that up front.
const isWindows =
  typeof navigator !== 'undefined' && /Windows/i.test(navigator.userAgent)

// USBFlashTab: flash a CM4/CM5 EEPROM directly from the browser over WebUSB. The
// board serial is read straight from the USB descriptor on connect (the short
// low-word form — all the ROM exposes); the backend matches it to the full node.
// The device is baked with a node's adoption token so it boots network-first and
// self-adopts. Chromium-only, and only on a secure (HTTPS or localhost) page.
//
// onOpenSdBundle, when this install offers SD provisioning, switches to that
// tab: it is the way to prepare a board when this page cannot flash.
export const USBFlashTab: FC<{ onOpenSdBundle?: () => void }> = ({
  onOpenSdBundle,
}) => {
  const { t } = useTranslation()
  const availability = webusbAvailability(currentWebUSBEnvironment())
  const leaseHint = serialLeaseHint(useServerCapabilities().serial_lease_days)
  const [log, setLog] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [soc, setSoc] = useState<Soc | null>(null)
  const [serial, setSerial] = useState('')
  const [nodeName, setNodeName] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [capRefusal, setCapRefusal] = useState<DeviceCapRefusal | null>(null)
  const [done, setDone] = useState<string | null>(null)
  const bootRef = useRef<RPIBoot | null>(null)

  const append = (line: string) => setLog((l) => [...l, line])

  // Release a held device handle (claimed interface) — best-effort.
  const dropDevice = async () => {
    const held = bootRef.current
    bootRef.current = null
    if (held) await held.close().catch(() => {})
  }

  // Release the USB handle on unmount so the device isn't left claimed.
  useEffect(() => {
    return () => {
      void dropDevice()
    }
  }, [])

  const connect = async () => {
    setError(null)
    setDone(null)
    // A serial belongs to ONE board: always reset before probing the (possibly
    // different) newly picked device, or a stale value could bake board A's
    // package onto board B.
    setSerial('')
    setSoc(null)
    await dropDevice()
    try {
      const device = await requestBootDevice()
      const boot = new RPIBoot(device, append)
      bootRef.current = boot
      await boot.open()
      setSoc(boot.soc)
      const ds = boot.descriptorSerial
      if (ds) {
        setSerial(ds)
        append(
          t('widgets.sidebars.addNode.usb.logSerialFromUsb', { serial: ds }),
        )
      } else {
        append(t('widgets.sidebars.addNode.usb.logNoSerial'))
      }
    } catch (e: unknown) {
      await dropDevice()
      setSoc(null)
      // An empty picker on Windows is almost always the missing WinUSB driver,
      // not a real fault — point at the fix rather than showing a bare
      // "NotFoundError: No device selected".
      const notFound = e instanceof DOMException && e.name === 'NotFoundError'
      setError(
        notFound && isWindows
          ? t('widgets.sidebars.addNode.usb.errorNoBootDevice')
          : String(e),
      )
    }
  }

  const flash = async () => {
    const boot = bootRef.current
    if (!boot || !boot.soc) {
      setError(t('widgets.sidebars.addNode.usb.errorConnectFirst'))
      return
    }
    if (!/^[0-9a-fA-F]{6,16}$/.test(serial.trim())) {
      setError(t('widgets.sidebars.addNode.usb.errorSerialRequired'))
      return
    }
    setBusy(true)
    setError(null)
    setCapRefusal(null)
    setDone(null)
    try {
      append(t('widgets.sidebars.addNode.usb.logBaking'))
      const prov = await webusbProvision({
        soc: boot.soc,
        serial: serial.trim(),
        node_name: nodeName.trim() || undefined,
      })
      append(
        t('widgets.sidebars.addNode.usb.logNodeReady', {
          nodeName: prov.node_name,
          packageBase: prov.package_base,
        }),
      )

      append(t('widgets.sidebars.addNode.usb.logFetchingPackage'))
      const files: Record<string, Uint8Array> = {}
      for (const name of prov.files) {
        files[name] = new Uint8Array(
          await fetchPackageFile(prov.package_base, name, prov.sig_query ?? ''),
        )
        append(
          t('widgets.sidebars.addNode.usb.logPackageFile', {
            name,
            bytes: files[name].length,
          }),
        )
      }

      if (!files['recovery.bin']) {
        throw new Error(
          t('widgets.sidebars.addNode.usb.errorMissingRecoveryBin'),
        )
      }

      append(t('widgets.sidebars.addNode.usb.logPushingSecondStage'))
      await boot.pushSecondStage(files['recovery.bin'])
      // The device re-enumerates after the second stage; the old handle is dead.
      await dropDevice()
      append(t('widgets.sidebars.addNode.usb.logWaitingReenumerate'))
      const reDevice = await waitForReenumeratedDevice(append)
      const boot2 = new RPIBoot(reDevice, append)
      bootRef.current = boot2
      await boot2.open()
      append(t('widgets.sidebars.addNode.usb.logServingPackage'))
      await boot2.fileServer(files)
      append(t('widgets.sidebars.addNode.usb.logFlashSent'))
      // Promote the node to ready_for_adoption now that the flash completed.
      // Until this commit it stays in the "flashing" state, so a window closed
      // mid-flash leaves the node visibly "flashing" as a retry cue rather than
      // a misleading "ready" node that was never actually written.
      await webusbCommitFlash(prov.node_id)
      await nodesModel.fetchNodesFx()
      setDone(
        t('widgets.sidebars.addNode.usb.flashedNotice', {
          nodeName: prov.node_name,
          serial: serial.trim(),
        }),
      )
    } catch (e: unknown) {
      // The bake was refused before the EEPROM was touched: the install is
      // at its device cap. The device is unchanged.
      const cap = deviceCapRefusal(e)
      if (cap) {
        setCapRefusal(cap)
      } else if (isDeviceCapUnverified(e)) {
        setError(t('entities.entitlement.deviceCap.unverified'))
      } else {
        setError(extractApiErrorMessage(e, String(e)))
      }
    } finally {
      // The device reboots into its EEPROM after the flash (or errored out) —
      // either way release the handle and require a fresh Connect.
      await dropDevice()
      setSoc(null)
      setBusy(false)
    }
  }

  if (availability !== 'available') {
    return (
      <div className="flex flex-col gap-3">
        <div className="bg-amber-6/10 border border-amber-6/50 text-amber-4 px-4 py-3 rounded text-sm">
          {availability === 'insecure-context' ? (
            t('widgets.sidebars.addNode.usb.insecureContext')
          ) : (
            <>
              {t('widgets.sidebars.addNode.usb.unsupportedPrefix')}
              <code className="mx-1 text-neutral-3">rpiboot</code>{' '}
              {t('widgets.sidebars.addNode.usb.unsupportedSuffix')}
            </>
          )}
        </div>
        {onOpenSdBundle && (
          <div className="flex flex-col items-start gap-2 text-sm text-neutral-4">
            {t('widgets.sidebars.addNode.usb.sdAlternative')}
            <Button size="md" kind="secondary" onClick={onOpenSdBundle}>
              {t('widgets.sidebars.addNode.usb.openSdBundle')}
            </Button>
          </div>
        )}
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="text-sm text-neutral-4">
        {t('widgets.sidebars.addNode.usb.description')}
      </div>

      {isWindows && (
        <div className="bg-amber-6/10 border border-amber-6/50 text-amber-4 px-4 py-2 rounded text-xs">
          <span className="font-semibold">
            {t('widgets.sidebars.addNode.usb.windowsOnWindows')}
          </span>{' '}
          {t('widgets.sidebars.addNode.usb.windowsInstallThe')}{' '}
          <span className="font-semibold">WinUSB</span>{' '}
          {t('widgets.sidebars.addNode.usb.windowsDriverIntro')}{' '}
          <a
            href="https://github.com/raspberrypi/usbboot"
            target="_blank"
            rel="noreferrer"
            className="underline"
          >
            usbboot
          </a>{' '}
          {t('widgets.sidebars.addNode.usb.windowsInstallerOrBind')}{' '}
          <a
            href="https://zadig.akeo.ie"
            target="_blank"
            rel="noreferrer"
            className="underline"
          >
            Zadig
          </a>{' '}
          {t('widgets.sidebars.addNode.usb.windowsZadigSteps')}
        </div>
      )}

      {capRefusal && <DeviceCapNotice refusal={capRefusal} />}
      {error && (
        <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-2 rounded text-sm break-all">
          {error}
        </div>
      )}
      {done && (
        <div className="bg-lime-6/10 border border-lime-6/50 text-lime-5 px-4 py-2 rounded text-sm">
          {done}
        </div>
      )}

      <Button size="md" kind="secondary" onClick={connect} disabled={busy} grow>
        {soc
          ? t('widgets.sidebars.addNode.usb.connected', { soc })
          : t('widgets.sidebars.addNode.usb.connect')}
      </Button>

      <Input
        label={t('widgets.sidebars.addNode.usb.serialLabel')}
        value={serial}
        onChange={(e) => setSerial(e.target.value)}
        placeholder={t('widgets.sidebars.addNode.usb.serialPlaceholder')}
        disabled={busy}
      />
      <Input
        label={t('widgets.sidebars.addNode.usb.nodeNameLabel')}
        value={nodeName}
        onChange={(e) => setNodeName(e.target.value)}
        disabled={busy}
      />

      <div className="bg-amber-6/10 border border-amber-6/50 text-amber-4 px-4 py-2 rounded text-xs">
        {t('widgets.sidebars.addNode.usb.flashWarning')}
      </div>

      {leaseHint.kind !== 'none' && (
        <div className="text-xs text-neutral-4">
          {leaseHint.kind === 'days'
            ? t('widgets.sidebars.addNode.usb.leaseDays', {
                count: leaseHint.days,
              })
            : t('widgets.sidebars.addNode.usb.leaseDefault')}
          <span className="whitespace-nowrap">
            &nbsp;
            <InfoHint text={t('widgets.sidebars.addNode.usb.leaseTooltip')} />
          </span>
        </div>
      )}

      <Button size="lg" onClick={flash} disabled={busy || !soc} grow>
        {busy
          ? t('widgets.sidebars.addNode.usb.working')
          : t('widgets.sidebars.addNode.usb.flash')}
      </Button>

      {log.length > 0 && (
        <pre className="bg-neutral-13 rounded px-3 py-2 text-xs text-neutral-4 font-mono max-h-64 overflow-auto whitespace-pre-wrap">
          {log.join('\n')}
        </pre>
      )}
    </div>
  )
}
