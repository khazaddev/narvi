// SignedInGreeting (Step 81) is the sign-in view's own "already signed
// in" headline -- split into its own presentational, hook-free component
// (rather than inlined in routes/sign-in.tsx) specifically so it has a
// direct render test with no QueryClientProvider/RouterProvider needed
// (__tests__/signedInGreeting.test.tsx).
//
// member.displayName and member.email are BOTH third-party-controlled:
// displayName is sourced from the signing-in user's own GitHub profile
// `name` field (internal/adapters/inbound/auth/callback.go's own
// githubUser.Name -- entirely GitHub-user-controlled, never validated
// against a character set by this codebase), and email, while a real
// email address, is likewise supplied by the provider, not typed by
// Narvi. Both are rendered via plain JSX text interpolation, which React
// escapes by construction -- this file has no dangerouslySetInnerHTML,
// and no other library renders markup on its behalf.
import type { Member } from '@narvi/contracts/rest-dtos'

export interface SignedInGreetingProps {
  member: Member
}

export function SignedInGreeting({ member }: SignedInGreetingProps) {
  return (
    <div className="signin-notice signin-notice-ok">
      <p>
        Signed in as <strong>{member.displayName}</strong> ({member.email})
      </p>
    </div>
  )
}
