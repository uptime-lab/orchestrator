import { createFileRoute } from '@tanstack/react-router'

import { CredentialsPage } from '@/pages/credentials'

import { ousModel } from '@/entities/ou'
import { sshKeysModel } from '@/entities/sshKey'

export type CredentialsTab = 'ssh-keys' | 'ca' | 'users'

const isCredentialsTab = (v: unknown): v is CredentialsTab =>
  v === 'ssh-keys' || v === 'ca' || v === 'users'

export const Route = createFileRoute('/_mainLayout/credentials')({
  validateSearch: (search: Record<string, unknown>) => {
    const tab: CredentialsTab = isCredentialsTab(search.tab)
      ? search.tab
      : 'ssh-keys'
    const ou =
      typeof search.ou === 'string' && search.ou ? search.ou : undefined
    const sidebar = search.sidebar

    if (
      (sidebar === 'sshKeyDetails' || sidebar === 'editSSHKey') &&
      typeof search.keyId === 'string'
    ) {
      return { tab, ou, sidebar, keyId: search.keyId }
    }
    if (sidebar === 'createSSHKey') {
      return { tab, ou, sidebar: 'createSSHKey' as const }
    }
    if (sidebar === 'createAccessUser') {
      return { tab, ou, sidebar: 'createAccessUser' as const }
    }
    if (sidebar === 'createCACert') {
      return { tab, ou, sidebar: 'createCACert' as const }
    }
    if (sidebar === 'accessUserDetails' && typeof search.entryId === 'string') {
      return {
        tab,
        ou,
        sidebar: 'accessUserDetails' as const,
        entryId: search.entryId,
      }
    }
    // OU sidebars reachable from the rail's edit mode.
    return { tab, ou, sidebar: undefined }
  },
  component: () => <CredentialsPage />,
  // allSettled for the same reason as the secrets loader: ssh-key inventory and
  // the OU tree are separate grants, and one refusal must not replace the page.
  loader: async () => {
    await Promise.allSettled([
      sshKeysModel.fetchSSHKeysFx(),
      ousModel.fetchOUsFx(),
    ])
  },
})
