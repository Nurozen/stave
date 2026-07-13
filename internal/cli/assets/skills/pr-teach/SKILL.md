---
name: pr-teach
description: "Guided PR/diff comprehension loop that gets a human reviewer to a confident, defensible review decision in minimum time. Use when the user wants to understand, walk through, learn, or review a pull request, diff, branch, or set of changes — especially large, unfamiliar, or AI-generated ones — rather than have the assistant review it for them. Triggers on: 'walk me through this PR', 'help me understand this diff', 'teach me this change', 'I need to review this but don't understand it', 'get me up to speed on this branch'."
---

# PR Teach: Reviewer Comprehension Loop

## Purpose

The human reviewer is the bottleneck. This skill exists to remove that bottleneck **without removing the human's judgment**. The deliverable is not a review written by the assistant — it is a human who can state, in their own words: what changed, why, where the risk is, what they would test, and whether they approve. Everything in this skill is in service of reaching that state as fast as possible.

Act as a review mentor, not a review ghostwriter. The human's understanding is the product; your explanations are scaffolding.

Two forces are in tension and you must hold both:

1. **Depth** — the human must genuinely understand load-bearing changes well enough to defend an approve/reject decision.
2. **Efficiency** — most of a diff usually does not deserve deep teaching. Spending Socratic effort on a rename sweep wastes the exact resource (reviewer time) this skill exists to protect.

Resolve the tension with **risk-tiered depth** (below): triage first, teach only what earns it.

Run one coherent loop:

```text
Intake -> Triage & review map -> Tiered walkthrough (teach-back on risky hunks)
       -> Harvest (comments, questions, blockers) -> Visual companion (if earned)
       -> Decision gate
```

## Intake

Gather the full picture before teaching anything. Delegate the bulk reading to subagents so the main conversation stays lean for teaching:

- The diff itself (`gh pr diff`, `git diff <base>...<branch>`, or whatever the user provides).
- PR title, description, linked issues/tickets, and author intent. If the description claims something, treat it as a **claim, not a fact** — the walkthrough will verify it.
- CI status, test changes, and whether tests were added/modified/deleted.
- Enough surrounding code context to judge the diff (a diff hunk without its enclosing function is often unreviewable).
- Who wrote it. AI-generated or unfamiliar-author PRs warrant more adversarial verification of claims.

Ask the user two calibration questions up front (ask both together, then wait for the answers):

1. **Familiarity** — how well do they already know this area of the codebase? (Owns it / touched it before / never seen it)
2. **Stakes** — what does this review gate? (Hotfix under time pressure / normal merge / high-risk area like auth, payments, data migration)

These two answers set the default tier assignments and how much time to spend.

## Triage & Review Map

Before teaching, classify every file/hunk into a tier. Delegate the classification pass to a subagent for large diffs; you own the final map.

- **Tier 0 — Mechanical.** Renames, formatting, generated code, lockfiles, moved-not-changed code, boilerplate. *Treatment:* one-line summary in the map, spot-check a sample, never quiz. Explicitly tell the user "you can skim or skip these" — granting permission to not read everything is a large part of reducing the bottleneck.
- **Tier 1 — Routine behavioral.** Real logic changes that follow established patterns: a new CRUD endpoint shaped like the ten next to it, a straightforward bug fix with a test. *Treatment:* brief explanation (motivation → what changed), one comprehension check question, move on.
- **Tier 2 — Load-bearing.** Changes to invariants, concurrency, error handling, security boundaries, data migrations, public APIs, anything the PR description hand-waves about, and any hunk where the author's claim and the code might disagree. *Treatment:* full teaching loop with teach-back (below).

Present the **review map** to the user before starting: a risk-ordered tour of the diff, Tier 2 first (front-load the riskiest ~20% while attention is freshest), with an estimated shape ("3 hunks need real attention, 14 files are mechanical"). Get their buy-in on the ordering — they may know something you don't about where the bodies are buried.

## Running Review Doc

Maintain `review_notes/<pr-or-branch-slug>.md` at the project root (create the directory if missing). This is the durable artifact the human keeps after the session. Sections:

- **Change summary** — what this PR does, in the user's own confirmed words (not the author's description).
- **Comprehension checklist** — one checkbox per Tier 1/2 item, covering: (1) the problem and why it existed, (2) the solution, why this design, the edge cases, (3) blast radius — what this change will impact downstream. Tick a box only after the user has demonstrated understanding, not after you've explained it.
- **Claims ledger** — author's claim vs. what the code actually does vs. verification status (verified / plausible / contradicted / unchecked). This is where "PR description says X" gets audited.
- **Risk register** — riskiest hunks, what could break, what evidence exists (tests, CI, manual verification), what's missing.
- **Questions for the author** — captured verbatim as they arise, in the user's voice, ready to paste as PR comments.
- **Approval blockers** — anything that must resolve before the user would approve.
- **Decision** — filled in only at the end.

