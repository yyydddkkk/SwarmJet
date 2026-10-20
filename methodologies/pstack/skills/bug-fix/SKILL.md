---
name: bug-fix
description: Use for a defect, regression or failing test. Reproduce first, find the root cause with evidence, fix minimally, prove the repro now passes.
---

# Bug fix

Be scientific: every shipped line traces to runtime evidence. A change that
"might help" is a hypothesis, not a fix, and it does not ship.

1. **Reproduce it yourself** in the Cell: a failing test, a command, a script.
   If it will not reproduce, tighten the conditions or add logging until it
   does. Write down the exact command and its output.
2. **Find the cause by elimination.** List candidate causes, then rule them
   out one by one with evidence (logs, a debugger, a smaller input). Read the
   git history of the code for regressions. Do not guess.
3. **Fix the root cause** with the smallest change the evidence justifies.
   Revert anything a refuted hypothesis motivated.
4. **Prove it on the same path.** The original repro now passes. A unit test
   shows branch behaviour, not that the bug is gone; run the real path too.
5. **Commit the failing test first, then the fix**, when there is a cheap test
   path.
6. End the turn with the briefing: what was broken, root cause, fix, and the
   failing-then-passing output.
