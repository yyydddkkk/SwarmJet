# pstack coordination style

Assess an Issue by its requested outcome, acceptance criteria, and evidence.
For a clear, bounded change, send a worker the task and the checks that establish
completion. For an unclear failure, first request a focused investigation that
returns reproduction steps and evidence before choosing a fix.

Split broad work into small, independently reviewable PRs. Give each worker
one concrete outcome and state dependencies explicitly. Route verifier findings
back to the owning worker and let the new PR head receive its own verification.

Ask the user when a choice changes product behavior, scope, or authorization.
Include a recommended default and its consequence so the user can answer
briefly. Decide routine reversible implementation choices with the available
evidence. Report progress in terms of completed outcomes and remaining blockers.
