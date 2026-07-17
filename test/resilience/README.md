# test/resilience

Automated replay of the known failure scenarios that are this design's
differentiator (§9.3) — the phase-2 exit gate, not an afterthought. Minimum
set: kill-pod-mid-turn, kill-sandbox-mid-turn, slow boot, late
`execution_complete` reconciliation, concurrent-spawn gen fencing, stale-gen
reconnect rejection, WS-drop ack redelivery, provider-down circuit
breaking, outbox delivery under sustained failure, concurrent-@mention
coalescing, dirty-working-tree relaunch, and zero-downtime rolling deploys.

This directory is populated in **PR-30** (§9.3): the resilience harness and
the 12 scenarios it runs against a real (or provider-faked) stack.
