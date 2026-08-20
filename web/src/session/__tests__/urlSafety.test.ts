import { describe, expect, it } from 'vitest'

import { isSafeHref } from '../urlSafety'

describe('isSafeHref', () => {
  it('accepts an absolute https URL (a PR/preview artifact link)', () => {
    expect(isSafeHref('https://github.com/acme/example/pull/1204')).toBe(true)
  })

  it('accepts an absolute http URL', () => {
    expect(isSafeHref('http://preview.example.invalid/')).toBe(true)
  })

  it('accepts a same-origin relative path (an upload artifact link)', () => {
    expect(isSafeHref('/api/sessions/abc-123/uploads/def-456/content')).toBe(true)
  })

  it('rejects a javascript: URL -- the exact attack this guard exists for', () => {
    expect(isSafeHref('javascript:alert(1)')).toBe(false)
  })

  it('rejects case/whitespace obfuscated javascript: variants', () => {
    expect(isSafeHref('JaVaScRiPt:alert(1)')).toBe(false)
    expect(isSafeHref('   javascript:alert(1)')).toBe(false)
    expect(isSafeHref('java\tscript:alert(1)')).toBe(false)
  })

  it('rejects a data: URL', () => {
    expect(isSafeHref('data:text/html,<script>alert(1)</script>')).toBe(false)
  })

  it('rejects a vbscript: URL', () => {
    expect(isSafeHref('vbscript:msgbox(1)')).toBe(false)
  })

  it('rejects a protocol-relative "//evil.example" -- a browser resolves this against a DIFFERENT origin despite looking relative', () => {
    expect(isSafeHref('//evil.example/steal')).toBe(false)
  })

  it('rejects an empty string', () => {
    expect(isSafeHref('')).toBe(false)
  })

  it('rejects a malformed string that fails to parse as any URL', () => {
    expect(isSafeHref('ht!tp://[not a url')).toBe(false)
  })

  it('rejects a bare "file:" URL', () => {
    expect(isSafeHref('file:///etc/passwd')).toBe(false)
  })

  // Named mutation-test target: weakening the scheme check to a substring
  // test (`raw.includes('http')`) instead of parsing with `new URL` and
  // reading back `.protocol` must make THIS test fail -- verified
  // manually during this Step's own verification pass (temporarily
  // replacing isSafeHref's body with `return raw.includes('http')`, which
  // makes this test fail because "javascript:alert(1)//http" contains
  // "http" while still being a javascript: URL), then reverted
  // byte-identical. See this Step's own PR description for the exact
  // before/after.
  it('rejects a javascript: URL that also contains the substring "http" (proves this is a real scheme check, not a substring match)', () => {
    expect(isSafeHref('javascript:alert(1)//http')).toBe(false)
  })
})
