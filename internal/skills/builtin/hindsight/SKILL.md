---
name: hindsight
description: Review the available session evidence and propose only durable process lessons for human review. Use when the user asks for hindsight, lessons learned, or a session retrospective. Does not redo completed work or automatically accept memory.
disable-model-invocation: true
license: MIT
metadata:
  author: Jeffrey Smith / EfficientStreet; adapted for Prowl
  source: https://github.com/EfficientStreet/hindsight
---

# Hindsight

Run only when the user requests it. Review completed work; do not reopen tasks,
edit project code, rerun checks, or start a fresh-agent retry loop. A clean session
may have nothing worth retaining.

## Review the evidence you actually have

1. Establish coverage. Review available tasks, decisions, corrections, tool
   results, and verified outcomes. If compaction or missing history limits the
   record, state that limitation; do not claim an end-to-end review you could
   not perform.
2. Find root causes, not a chronology. Identify which specific earlier decision
   or missing check caused repeated work. Separate a reasonable failed attempt
   from an unsupported assumption. Infrastructure failures are observations,
   not automatically process mistakes.
3. Keep only reusable lessons. A candidate must change how a future task is
   handled. State the triggering condition, concrete action, reason, and limits.
   Exclude task status, one-off content, secrets, personal diagnoses, and generic
   advice such as “be careful.”
4. Separate observation from inference. A verified fix or confirmed causal
   mechanism can support a scoped lesson. One occurrence is not a universal
   rule. Label unconfirmed hypotheses as watch-items and preserve contrary
   evidence and uncertainty.

## Propose, do not silently install

Use Prowl's existing knowledge workflow, not a second memory directory:

- Search related accepted knowledge with `prowl_agent search` or
  `prowl_agent context search`; recover detail with `context get`. Check scope,
  freshness, and conflicts before proposing anything.
- If a topic already exists and its bundle-relative target is known, use that
  `target` with `learn` to propose an update. Do not create a near-duplicate or
  guess a target. If the target or evidence is unavailable, report the gap.
- Call `learn` only for evidence-backed candidates. Put the lesson, applicable
  repository/environment, supporting observations, uncertainty, and exceptions
  in `memory`; use `context` for the session/source explanation and `evidence`
  for resolvable code anchors such as `internal/example.go#Function`.
- Do not set `learn.skill` during this pass: a managed skill would install the
  procedure before the knowledge proposal has been reviewed. Do not accept,
  overwrite, or delete accepted knowledge on the user's behalf.
- If new evidence conflicts with an accepted note, identify both claims and
  propose the correction for review. Never silently choose the convenient one.

A successful `learn` call creates a proposal in the review inbox. It does **not**
mean the lesson has been accepted or will be applied to later sessions. If the
proposal fails, say it was not saved; do not describe a narrative report as
persistent memory.

## Finish

Report the concrete lessons proposed, where review is needed, watch-items held
back, and any coverage limits. Keep the report shorter than the reviewed work.
If there is no durable lesson, say so without manufacturing a finding.

Adapted from EfficientStreet's Hindsight. Original author credit: Jeffrey Smith.
The upstream MIT copyright and permission notice are included in `LICENSE`.
