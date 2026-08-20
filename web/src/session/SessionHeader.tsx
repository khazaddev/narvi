// SessionHeader.tsx -- the session workspace's own `.sess-head` (title,
// repo/branch, source tag, status chip, cost) -- decision 31's own
// "source stays attached to the session" applies here too (the `.srctag`
// next to the title), alongside decision 1's status chip.
import type { Session } from '@narvi/contracts/rest-dtos'

import { deriveStatusChip } from './sessionStatus'
import { SourceIcon } from './SourceIcon'
import type { TimelineModel } from './timelineModel'

const SOURCE_LABELS: Record<Session['spawnSource'], string> = {
  web: 'web',
  slack: 'Slack',
  linear: 'Linear',
  github: 'GitHub',
}

export function SessionHeader({ session, model }: { session: Session; model: TimelineModel }) {
  const chip = deriveStatusChip(session)
  const title = model.latestTitle ?? session.title ?? '(untitled session)'
  const primaryRepo = session.repos[0]

  let totalCost = 0
  let toolCalls = 0
  let hasCost = false
  for (const turn of model.turns) {
    for (const step of turn.steps) {
      toolCalls += step.toolCalls.length
      if (step.cost?.usd != null) {
        hasCost = true
        totalCost += step.cost.usd
      }
    }
  }

  return (
    <header className="sess-head">
      <span className="title">{title}</span>
      {primaryRepo && (
        <span className="repo">
          {primaryRepo.name}
          {primaryRepo.branch ? ` · ${primaryRepo.branch}` : ''}
        </span>
      )}
      <span className="srctag">
        <SourceIcon source={session.spawnSource} />
        {SOURCE_LABELS[session.spawnSource]}
      </span>
      <span className={`chip ${chip.tone}`}>
        <span className="dot" />
        {chip.label}
      </span>
      <span className="spacer" />
      {(hasCost || toolCalls > 0) && (
        <span className="cost">
          {hasCost ? `$${totalCost.toFixed(2)} · ` : ''}
          {toolCalls} tool call{toolCalls === 1 ? '' : 's'}
        </span>
      )}
    </header>
  )
}
