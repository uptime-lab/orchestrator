import type { TFunction } from 'i18next'
import { FC, useEffect, useRef, useState, type SyntheticEvent } from 'react'
import { useTranslation } from 'react-i18next'

import {
  deviceCapRefusal,
  isDeviceCapUnverified,
  type DeviceCapRefusal,
} from '@/entities/entitlement/deviceCap'
import { DeviceCapNotice } from '@/entities/entitlement/ui/DeviceCapNotice'
import {
  getAdoptionJob,
  sshAdopt,
  type AdoptionJob,
} from '@/entities/node/api/postSSHAdopt'
import {
  sshPreflight,
  type SSHPreflightResult,
} from '@/entities/node/api/postSSHPreflight'
import {
  applyPreflightResult,
  expectedFingerprint,
  type HostKeyTrust,
} from '@/entities/node/sshHostKeyTrust'
import { sshKeysModel, useSSHKeys } from '@/entities/sshKey'

import { Button } from '@/shared/ui/button/button.tsx'
import { InfoHint } from '@/shared/ui/infoHint'
import { Input } from '@/shared/ui/input'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { consoleLoginError, ConsoleLoginFields } from '../ConsoleLoginFields'

type AuthMethod = 'password' | 'key' | 'agent'

const isTerminal = (status: string) =>
  status === 'completed' || status === 'failed'

// Human-readable labels for the backend sudo_mode probe values.
const sudoLabel = (t: TFunction, mode: string): string | undefined => {
  switch (mode) {
    case 'root':
      return t('widgets.sidebars.addNode.ssh.sudoRoot')
    case 'nopasswd':
      return t('widgets.sidebars.addNode.ssh.sudoNopasswd')
    case 'password':
      return t('widgets.sidebars.addNode.ssh.sudoPasswordOk')
    case 'none':
      return t('widgets.sidebars.addNode.ssh.sudoNone')
    case 'bad_password':
      return t('widgets.sidebars.addNode.ssh.sudoBadPassword')
    default:
      return undefined
  }
}

