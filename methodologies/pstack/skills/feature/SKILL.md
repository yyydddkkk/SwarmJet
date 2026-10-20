---
name: feature
description: Use for new or changed behaviour. Understand the subsystem, settle the data shape first, build in small verified commits, prove it end to end.
---

# Feature

1. **Understand the subsystem first.** Read how the affected code works today
   and how its callers use it.
2. **Settle the shape before logic.** Name the core data types and the module
   boundary. Prefer a typed model, a table or a state machine over scattered
   conditionals. Make illegal states unrepresentable where cheap.
3. **Plan small units.** Each unit ends in a state you can check. Shared state
   between parts is split before it is serialized.
4. **Build and verify each unit**, committing as you go. Keep edits surgical
   and match the surrounding code.
5. **Prove the behaviour on the real surface**: run the command, call the
   endpoint, drive the flow. "Inconclusive" is not a pass; say so.
6. **Tidy the history** into small ordered commits before you finish.
7. End the turn with the briefing: what you built, what you chose and why
   (alternatives in a short table if there was a real choice), how you proved
   it, open decisions.
