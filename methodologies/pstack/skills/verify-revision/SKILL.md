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
   the command, the endpoint, the script. Read the diff against the base.
   When the Issue says to preserve existing behaviour or gives compatibility
   requirements, a baseline comparison is mandatory:
   - Pin the baseline SHA named by the Issue, or otherwise the merge-base of
     this head and the PR base branch; record that SHA and the head SHA.
   - Use a separate temporary worktree at that baseline commit. Run the same
     checks with identical inputs, commands and environment on both commits,
     covering each compatibility requirement. Keep baseline artifacts separate
     from head artifacts so each command exercises the intended revision.
   - Record the commands and both results, including relevant output, exit
     codes and side effects. If the comparison cannot run, attest `blocked`
     with the missing evidence rather than claim compatibility.
4. **Read the diff adversarially** for correctness, security, data loss,
   concurrency and missed cases. Check every explicit task requirement and
   applicable standard, including RFC MUST requirements. A demonstrated
   violation is `fail`, even if the changed code otherwise works. A behaviour
   change the Issue did not allow is `fail`, never `pass_notes`, even when all
   new tests pass. For example,
   if the task requires rejecting invalid tokens and the endpoint accepts one,
   attest `fail`. A clearer local name in otherwise conforming code is at most
   `pass_notes`. A finding needs a concrete location and evidence; "I would
   have done it differently" is not a finding.
5. **Attest once** with `attest`: `pr`, `sha` (your exact commit), `result`,
   `summary`, and `findings` (severity critical / warning / nit, location,
   finding, evidence).
6. **Hand back.** On `fail` or `pass_notes`, `send` the findings to
   `forager:<parentId>` (see `get_forager` for your own id).
7. **Finish.** Call `finish_task` with a one-line summary, then end your turn.

## Compatibility example

An Issue asks to simplify an importer's error handling while preserving its
malformed-input behaviour. Run the same malformed input on the pinned
baseline and head:

- Baseline exits 1 with a diagnostic; head exits 2 with a shorter diagnostic.
  The Issue did not allow those changes: attest `fail`, even if the head's
  tests expect the new result.
- Baseline and head both exit 1 with the same required diagnostic and side
  effects. This compatibility check passes. With all other requirements
  proven, attest `pass`, or `pass_notes` for an unrelated non-blocking naming
  suggestion. If the Issue explicitly allows a changed diagnostic, judge it
  against that allowance while still preserving every other required behaviour.