// SSHTab: test a connection, then adopt an already-running device —
// install the agent, register the node and (optionally) flash a network-first
// EEPROM. Installing the agent and flashing reboot the device.
export const SSHTab: FC = () => {
  const { t } = useTranslation()
  const { keys } = useSSHKeys()
  const connectionKeys = keys.filter((k) => k.has_private_key)

  // Connection
  const [host, setHost] = useState('')
  const [port, setPort] = useState('22')
  const [username, setUsername] = useState('')
  const [authMethod, setAuthMethod] = useState<AuthMethod>('password')
  const [password, setPassword] = useState('')
  const [sshKeyId, setSSHKeyId] = useState('')
  const [sudoPassword, setSudoPassword] = useState('')

  // Node record
  const [nodeName, setNodeName] = useState('')
  const [defaultUsername, setDefaultUsername] = useState('')
  const [defaultPassword, setDefaultPassword] = useState('')
  const [networkFirst, setNetworkFirst] = useState(true)
  const [ack, setAck] = useState(false)

  const [error, setError] = useState<string | null>(null)
  // Adopt errors render next to the Adopt button (bottom of a long form), so
  // they are visible where the click happens instead of scrolled off up top.
  const [adoptError, setAdoptError] = useState<string | null>(null)
  const [capRefusal, setCapRefusal] = useState<DeviceCapRefusal | null>(null)
  const [testing, setTesting] = useState(false)
  const [accepting, setAccepting] = useState(false)
  const [adopting, setAdopting] = useState(false)
  const [preflight, setPreflight] = useState<SSHPreflightResult | null>(null)
  // The host key the operator trusts for this host:port, SEPARATE from the
  // latest probe result: a failed probe must not forget it (the retry would
  // then carry no expectation and take whatever key answered), and only the
  // explicit "Accept new host key" action may replace it. Transitions live in
  // entities/node/sshHostKeyTrust.
  const [trust, setTrust] = useState<HostKeyTrust | null>(null)
  const [job, setJob] = useState<AdoptionJob | null>(null)
  const pollRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const adoptErrorRef = useRef<HTMLDivElement | null>(null)

  // When this device is already registered, adoption reuses that node — the
  // new-node name/credentials are irrelevant (and not required).
  const reusing = !!preflight?.existing_node_id

  useEffect(() => {
    if (adoptError) {
      adoptErrorRef.current?.scrollIntoView({
        behavior: 'smooth',
        block: 'center',
      })
    }
  }, [adoptError])

  // A preflight result describes ONE target; once any connection field changes
  // it no longer applies (stale "already registered" state would hide the
  // new-node fields for a different device). Drop it with the errors.
  const invalidateProbe = () => {
    setPreflight(null)
    setError(null)
    setAdoptError(null)
    setCapRefusal(null)
  }
  // Trust is for ONE host:port. A different target starts from first contact;
  // a different username or auth method does not change whose key it is.
  const retarget = () => {
    invalidateProbe()
    setTrust(null)
  }

  useEffect(() => {
    sshKeysModel.fetchSSHKeysFx()
    return () => {
      if (pollRef.current) clearTimeout(pollRef.current)
    }
  }, [])

  useEffect(() => {
    if (connectionKeys.length > 0 && !sshKeyId)
      setSSHKeyId(connectionKeys[0].id)
  }, [connectionKeys, sshKeyId])

  // Poll the job while it is in flight.
  useEffect(() => {
    if (!job || isTerminal(job.status)) return
    pollRef.current = setTimeout(async () => {
      try {
        const next = await getAdoptionJob(job.id)
        setJob(next)
      } catch {
        /* keep last state; will retry on next tick if not terminal */
        setJob((j) => (j ? { ...j } : j))
      }
    }, 3000)
    return () => {
      if (pollRef.current) clearTimeout(pollRef.current)
    }
  }, [job])

  const connectionBody = () => ({
    host: host.trim(),
    port: Number(port) || 22,
    username: username.trim(),
    password: authMethod === 'password' ? password : undefined,
    use_agent: authMethod === 'agent' ? true : undefined,
    sudo_password: sudoPassword || undefined,
  })

  // runProbe is the ONE place a preflight is sent. Every probe carries the
  // trusted key as its expectation, so the server refuses a different key
  // before any credential goes out; `accept` is the operator's explicit
  // "the device was reinstalled", and the only way a different key replaces
  // the trusted one.
  const runProbe = async (accept = false): Promise<SSHPreflightResult> => {
    const res = await sshPreflight({
      ...connectionBody(),
      ssh_key_id: authMethod === 'key' ? sshKeyId || undefined : undefined,
      expected_host_key_fingerprint: expectedFingerprint(trust),
      accept_new_host_key: accept || undefined,
    })
    setPreflight(res)
    setTrust((prev) => applyPreflightResult(prev, res, accept).trust)
    return res
  }

  const handleTest = async (e: SyntheticEvent) => {
    e.preventDefault()
    setError(null)
    setAdoptError(null)
    setCapRefusal(null)
    setPreflight(null)
    if (!host.trim() || !username.trim()) {
      setError(t('widgets.sidebars.addNode.ssh.hostUsernameRequired'))
      return
    }
    setTesting(true)
    try {
      await runProbe()
    } catch (err: unknown) {
      setError(
        extractApiErrorMessage(
          err,
          t('widgets.sidebars.addNode.ssh.preflightFailed'),
        ),
      )
    } finally {
      setTesting(false)
    }
  }

  const handleAcceptNewHostKey = async () => {
    setError(null)
    setAdoptError(null)
    setCapRefusal(null)
    setAccepting(true)
    try {
      await runProbe(true)
    } catch (err: unknown) {
      setError(
        extractApiErrorMessage(
          err,
          t('widgets.sidebars.addNode.ssh.preflightFailed'),
        ),
      )
    } finally {
      setAccepting(false)
    }
  }

  const handleAdopt = async () => {
    setError(null)
    setAdoptError(null)
    setCapRefusal(null)
    setAdopting(true)
    try {
      // Always re-run the connection test first, so we fail fast with a clear
      // message instead of aborting mid-adoption (the common case: sudo needs a
      // password that was not supplied — install_agent would die on it). It also
      // tells us whether this device is already registered (reuse vs new),
      // checks the device still presents the trusted host key, and returns
      // the ticket the adopt call must present.
      const pf = await runProbe()
      if (pf.host_key_changed) {
        setAdoptError(
          t('widgets.sidebars.addNode.ssh.hostKeyChanged', {
            expected: pf.expected_host_key_fingerprint,
            observed: pf.host_key_fingerprint,
          }),
        )
        return
      }
      if (!pf.reachable) {
        setAdoptError(
          t('widgets.sidebars.addNode.ssh.notReachable', {
            reason:
              pf.error || t('widgets.sidebars.addNode.ssh.connectionFailed'),
          }),
        )
        return
      }
      if (!pf.arch_supported) {
        setAdoptError(
          t('widgets.sidebars.addNode.ssh.unsupportedArchitecture', {
            arch: pf.arch || t('widgets.sidebars.addNode.ssh.unknownArch'),
          }),
        )
        return
      }
      if (pf.sudo_mode === 'none') {
        setAdoptError(t('widgets.sidebars.addNode.ssh.sudoPasswordNeeded'))
        return
      }
      if (pf.sudo_mode === 'bad_password') {
        setAdoptError(t('widgets.sidebars.addNode.ssh.sudoPasswordRejected'))
        return
      }
      // New-node details are only needed when creating a node; when this device
      // is already registered, adoption reuses the existing one.
      const reuse = !!pf.existing_node_id
      if (!reuse && !nodeName.trim()) {
        setAdoptError(t('widgets.sidebars.addNode.ssh.nodeNameRequired'))
        return
      }
      const credentialError = consoleLoginError(
        defaultUsername,
        defaultPassword,
      )
      if (!reuse && credentialError) {
        setAdoptError(credentialError)
        return
      }
      const started = await sshAdopt({
        ...connectionBody(),
        auth_ssh_key_id:
          authMethod === 'key' ? sshKeyId || undefined : undefined,
        // The probe's ticket: the server's own proof that this operator saw
        // this key on this host:port just now. The adoption connects strictly
        // against the key inside it and refuses a different one before any
        // credential is sent. No fallback to a client-held fingerprint: if
        // the ticket is missing the server says so, with the remedy.
        host_key_ticket: pf.host_key_ticket,
        node_name: nodeName.trim(),
        default_username: defaultUsername.trim(),
        default_password: defaultPassword.trim(),
        provision_network_first: networkFirst,
      })
      setJob(started)
    } catch (err: unknown) {
      // Refused before any job existed: the install is at its device cap.
      const cap = deviceCapRefusal(err)
      if (cap) {
        setCapRefusal(cap)
        return
      }
      if (isDeviceCapUnverified(err)) {
        setAdoptError(t('entities.entitlement.deviceCap.unverified'))
        return
      }
      setAdoptError(
        extractApiErrorMessage(
          err,
          t('widgets.sidebars.addNode.ssh.adoptFailed'),
        ),
      )
    } finally {
      setAdopting(false)
    }
  }

  const jobRunning = !!job && !isTerminal(job.status)

  return (
    <div className="flex flex-col gap-5">
      <div className="text-sm text-neutral-4">
        {t('widgets.sidebars.addNode.ssh.intro')}
      </div>

      {error && (
        <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-2 rounded text-sm">
          {error}
        </div>
      )}

      <form className="flex flex-col gap-4" onSubmit={handleTest}>
        <div className="flex gap-3">
          <div className="flex-1">
            <Input
              label={t('widgets.sidebars.addNode.ssh.hostLabel')}
              value={host}
              onChange={(e) => {
                setHost(e.target.value)
                retarget()
              }}
              placeholder={t('widgets.sidebars.addNode.ssh.hostPlaceholder')}
              disabled={jobRunning}
            />
          </div>
          <div className="w-24">
            <Input
              label={t('widgets.sidebars.addNode.ssh.portLabel')}
              value={port}
              onChange={(e) => {
                setPort(e.target.value)
                retarget()
              }}
              disabled={jobRunning}
            />
          </div>
        </div>

        <Input
          label={t('widgets.sidebars.addNode.ssh.usernameLabel')}
          value={username}
          onChange={(e) => {
            setUsername(e.target.value)
            invalidateProbe()
          }}
          placeholder={t('widgets.sidebars.addNode.ssh.usernamePlaceholder')}
          disabled={jobRunning}
        />

        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.addNode.ssh.authLabel')}
          </label>
          <select
            value={authMethod}
            onChange={(e) => {
              setAuthMethod(e.target.value as AuthMethod)
              invalidateProbe()
            }}
            disabled={jobRunning}
            className="w-full bg-neutral-13 rounded px-2.5 py-2.5 text-white outline-none disabled:opacity-50"
          >
            <option value="password">
              {t('widgets.sidebars.addNode.ssh.authPassword')}
            </option>
            <option value="key">
              {t('widgets.sidebars.addNode.ssh.authKey')}
            </option>
            <option value="agent">
              {t('widgets.sidebars.addNode.ssh.authAgent')}
            </option>
          </select>
        </div>

        {authMethod === 'agent' ? (
          <div className="text-xs text-neutral-4 flex flex-col gap-1">
            <span>{t('widgets.sidebars.addNode.ssh.agentNote')}</span>
            <code className="text-neutral-3 bg-neutral-13 rounded px-2 py-1 break-all">
              ssh -o StreamLocalBindUnlink=yes -L 8080:localhost:80 -R
              /path/to/agentsock/agent.sock:$SSH_AUTH_SOCK user@&lt;server&gt;
            </code>
            <span>{t('widgets.sidebars.addNode.ssh.agentTouchNote')}</span>
          </div>
        ) : authMethod === 'password' ? (
          <Input
            type="password"
            label={t('widgets.sidebars.addNode.ssh.passwordLabel')}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="off"
            disabled={jobRunning}
          />
        ) : connectionKeys.length > 0 ? (
          <div className="flex flex-col gap-2">
            <label className="text-sm font-medium text-neutral-7 uppercase">
              {t('widgets.sidebars.addNode.ssh.connectionKeyLabel')}
            </label>
            <select
              value={sshKeyId}
              onChange={(e) => {
                setSSHKeyId(e.target.value)
                invalidateProbe()
              }}
              disabled={jobRunning}
              className="w-full bg-neutral-13 rounded px-2.5 py-2.5 text-white outline-none disabled:opacity-50"
            >
              {connectionKeys.map((k) => (
                <option key={k.id} value={k.id}>
                  {k.alias}
                </option>
              ))}
            </select>
          </div>
        ) : (
          <div className="text-xs text-neutral-4">
            {t('widgets.sidebars.addNode.ssh.noConnectionKeys')}
          </div>
        )}

        <Input
          type="password"
          label={t('widgets.sidebars.addNode.ssh.sudoPasswordLabel')}
          value={sudoPassword}
          onChange={(e) => setSudoPassword(e.target.value)}
          autoComplete="off"
          disabled={jobRunning}
        />

        <Button
          type="submit"
          size="md"
          kind="secondary"
          disabled={testing || accepting || jobRunning}
          grow
        >
          {testing
            ? t('widgets.sidebars.addNode.ssh.testing')
            : t('widgets.sidebars.addNode.ssh.testConnection')}
        </Button>
      </form>

      {preflight && (
        <PreflightResult
          result={preflight}
          trust={trust}
          accepting={accepting}
          onAcceptNewHostKey={jobRunning ? undefined : handleAcceptNewHostKey}
        />
      )}

      {/* Node record + adopt */}
      <div className="border-t border-neutral-12 pt-4 flex flex-col gap-4">
        {reusing ? (
          <div className="text-sm text-neutral-4">
            {t('widgets.sidebars.addNode.ssh.reAdoptingIntro')}{' '}
            <span className="text-neutral-2 font-medium">
              {preflight?.existing_node_name}
            </span>{' '}
            {t('widgets.sidebars.addNode.ssh.reAdoptingSuffix')}
          </div>
        ) : (
          <>
            <div className="text-sm font-medium text-neutral-6 uppercase">
              {t('widgets.sidebars.addNode.ssh.newNode')}
            </div>
            <Input
              label={t('widgets.sidebars.addNode.ssh.nodeNameLabel')}
              value={nodeName}
              onChange={(e) => setNodeName(e.target.value)}
              disabled={jobRunning}
            />
            {/* The device already runs an OS and gets the agent during adoption,
                so access config applies on its first report — unlike the SD and
                USB paths, which come up in the recovery bootstrap first. */}
            <ConsoleLoginFields
              timing="on-connect"
              username={defaultUsername}
              password={defaultPassword}
              onUsernameChange={setDefaultUsername}
              onPasswordChange={setDefaultPassword}
              disabled={jobRunning}
            />
          </>
        )}

        <label className="flex items-start gap-2.5 text-sm text-neutral-2 cursor-pointer select-none">
          <input
            type="checkbox"
            checked={networkFirst}
            onChange={(e) => setNetworkFirst(e.target.checked)}
            disabled={jobRunning}
            className="mt-0.5 w-4 h-4 accent-lime-6 cursor-pointer"
          />
          <span>{t('widgets.sidebars.addNode.ssh.networkFirstCheckbox')}</span>
        </label>

        <div className="bg-amber-6/10 border border-amber-6/50 text-amber-4 px-4 py-2 rounded text-xs">
          {t('widgets.sidebars.addNode.ssh.adoptWarning')}
        </div>
        <label className="flex items-center gap-2.5 text-sm text-neutral-2 cursor-pointer select-none">
          <input
            type="checkbox"
            checked={ack}
            onChange={(e) => setAck(e.target.checked)}
            disabled={jobRunning}
            className="w-4 h-4 accent-lime-6 cursor-pointer"
          />
          <span>{t('widgets.sidebars.addNode.ssh.ackReboot')}</span>
        </label>

        {capRefusal && <DeviceCapNotice refusal={capRefusal} />}
        {adoptError && (
          <div
            ref={adoptErrorRef}
            className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-2 rounded text-sm"
          >
            {adoptError}
          </div>
        )}

        <Button
          size="lg"
          onClick={handleAdopt}
          disabled={!ack || adopting || jobRunning}
          grow
        >
          {jobRunning
            ? t('widgets.sidebars.addNode.ssh.adopting')
            : adopting
              ? t('widgets.sidebars.addNode.ssh.starting')
              : t('widgets.sidebars.addNode.ssh.adoptDevice')}
        </Button>
      </div>

      {job && <JobProgress job={job} />}
    </div>
  )
}

