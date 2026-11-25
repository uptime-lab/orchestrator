import { useUnit } from 'effector-react'
import { FC, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { nodesModel } from '@/entities/node'
import { $nodes } from '@/entities/node/model'
import { nodeDisplayName } from '@/entities/node/utils.ts'
import { ousModel, useOUs } from '@/entities/ou'
import { apiSecrets, type SecretAttachment } from '@/entities/secret'
import { usePreferHostnames } from '@/entities/user/preferences'

// "Attached to" list shown in the secret detail sidebar: resolves every
// direct attachment of the secret to a named OU or node.
export const SubjectAttachmentTargets: FC<{
  subject: { kind: 'secret'; secretId: string }
}> = ({ subject }) => {
  const { t } = useTranslation()
  const preferHostnames = usePreferHostnames()
  const { ous } = useOUs()
  const nodes = useUnit($nodes)
  const [attachments, setAttachments] = useState<SecretAttachment[] | null>(
    null,
  )
  const [failed, setFailed] = useState(false)

  const subjectKey = subject.secretId

  useEffect(() => {
    if (ous.length === 0) {
      ousModel.fetchOUsFx()
    }
    if (nodes.length === 0) {
      nodesModel.fetchNodesFx()
    }
    setAttachments(null)
    setFailed(false)
    apiSecrets
      .getSecretAttachments(subject.secretId)
      .then((result) => setAttachments(result.attachments))
      .catch(() => setFailed(true))
    // Reload when the subject changes; the name stores are shared caches.
  }, [subjectKey])

  const ouName = (id: string) => ous.find((ou) => ou.id === id)?.name ?? '…'
  const nodeName = (id: string) => {
    const node = nodes.find((n) => n.id === id)
    return node ? nodeDisplayName(node, preferHostnames) || id.slice(0, 8) : '…'
  }

  return (
    <div className="flex flex-col gap-2 border-t border-neutral-11 pt-4">
      <div className="text-sm font-medium text-neutral-7 uppercase">
        {t('widgets.subjectTargets.title')}
      </div>
      {failed ? (
        <div className="text-sm text-red-6">
          {t('widgets.subjectTargets.loadError')}
        </div>
      ) : attachments === null ? (
        <div className="text-sm text-neutral-4">
          {t('widgets.subjectTargets.loading')}
        </div>
      ) : attachments.length === 0 ? (
        <div className="text-sm text-neutral-4">
          {t('widgets.subjectTargets.empty')}
        </div>
      ) : (
        <div className="flex flex-wrap gap-1.5">
          {attachments.map((att) => (
            <span
              key={att.id}
              className="inline-flex items-center gap-1.5 bg-neutral-13 rounded px-2 py-1 text-xs font-mono text-neutral-2"
            >
              <span className="text-neutral-6 uppercase">
                {att.ou_id
                  ? t('widgets.subjectTargets.ou')
                  : t('widgets.subjectTargets.node')}
              </span>
              {att.ou_id ? ouName(att.ou_id) : nodeName(att.node_id ?? '')}
            </span>
          ))}
        </div>
      )}
      <div className="text-xs text-neutral-6">
        {t('widgets.subjectTargets.hint')}
      </div>
    </div>
  )
}
