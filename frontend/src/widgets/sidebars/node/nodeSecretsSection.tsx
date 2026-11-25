import { FC } from 'react'
import { useTranslation } from 'react-i18next'

import { SecretsAttachmentSection } from '@/widgets/secretsAttachment'

// Secrets attached directly to a node. OU-inherited secrets also apply and are
// surfaced via the OU; the section shows a read-only hint about that.
//
// canManage comes from the caller, which knows the node and the caller's
// grants.
export const NodeSecretsSection: FC<{ nodeId: string; canManage: boolean }> = ({
  nodeId,
  canManage,
}) => {
  const { t } = useTranslation()
  return (
    <div className="flex flex-col gap-3">
      <SecretsAttachmentSection
        scope={{ kind: 'node', nodeId }}
        canManage={canManage}
      />
      {/* Mirrors the Attributes footnote: explains what a secret is in the node
          context and when to reach for Attributes instead. */}
      <p className="text-xxs text-neutral-6 leading-relaxed">
        {t('widgets.sidebars.node.nodeSecretsSection.footnoteIntro')}{' '}
        <span className="font-mono text-neutral-4">{'{{secrets.<name>}}'}</span>
        {t('widgets.sidebars.node.nodeSecretsSection.footnoteMiddle')}{' '}
        <span className="text-neutral-4">
          {t('widgets.sidebars.node.nodeSecretsSection.footnoteAttributes')}
        </span>
        {t('widgets.sidebars.node.nodeSecretsSection.footnoteSuffix')}
      </p>
    </div>
  )
}