const Row = ({
  label,
  ok,
  value,
}: {
  label: string
  ok?: boolean
  value: string
}) => (
  <div className="flex items-center justify-between gap-3 py-1 border-b border-neutral-12 last:border-0">
    <span className="text-neutral-6">{label}</span>
    <span
      className={
        'font-mono text-xs truncate ' +
        (ok === undefined
          ? 'text-neutral-3'
          : ok
            ? 'text-lime-5'
            : 'text-red-9')
      }
    >
      {value}
    </span>
  </div>
)

// HostKeyNote says how the shown key came to be trusted. First contact is
// stated out loud: a key nobody checked is a different thing from a key that
// matched an earlier test.
const HostKeyNote: FC<{ result: SSHPreflightResult }> = ({ result }) => {
  const { t } = useTranslation()
  if (result.host_key_reset) {
    return (
      <div className="text-xs text-amber-4 mt-1">
        {t('widgets.sidebars.addNode.ssh.hostKeyReplaced', {
          previous: result.expected_host_key_fingerprint,
        })}
      </div>
    )
  }
  if (result.first_contact) {
    return (
      <div className="text-xs text-neutral-4 mt-1">
        {t('widgets.sidebars.addNode.ssh.hostKeyFirstContact')}
        <span className="whitespace-nowrap">
          &nbsp;
          <InfoHint
            text={t('widgets.sidebars.addNode.ssh.hostKeyFirstContactHint')}
          />
        </span>
      </div>
    )
  }
  return (
    <div className="text-xs text-neutral-4 mt-1">
      {t('widgets.sidebars.addNode.ssh.hostKeyVerified')}
    </div>
  )
}

