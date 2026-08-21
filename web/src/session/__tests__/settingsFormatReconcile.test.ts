// reconcileServerBackedField is the decision behind every editable control on
// the repository-settings screen. Each card used to seed its inputs with
// useState(settings.X), captured once per mount, so a later refetch left the
// control showing a stale value while the read-only summary above it updated
// -- and pressing Save wrote the stale value back over the newer one.
//
// The naive fix (remount on every refetch) trades that for wiping in-progress
// typing, which is why the untouched/touched split below is the whole point.
import { describe, expect, it } from 'vitest'

import { reconcileServerBackedField, serverBackedValuesEqual } from '../repoSettingsFormat'

describe('reconcileServerBackedField', () => {
  it('adopts a new server value while the field is untouched', () => {
    const prev = { value: '25', server: '25', dirty: false }
    expect(reconcileServerBackedField(prev, '77')).toEqual({ value: '77', server: '77', dirty: false })
  })

  it('keeps the operator edit when the server moved somewhere else', () => {
    const prev = { value: '99', server: '25', dirty: true }
    expect(reconcileServerBackedField(prev, '77')).toEqual({ value: '99', server: '77', dirty: true })
  })

  it('goes clean when the server catches up to exactly what was typed', () => {
    const prev = { value: '99', server: '25', dirty: true }
    expect(reconcileServerBackedField(prev, '99')).toEqual({ value: '99', server: '99', dirty: false })
  })

  it('compares array values by content, not identity', () => {
    const prev = { value: ['auth'], server: ['auth'], dirty: false }
    // a fresh array with the same contents must not be treated as a change
    expect(reconcileServerBackedField(prev, ['auth'])).toEqual({ value: ['auth'], server: ['auth'], dirty: false })
    // ...and a genuinely different list is adopted
    expect(reconcileServerBackedField(prev, ['auth', 'migrations'])).toEqual({ value: ['auth', 'migrations'], server: ['auth', 'migrations'], dirty: false })
  })

  it('adopts null-shaped values (blank meaning "engine default") like any other', () => {
    const prev = { value: '30', server: '30', dirty: false }
    expect(reconcileServerBackedField(prev, '')).toEqual({ value: '', server: '', dirty: false })
  })
})

describe('serverBackedValuesEqual', () => {
  it('treats structurally equal arrays as equal', () => {
    expect(serverBackedValuesEqual(['a', 'b'], ['a', 'b'])).toBe(true)
    expect(serverBackedValuesEqual(['a', 'b'], ['b', 'a'])).toBe(false)
  })
})
