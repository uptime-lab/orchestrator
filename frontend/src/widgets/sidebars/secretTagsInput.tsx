import { FC, useEffect, useMemo } from 'react'

import { TagsInput } from '@/widgets/tagsInput'

import { secretsModel, useSecretTags } from '@/entities/secret'

import { deliverySentence } from './secretTagDelivery'

// Suggested vocabulary shown while the account has no tags of its own.
const STARTER_TAGS = ['node-specific', 'automation', 'external', 'infra']

// Tag input for secret forms: suggestions come from the account's existing
// secret-tag vocabulary (starter set when empty).
export const SecretTagsInput: FC<{
  value: string[]
  onChange: (tags: string[]) => void
  disabled?: boolean
}> = ({ value, onChange, disabled }) => {
  const { tags: accountTags } = useSecretTags()

  useEffect(() => {
    if (accountTags.length === 0) {
      secretsModel.fetchSecretTagsFx()
    }
    // Shared cache; load once when the input mounts.
  }, [])

  const suggestions = useMemo(
    () =>
      accountTags.length > 0
        ? accountTags.map((entry) => entry.tag)
        : STARTER_TAGS,
    [accountTags],
  )

  // What the tags on this secret will DELIVER it to.
  //
  // Tag attachments have dynamic membership: attaching #infra to a unit means
  // every secret carrying #infra reaches that unit, including one tagged later.
  // So adding a tag here is a delivery decision, and the only place that was
  // visible was the Tags tab — on the tag, not at the moment a secret acquires
  // one. The counts come from the same vocabulary the suggestions do, so this
  // costs no extra request.
  const delivery = useMemo(() => {
    const selected = new Set(value.map((tag) => tag.toLowerCase()))
    const delivering = accountTags.filter(
      (entry) =>
        selected.has(entry.tag.toLowerCase()) &&
        (entry.ou_count > 0 || entry.node_count > 0),
    )
    if (delivering.length === 0) {
      return null
    }
    return {
      tags: delivering.map((entry) => entry.tag),
      ous: delivering.reduce((total, entry) => total + entry.ou_count, 0),
      nodes: delivering.reduce((total, entry) => total + entry.node_count, 0),
    }
  }, [value, accountTags])

  return (
    <div className="flex flex-col gap-2">
      <TagsInput
        value={value}
        onChange={onChange}
        suggestions={suggestions}
        disabled={disabled}
      />
      {delivery && (
        <div className="text-xs text-amber-5">{deliverySentence(delivery)}</div>
      )}
    </div>
  )
}
