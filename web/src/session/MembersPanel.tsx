// MembersPanel.tsx -- Settings -> Members & access (§13.2/§13.3, Step
// 86): role management, linked-identity chips, and the audit log.
//
// # A real shape difference from the mockup, stated here rather than
// # silently reproduced
//
// mockups.html draws a "linear · pending" chip on the SAME row as a
// member's other linked identities. The real DTO shape does not support
// that: ListMembersResponse.pendingLinkPrompts is a FLAT, top-level list
// (PendingLinkPrompt{provider, externalId, expiresAt, createdAt}) with no
// userID field at all -- §13.2's own auto-linking design means a pending
// prompt has not yet been matched to any member (that happens once the
// verified email resolves it), so there is no real "this row's own
// pending identity" to draw. This panel renders pending prompts as their
// own separate list instead of fabricating a per-member association the
// data cannot support. The mockup's own "Resend link" button is likewise
// declined -- members.go exposes no resend endpoint (only list/
// role-change/manual link/unlink); a button with no real action behind
// it would be exactly the "plausible fiction" this Step's own
// instructions rule out.
//
// # Adversarial rendering
//
// Member.displayName/email and every AuditLogEntry field (action,
// resourceType, resourceId, detail) are rendered as plain text only (the
// T component below) or, for `detail` (an opaque per-action JSON blob,
// AuditLogEntry.detail's own doc comment: "Arbitrary per-action JSON
// detail"), through safeJsonPreview -- never markdown-parsed, never
// interpolated into an href.
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import type { AuditLogEntry, Member } from '@narvi/contracts/rest-dtos'

import { listAuditLog, listMembers, updateMemberRole } from '../api/endpoints'
import { ApiError } from '../api/http'
import { auditLogQueryKeys, memberQueryKeys } from '../api/queryKeys'
import { meQueryOptions } from '../auth/session'
import { formatDateTime, identityProviderLabel, roleTone } from './settingsFormat'
import { safeJsonPreview, truncateForDisplay } from './textSafety'

const MAX_FIELD_CHARS = 500
const ROLES = ['admin', 'maintainer', 'member', 'viewer'] as const

function T({ text }: { text: string }) {
  return <>{truncateForDisplay(text, MAX_FIELD_CHARS)}</>
}

/** MemberRow renders one member's own table row -- exported for direct render-safety testing (mirrors AutomationsView.tsx's own AutomationRow precedent): member.displayName is admin-editable free text (github display name, §13.1), never Narvi-validated, and must render as plain text only. */
export function MemberRow({ member, canManage, onShowAudit }: { member: Member; canManage: boolean; onShowAudit: (userId: string) => void }) {
  const queryClient = useQueryClient()
  const roleMutation = useMutation({
    mutationFn: (role: string) => updateMemberRole(member.id, { role }),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: memberQueryKeys.list() }),
  })

  return (
    <tr>
      <td>
        <T text={member.displayName} /> {member.disabled && <span className="chip warn">disabled</span>}
      </td>
      <td>
        {canManage ? (
          <select className="sel-select" value={member.role} disabled={roleMutation.isPending} onChange={(e) => roleMutation.mutate(e.target.value)}>
            {ROLES.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>
        ) : (
          <span className={`chip ${roleTone(member.role)}`}>
            <span className="dot" />
            {member.role}
          </span>
        )}
      </td>
      <td>
        {member.identities.length === 0 && <span style={{ color: 'var(--faint)' }}>none linked</span>}
        {member.identities.map((id) => (
          <span key={id.id} className="idchip ok" style={{ marginRight: 4 }}>
            {identityProviderLabel(id.provider)} ✓
          </span>
        ))}
      </td>
      <td style={{ textAlign: 'right' }}>
        <button type="button" className="btn" style={{ padding: '2px 9px', fontSize: 11 }} onClick={() => onShowAudit(member.id)}>
          Audit log
        </button>
      </td>
    </tr>
  )
}

