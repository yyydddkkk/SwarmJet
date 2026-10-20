---
name: refactoring
description: Use for a behaviour-preserving change to structure. Pin current behaviour, change in small steps, prove nothing observable changed.
---

# Refactoring

1. **Pin the behaviour.** Find or write tests that capture what users of the
   code observe. Run them and keep the output.
2. **Subtract before you add.** Remove dead code and redundant layers first.
3. **Migrate callers, then delete the old API** in the same change. Do not
   leave compatibility shims behind.
4. **Change in small steps**, running the pinned tests after each step and
   committing when green.
5. **Prove it**: the pinned tests pass unchanged, and the real entry points
   still behave the same.
6. End the turn with the briefing: what moved, what was deleted, how you
   proved behaviour is unchanged.