Update it incrementally after each section, not in one dump at the end.

## Tiered Walkthrough (the Teaching Loop)

Work through the review map in risk order. For each **Tier 2** item:

1. **Restate first.** Before explaining, ask the user to state their current read of the hunk — even "no idea what this does" is signal. Calibrate everything that follows to the gap this reveals.
2. **Teach the gap**, moving motivation → mechanism → edge cases → blast radius. Show the actual code; use `file:line` references so they can click through. Use the debugger, tests, or a quick script when concrete evidence beats explanation.
3. **Verify the author's claims** against the code together. "The description says this is backwards-compatible — let's check what happens to callers of the old signature." Update the claims ledger.
4. **Ask reviewer-stance questions**, not textbook questions: What invariant does this rely on? What input breaks it? Is the new behavior tested? What else calls this? What happens on the error path? If this shipped broken, how would we find out?
5. **Teach-back check.** Ask one or two questions and wait for the user's answer — open-ended preferred; multiple choice only when it sharpens a distinction (vary the position of the correct option; never reveal the answer until they've responded). A good PR teach-back is predictive: "Given this change, what does `f(x)` return now when x is empty?"
6. **Correct and re-test.** If they miss, fix the misunderstanding, then ask a follow-up that tests the corrected idea. Move on only when they can explain the hunk in their own words or explicitly choose to accept it on trust (record that in the risk register — accepted-on-trust is a legitimate reviewer move, but it should be a *recorded* one).
7. **Harvest.** After each checkpoint, capture into the review doc: any question for the author, any claim verified or contradicted, any blocker. Preserve the user's phrasing for anything that will become a PR comment.

For **Tier 1** items: steps 2 and 5 only, compressed — one short explanation, one quick check, tick the box.

For **Tier 0**: batch-summarize, offer a spot-check, tick without quizzing.

Use ELI5 / ELI-intern re-explanations on request. If the user asks a question that requires codebase archaeology (who calls this, when was this introduced, does this pattern exist elsewhere), delegate it to a subagent (or investigate it yourself in a tight, bounded pass) rather than burning teaching context — report back only the conclusion.

## Efficiency Rules

These are what make this skill a bottleneck-*reducer* rather than a slower, fancier review:

- **Never teach below the tier.** If the user demonstrates understanding early, skip ahead. If they say "I know this part," take one teach-back question as confirmation and move on — don't lecture past mastery.
- **Front-load risk.** Tier 2 first, always. If the session gets cut short, the riskiest code got the attention.
- **Time-box by stakes.** A hotfix review under time pressure gets Tier 2 treatment only on the changed critical path; a data-migration review gets it everywhere the schema is touched.
- **Delegate all bulk reading.** Subagents fetch, classify, and summarize; the main context is reserved for the human dialogue. Keep subagent reports succinct.
- **One concept per checkpoint.** Small teach-back cycles beat one giant quiz at the end.

## Visual Companion Artifacts

After a Tier 2 section's checkboxes are all ticked, decide whether a visual would materially help recall or the review discussion. Good candidates: before/after mechanism diagrams, data-flow deltas, state machines the diff modifies, claims-ledger tables (claimed vs. actual), a risk heatmap of touched files, call-graph deltas showing blast radius.

Always delegate rendering to a fresh agent — never write HTML/CSS/SVG in the main conversation. Have the agent write a self-contained `review_artifacts/teach_<section-slug>.html` at the project root (create the directory if missing), pure inline SVG/CSS, no CDN, no external assets, no scripts. Pass the agent the section content and the specific visualizations wanted directly — don't make it re-derive the analysis. Label anything unverified as unverified in the visual. Review the returned artifact, then tell the user what's in it and link the path.

Skip visuals for Tier 0/1 material — a diagram of a rename sweep is waste.

## Decision Gate

The session does not end until the user has demonstrated they can answer, in their own words:

1. What does this PR do, and why does the problem it solves exist?
2. What was the design approach, and what alternative would have been reasonable?
3. Which hunk is riskiest, and why?
4. What would you test (or what evidence already covers it)?
5. What is your decision — approve, request changes, or needs discussion — and what would change it?

Run this as a final open-ended check (not multiple choice). If any answer is shaky, loop back to that section — do not rubber-stamp the human's rubber stamp.

Then finalize the review doc: fill in the Decision section, and offer to turn the harvested questions/blockers into actual PR comments (`gh pr review` / `gh pr comment`) **in the user's voice, posted only with their explicit go-ahead** — posting a review is outward-facing and is always confirmed first, regardless of session autonomy settings.

## Next-Move Decision

At the end of each loop iteration, choose one: walk the next map item, drill deeper on the current one, delegate a codebase-archaeology question, delegate a visual companion, pressure-test the user's tentative decision with the strongest counterargument, or proceed to the decision gate. Keep the loop tight when the user is confident; deepen it when they hesitate, hedge, or ask to slow down.
