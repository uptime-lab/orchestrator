import { Link } from '@tanstack/react-router'
import { t } from 'i18next'
import { FC, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { apiAccess, type AccessEntry } from '@/entities/access'

import { Input } from '@/shared/ui/input'

// ConsoleLoginFields is the credential half of every Add Node tab.
//
// It replaced a required "Default Username" + "Default Password" pair, which
// read like the way you get into the node and was not: the platform never logs
// into a device over SSH (the agent dials out over gRPC), and the users a person
// actually logs in as come from Credentials → Users, applied by the agent once
// the node connects.
//
// What is left is genuinely optional and genuinely different: a local account
// for the physical console, for when the network or the agent is down and SSH —
// key or not — is not an option.
//
// It deliberately does NOT offer a picker over Credentials users. Picking one
// here would create that account through the provisioning path, which does not
// put it in the `orchestrator-managed` group, and the access reconcile then
// skips the name as an unmanaged pre-existing user. The result would be an
// account Credentials shows but cannot manage.
// When any of this actually takes effect depends on what the device boots into,
// and saying "when this node connects" everywhere was wrong. A device adopted
// with the one-command install is already running its own OS with the agent on
// it, so access config applies on the next report. A device that boots an SD or
// USB bundle comes up in the RECOVERY BOOTSTRAP — a Buildroot initramfs with no
// persistent root, no groupadd and no systemd units — where apply_access_config
// is held back on purpose (node_command.RequiresManagedOS; the dispatcher parks
// it while current_boot_device is "http"). Nothing about users happens there
// until an OS is installed and the node reports running it.
export type AccessTiming = 'on-connect' | 'after-os-install'

type Props = {
  username: string
  password: string
  onUsernameChange: (v: string) => void
  onPasswordChange: (v: string) => void
  timing: AccessTiming
  disabled?: boolean
}

export const ConsoleLoginFields: FC<Props> = ({
  username,
  password,
  onUsernameChange,
  onPasswordChange,
  timing,
  disabled,
}) => {
  const { t } = useTranslation()
  const afterInstall = timing === 'after-os-install'
  const [fleetUsers, setFleetUsers] = useState<string[] | null>(null)
  // Collapsed by default — it is the exception, not the thing most people came
  // here to fill in. Starts open when a value is already present so a filled
  // field can never hide behind a closed row (a re-render after a failed submit
  // would otherwise swallow what the operator typed).
  const [open, setOpen] = useState(() => Boolean(username || password))

  useEffect(() => {
    // Account-wide entries are the ones that reach a node no matter which unit
    // it lands in, so they are the honest answer to "who will be able to log
    // into this node" before the node exists. Best-effort: this is context, and
    // a failed lookup must not block creating a node.
    apiAccess
      .getAccessEntries({ kind: 'account' })
      .then((entries: AccessEntry[]) =>
        setFleetUsers(
          entries
            .filter((e) => e.type === 'user' && e.username)
            .map((e) => e.username as string),
        ),
      )
      .catch(() => setFleetUsers([]))
  }, [])

  return (
    <div className="flex flex-col gap-4">
      <div className="rounded border border-neutral-11 bg-neutral-13/60 px-3 py-2.5 text-xs text-neutral-4">
        {fleetUsers === null ? (
          t('widgets.sidebars.addNode.consoleLogin.checkingUsers')
        ) : fleetUsers.length > 0 ? (
          <>
            {t('widgets.sidebars.addNode.consoleLogin.usersFromCredentials')}{' '}
            <span className="text-neutral-2">{fleetUsers.join(', ')}</span>
            {afterInstall
              ? t('widgets.sidebars.addNode.consoleLogin.appliedAfterInstall')
              : t(
                  'widgets.sidebars.addNode.consoleLogin.appliedOnConnect',
                )}{' '}
            {t('widgets.sidebars.addNode.consoleLogin.manageThemIn')}{' '}
            <Link
              to="/credentials"
              search={{ tab: 'users', ou: undefined, sidebar: undefined }}
              className="text-lime-6 underline"
            >
              {t('widgets.sidebars.addNode.consoleLogin.credentialsLink')}
            </Link>
            .
          </>
        ) : (
          <>
            {t('widgets.sidebars.addNode.consoleLogin.noFleetUsers', {
              osSuffix: afterInstall
                ? t(
                    'widgets.sidebars.addNode.consoleLogin.noFleetUsersOsSuffix',
                  )
                : '',
            })}{' '}
            <Link
              to="/credentials"
              search={{ tab: 'users', ou: undefined, sidebar: undefined }}
              className="text-lime-6 underline"
            >
              {t('widgets.sidebars.addNode.consoleLogin.credentialsUsersLink')}
            </Link>{' '}
            {t('widgets.sidebars.addNode.consoleLogin.orScopeExisting')}
          </>
        )}
      </div>

      <div className="flex flex-col gap-3">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          className="flex items-center gap-2 text-left text-sm font-medium uppercase text-neutral-7 transition-colors hover:text-neutral-4"
        >
          <span
            className={`text-xs transition-transform ${open ? 'rotate-90' : ''}`}
            aria-hidden
          >
            ▶
          </span>
          {t('widgets.sidebars.addNode.consoleLogin.toggle')}
        </button>

        {open && (
          <>
            <div className="text-xs text-neutral-4">
              {t('widgets.sidebars.addNode.consoleLogin.descriptionPrefix')}{' '}
              <span className="text-neutral-2">
                {t('widgets.sidebars.addNode.consoleLogin.descriptionNot')}
              </span>{' '}
              {t('widgets.sidebars.addNode.consoleLogin.descriptionSuffix', {
                timingSentence: afterInstall
                  ? t(
                      'widgets.sidebars.addNode.consoleLogin.createdAfterInstall',
                    )
                  : t(
                      'widgets.sidebars.addNode.consoleLogin.createdOnProvision',
                    ),
              })}
            </div>
            <Input
              id="default_username"
              name="default_username"
              type="text"
              label={t('widgets.sidebars.addNode.consoleLogin.usernameLabel')}
              placeholder={t(
                'widgets.sidebars.addNode.consoleLogin.usernamePlaceholder',
              )}
              value={username}
              onChange={(e) => onUsernameChange(e.target.value)}
              autoComplete="off"
              disabled={disabled}
            />
            <Input
              id="default_password"
              name="default_password"
              type="password"
              label={t('widgets.sidebars.addNode.consoleLogin.passwordLabel')}
              placeholder={t(
                'widgets.sidebars.addNode.consoleLogin.passwordPlaceholder',
              )}
              value={password}
              onChange={(e) => onPasswordChange(e.target.value)}
              autoComplete="off"
              disabled={disabled}
            />
          </>
        )}
      </div>
    </div>
  )
}

// consoleLoginError validates the one incoherent combination: a password with
// nobody to own it. The backend refuses the same pair.
export const consoleLoginError = (
  username: string,
  password: string,
): string | null =>
  !username.trim() && password.trim()
    ? t('widgets.sidebars.addNode.consoleLogin.passwordNeedsUsername')
    : null