/** AuditLogRow renders one audit_log row -- exported for direct render-safety testing. action/resourceType/resourceId are server-controlled constants (never model/user free text) but resourceId can carry a caller-typed identifier (e.g. a repo full name) and detail is an OPAQUE per-action JSON blob (AuditLogEntry.detail's own doc comment: "Arbitrary per-action JSON detail") that can legitimately embed anything a request body once carried -- both render through the T/safeJsonPreview plain-text path, never markdown-parsed or interpolated into an href. */
export function AuditLogRow({ entry }: { entry: AuditLogEntry }) {
  return (
    <tr>
      <td>
        <T text={entry.action} />
      </td>
      <td>
        <T text={`${entry.resourceType} · ${entry.resourceId}`} />
      </td>
      <td style={{ fontFamily: 'var(--mono)', fontSize: 'var(--text-xs)', color: 'var(--muted)' }}>
        <T text={safeJsonPreview(entry.detail, 200)} />
      </td>
      <td style={{ fontFamily: 'var(--mono)', fontSize: 'var(--text-xs)', color: 'var(--faint)' }}>{entry.correlationId ? <T text={entry.correlationId} /> : '—'}</td>
      <td style={{ fontFamily: 'var(--mono)', fontSize: 'var(--text-xs)', color: 'var(--faint)' }}>{formatDateTime(entry.createdAt)}</td>
    </tr>
  )
}

function AuditLogSection({ actorUserId, onClear }: { actorUserId: string | null; onClear: () => void }) {
  const query = useQuery({
    queryKey: auditLogQueryKeys.list(),
    queryFn: ({ signal }) => listAuditLog(signal),
    retry: false,
  })

  if (query.isPending) return <p className="rail-empty">Loading audit log…</p>
  if (query.isError && query.error instanceof ApiError && query.error.status === 403) {
    return <p className="notavailable">Audit log is admin-only.</p>
  }
  if (query.isError) return <p className="rail-empty">Couldn't load audit log.</p>

  const entries: AuditLogEntry[] = actorUserId ? query.data.entries.filter((e) => e.actorUserId === actorUserId) : query.data.entries

  return (
    <div>
      {actorUserId && (
        <p className="ph">
          filtered to this member's own actions ·{' '}
          <button type="button" className="btn" style={{ padding: '2px 9px', fontSize: 11 }} onClick={onClear}>
            show all
          </button>
        </p>
      )}
      {entries.length === 0 && <p style={{ color: 'var(--faint)', fontSize: 'var(--text-sm)' }}>No entries.</p>}
      <table className="sectable">
        <thead>
          <tr>
            <th>Action</th>
            <th>Resource</th>
            <th>Detail</th>
            <th>Correlation</th>
            <th>When</th>
          </tr>
        </thead>
        <tbody>
          {entries.map((e) => (
            <AuditLogRow key={e.id} entry={e} />
          ))}
        </tbody>
      </table>
    </div>
  )
}

export function MembersPanel() {
  const meQuery = useQuery(meQueryOptions)
  const isAdmin = meQuery.data?.role === 'admin'
  const [auditFilter, setAuditFilter] = useState<string | null>(null)

  const listQuery = useQuery({
    queryKey: memberQueryKeys.list(),
    queryFn: ({ signal }) => listMembers(signal),
    retry: false,
  })

  const forbidden = listQuery.isError && listQuery.error instanceof ApiError && listQuery.error.status === 403

  return (
    <>
      <div className="panel">
        <h4>Members &amp; access</h4>
        <p className="ph">roles enforced server-side · every state change audited with correlation id</p>
        {forbidden && <p className="notavailable">Members &amp; access is admin-only. Your role cannot view this panel -- enforced server-side (authz.ActionManageMembers), not merely hidden here.</p>}
        {!forbidden && listQuery.isPending && <p className="rail-empty">Loading members…</p>}
        {!forbidden && listQuery.isError && <p className="rail-empty">Couldn't load members.</p>}
        {!forbidden && listQuery.isSuccess && (
          <>
            <table className="sectable">
              <thead>
                <tr>
                  <th>Member</th>
                  <th>Role</th>
                  <th>Linked identities</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {listQuery.data.members.map((m) => (
                  <MemberRow key={m.id} member={m} canManage={isAdmin} onShowAudit={setAuditFilter} />
                ))}
              </tbody>
            </table>
            {listQuery.data.pendingLinkPrompts.length > 0 && (
              <div style={{ marginTop: 12 }}>
                <b style={{ fontSize: 'var(--text-sm)' }}>Pending identity links</b>
                <p className="ph">not yet matched to a member -- resolves automatically once the verified email matches (§13.2)</p>
                {listQuery.data.pendingLinkPrompts.map((p, i) => (
                  <span key={i} className="idchip pend" style={{ marginRight: 6 }}>
                    {identityProviderLabel(p.provider)} · pending (expires {formatDateTime(p.expiresAt)})
                  </span>
                ))}
              </div>
            )}
          </>
        )}
      </div>
      {!forbidden && listQuery.isSuccess && (
        <div className="panel">
          <h4>Audit log</h4>
          <AuditLogSection actorUserId={auditFilter} onClear={() => setAuditFilter(null)} />
        </div>
      )}
    </>
  )
}