type PreflightResultProps = {
  result: SSHPreflightResult
  trust: HostKeyTrust | null
  accepting: boolean
  // Absent while an adoption runs: replacing the trusted key mid-run has no
  // effect on the run and would only confuse the next one.
  onAcceptNewHostKey?: () => void
}

const PreflightResult: FC<PreflightResultProps> = ({
  result,
  trust,
  accepting,
  onAcceptNewHostKey,
}) => {
  const { t } = useTranslation()
  if (result.host_key_changed) {
    return (
      <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-3 rounded text-sm flex flex-col gap-3">
        <div>
          {t('widgets.sidebars.addNode.ssh.hostKeyChanged', {
            expected: result.expected_host_key_fingerprint,
            observed: result.host_key_fingerprint,
          })}
        </div>
        {onAcceptNewHostKey && (
          <Button
            size="md"
            kind="secondary"
            onClick={onAcceptNewHostKey}
            disabled={accepting}
          >
            {accepting
              ? t('widgets.sidebars.addNode.ssh.acceptingNewHostKey')
              : t('widgets.sidebars.addNode.ssh.acceptNewHostKey')}
          </Button>
        )}
      </div>
    )
  }
  if (!result.reachable) {
    return (
      <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-3 rounded text-sm flex flex-col gap-1">
        <div>
          {t('widgets.sidebars.addNode.ssh.notReachable', {
            reason:
              result.error ||
              t('widgets.sidebars.addNode.ssh.connectionFailed'),
          })}
        </div>
        {/* The trusted key survived this failure; say so, since the next
            probe will insist on it. */}
        {trust && (
          <div className="text-xs text-neutral-4 font-mono break-all">
            {t('widgets.sidebars.addNode.ssh.hostKeyStillTrusted', {
              fingerprint: trust.fingerprint,
            })}
          </div>
        )}
      </div>
    )
  }
  return (
    <div className="bg-neutral-12 rounded px-4 py-3 text-sm">
      <div className="text-lime-5 font-bold mb-2">
        {t('widgets.sidebars.addNode.ssh.reachable')}
      </div>
      <Row
        label={t('widgets.sidebars.addNode.ssh.rowUser')}
        value={result.user || '—'}
      />
      <Row
        label={t('widgets.sidebars.addNode.ssh.rowArchitecture')}
        ok={result.arch_supported}
        value={`${result.arch || '—'}${result.arch_supported ? '' : t('widgets.sidebars.addNode.ssh.unsupportedSuffix')}`}
      />
      <Row
        label={t('widgets.sidebars.addNode.ssh.rowSudo')}
        ok={['root', 'nopasswd', 'password'].includes(result.sudo_mode)}
        value={sudoLabel(t, result.sudo_mode) || result.sudo_mode}
      />
      {result.cdn_checked && (
        <Row
          label={t('widgets.sidebars.addNode.ssh.rowAgentCdn')}
          ok={result.cdn_reachable}
          value={
            result.cdn_reachable
              ? t('widgets.sidebars.addNode.ssh.cdnReachable')
              : t('widgets.sidebars.addNode.ssh.cdnUnreachable')
          }
        />
      )}
      <Row
        label={t('widgets.sidebars.addNode.ssh.rowHostKey')}
        value={result.host_key_fingerprint || '—'}
      />
      <HostKeyNote result={result} />
      {result.notes && (
        <div className="text-xs text-amber-4 mt-2">{result.notes}</div>
      )}
      {result.existing_node_name && (
        <div className="mt-2 bg-amber-6/10 border border-amber-6/50 text-amber-4 px-3 py-2 rounded text-xs">
          {t('widgets.sidebars.addNode.ssh.alreadyRegisteredIntro')}{' '}
          <span className="text-neutral-2 font-medium">
            {result.existing_node_name}
          </span>
          {result.existing_node_status
            ? ` (${result.existing_node_status})`
            : ''}
          {t('widgets.sidebars.addNode.ssh.alreadyRegisteredSuffix')}
        </div>
      )}
    </div>
  )
}

