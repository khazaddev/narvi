// SessionHeader.tsx -- the session workspace's own `.sess-head` (title,
// repo/branch, source tag, status chip, cost) -- decision 31's own
// "source stays attached to the session" applies here too (the `.srctag`
// next to the title), alongside decision 1's status chip.
import type { Session } from '@narvi/contracts/rest-dtos'

import type { CostRollup } from './costRollup'
import { formatUsd } from './money'
import { deriveStatusChip } from './sessionStatus'
import { SourceIcon } from './SourceIcon'
import type { TimelineModel } from './timelineModel'

const SOURCE_LABELS: Record<Session['spawnSource'], string> = {
  web: 'web',
  slack: 'Slack',
  linear: 'Linear',
  github: 'GitHub',
}

export function SessionHeader({ session, model, cost }: { session: Session; model: TimelineModel; cost: CostRollup }) {
  const chip = deriveStatusChip(session)
  const title = model.latestTitle ?? session.title ?? '(untitled session)'
  const primaryRepo = session.repos[0]

  // The session total comes from the cost rollup, the SAME value the rail
  // shows, and deliberately not from the timeline model beside it. This
  // header used to sum the timeline's own per-step costs, and costRollup.ts
  // says in its own words why that is the wrong source: the timeline routes
  // every sub-task-tagged step_finish away from the main lane, which is
  // right for rendering a timeline and wrong for a total that must include
  // sub-task spend. The result was two different session totals on the same
  // screen, a few hundred pixels apart, with the smaller one here.
  const totalCost = cost.sessionUsd
  let toolCalls = 0
  for (const turn of model.turns) {
    for (const step of turn.steps) {
      toolCalls += step.toolCalls.length
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
      {(totalCost !== null || toolCalls > 0) && (
        <span className="cost">
          {totalCost !== null ? `${formatUsd(totalCost)} · ` : ''}
          {toolCalls} tool call{toolCalls === 1 ? '' : 's'}
        </span>
      )}
    </header>
  )
}
