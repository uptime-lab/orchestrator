import { FC } from 'react'
import { useTranslation } from 'react-i18next'

import { useNodeMetrics } from '@/entities/node/model'

import { InfoRow, SectionDivider } from './infoRow.tsx'

const GB = 1024 * 1024 * 1024

// Sparkline draws a small polyline of recent values normalized to [min,max].
const Sparkline: FC<{ values: number[]; max?: number }> = ({ values, max }) => {
  if (values.length < 2) {
    return <div className="h-6" />
  }
  const w = 120
  const h = 24
  const hi = max ?? Math.max(...values, 1)
  const step = w / (values.length - 1)
  const points = values
    .map((v, i) => {
      const y = h - (Math.min(v, hi) / hi) * h
      return `${(i * step).toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')
  return (
    <svg width={w} height={h} className="text-lime-6">
      <polyline
        points={points}
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinejoin="round"
        strokeLinecap="round"
      />
    </svg>
  )
}

// A metric renders as an InfoRow so its label/value columns line up with — and
// use the same font sizes as — the Configuration table above it.
const Metric: FC<{
  label: string
  value: string
  spark?: number[]
  sparkMax?: number
}> = ({ label, value, spark, sparkMax }) => (
  <InfoRow label={label}>
    <div className="flex items-center justify-between gap-3">
      {spark ? <Sparkline values={spark} max={sparkMax} /> : <span />}
      <span className="tabular-nums text-right">{value}</span>
    </div>
  </InfoRow>
)

export const MetricsPanel: FC<{ nodeId: string; online: boolean }> = ({
  nodeId,
  online,
}) => {
  const { t } = useTranslation()
  const history = useNodeMetrics(nodeId)
  const latest = history[history.length - 1]

  if (!latest) {
    return (
      <div className="flex flex-col gap-2">
        <SectionDivider label={t('widgets.sidebars.node.metricsPanel.title')} />
        <div className="text-sm text-neutral-6">
          {online
            ? t('widgets.sidebars.node.metricsPanel.waiting')
            : t('widgets.sidebars.node.metricsPanel.offline')}
        </div>
      </div>
    )
  }

  const cpuHistory = history.map((s) => s.cpu_percent)
  const memPct =
    latest.mem_total_bytes > 0
      ? (latest.mem_used_bytes / latest.mem_total_bytes) * 100
      : 0
  const memHistory = history.map((s) =>
    s.mem_total_bytes > 0 ? (s.mem_used_bytes / s.mem_total_bytes) * 100 : 0,
  )
  const hottest =
    latest.thermal && latest.thermal.length > 0
      ? Math.max(...latest.thermal.map((z) => z.temp_celsius))
      : null

  return (
    <div className="flex flex-col gap-2">
      <SectionDivider label={t('widgets.sidebars.node.metricsPanel.title')} />
      <Metric
        label={t('widgets.sidebars.node.metricsPanel.cpu')}
        value={`${latest.cpu_percent.toFixed(0)}%`}
        spark={cpuHistory}
        sparkMax={100}
      />
      <Metric
        label={t('widgets.sidebars.node.metricsPanel.memory')}
        value={`${memPct.toFixed(0)}% · ${(latest.mem_used_bytes / GB).toFixed(1)}/${(latest.mem_total_bytes / GB).toFixed(1)} GB`}
        spark={memHistory}
        sparkMax={100}
      />
      <Metric
        label={t('widgets.sidebars.node.metricsPanel.load')}
        value={`${latest.load_avg_1.toFixed(2)} · ${latest.load_avg_5.toFixed(2)} · ${latest.load_avg_15.toFixed(2)}`}
      />
      {hottest !== null && (
        <Metric
          label={t('widgets.sidebars.node.metricsPanel.temperature')}
          value={`${hottest.toFixed(1)} °C`}
        />
      )}
    </div>
  )
}