const STEP_ICON: Record<string, string> = {
  pending: '○',
  running: '◌',
  completed: '●',
  failed: '✕',
  skipped: '–',
}
const STEP_COLOR: Record<string, string> = {
  pending: 'text-neutral-7',
  running: 'text-amber-4',
  completed: 'text-lime-5',
  failed: 'text-red-9',
  skipped: 'text-neutral-6',
}

const JobProgress: FC<{ job: AdoptionJob }> = ({ job }) => {
  const { t } = useTranslation()
  return (
    <div className="bg-neutral-12 rounded px-4 py-3 text-sm flex flex-col gap-1">
      <div className="flex items-center justify-between mb-1">
        <span className="font-bold uppercase text-neutral-4">
          {t('widgets.sidebars.addNode.ssh.adoption')}
        </span>
        <span
          className={
            STEP_COLOR[
              job.status === 'running'
                ? 'running'
                : job.status === 'completed'
                  ? 'completed'
                  : job.status === 'failed'
                    ? 'failed'
                    : 'pending'
            ]
          }
        >
          {job.status}
        </span>
      </div>
      {job.steps.map((s) => (
        <div key={s.key} className="flex items-start gap-2 py-0.5">
          <span className={STEP_COLOR[s.status]}>
            {STEP_ICON[s.status] || '○'}
          </span>
          <div className="flex-1 min-w-0">
            <div className="text-neutral-3">{s.label}</div>
            {s.message && (
              <div className="text-xs text-neutral-6 truncate">{s.message}</div>
            )}
          </div>
        </div>
      ))}
      {job.error && <div className="text-xs text-red-9 mt-1">{job.error}</div>}
    </div>
  )
}
