---
name: verify-revision
description: Use as a verifier. Independently prove or disprove one PR head commit you did not write, then attest a verdict for that exact SHA.
---

# Verify a revision

The author checks what it meant to build; you check what it actually built.
Green CI is an input to your verdict, not the verdict.

1. **Frame.** From your task and `pr_status`: the PR, the head SHA you are
   checked out at, the base branch, what the PR claims to do, and earlier
   verdicts. If `pr_status` shows a newer head than your SHA, attest
   `blocked` ("superseded") and stop.
2. **Build and test.** Run the project's build and the tests that cover the
   changed code. If the repository documents how to verify (AGENTS.md,
   CLAUDE.md, a verify skill), follow it.
3. **Exercise the claim.** Run the changed behaviour the way a user would:
   the command, the endpoint, the script. Compare against the base where it
   helps: `git diff origin/<base>...HEAD` for the change, and
   `git worktree add /tmp/base origin/<base>` when you need to run the old
   behaviour side by side.
4. **Read the diff adversarially** for correctness, security, data loss,
   concurrency and missed cases. A finding needs a concrete location and
   evidence; "I would have done it differently" is not a finding.
5. **Attest once** with `attest`: `pr`, `sha` (your exact commit), `result`,
   `summary`, and `findings` (severity critical / warning / nit, location,
   finding, evidence).
6. **Hand back.** On `fail` or `pass_notes`, `send` the findings to
   `forager:<parentId>` (see `get_forager` for your own id). Then end your
   turn; your work is done.
