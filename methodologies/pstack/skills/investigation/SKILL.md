---
name: investigation
description: Use for a read-only question (how does X work, why is Y like this, is Z safe). Answer with evidence; change no code.
---

# Investigation

1. **Restate the question** in one sentence and what would answer it.
2. **Gather evidence**: read the code path end to end, the git history and
   blame for intent, and run small read-only experiments when behaviour is in
   doubt.
3. **Separate fact from inference.** Cite `file:line`, commits and command
   output for facts; mark guesses as guesses.
4. **Make no commits.** If the answer reveals work to do, describe it as a
   proposed task instead.
5. End the turn with the answer first, then the evidence, then open questions.
