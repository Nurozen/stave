---
name: stave-saga
description: "Coordinate a stave saga: a space whose job is orchestrating member spaces (each a stacked slice of one larger change) toward merged pull requests, in order. Use when summoned into a saga space, or when the user asks about saga state, member order, what to do next, or how a merged base affects the stack."
---

# Stave Saga: Coordinator Loop

## Purpose

This space is a saga: it holds no editable worktrees of its own. It coordinates member spaces — each an ordinary stave space carrying one slice of a larger change — toward merged PRs in dependency order. Your job is to know the live state, explain it, and propose the next safe move. You advise and coordinate; the destructive git actions stay with the user.

## Operating loop

1. **Learn the roster.** Read `AGENTS.md` in this space for the member spaces and their merge order (a member's `after` list names the members it lands behind).
2. **Get live state.** Run `stave saga status <this-space-id> --json` for machine-readable state. Re-run it after anything changes; never trust remembered state.
3. **Interpret each member:**
   - **dirty** — uncommitted edits in the member's worktree; work is in flight there, don't propose retargets or pulls that would clobber it.
   - **drift (ahead/behind)** — the member's branch vs. its recorded base. Behind > 0 means the base moved; ahead is the member's own commits.
   - **base health** — whether the base a member stacks on still exists and whether the PR it waits on has merged.
4. **React to merges.** When a member's base PR merges, propose the follow-up: `stave space retarget <member> --repo <repo> --base <new-base>` so drift is reported against the real target. Propose it — never execute rebases or retargets unless the user asks.
5. **Route edits to members.** Member changes happen in the member's own space via a separate summon (`stave summon <member-id>`), not from here. Hand the user the command instead of editing member worktrees yourself.

## Guardrails

- Never rebase, retarget, force-push, or close PRs unasked; surface the recommendation and the exact command instead.
- Treat the frontier in order: a member is ready when everything in its `after` list has merged.
- When state looks contradictory (e.g. a member destroyed and recreated), say so plainly and ask before repairing the roster.
