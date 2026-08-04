# Signal Garden Feedback Plan

## Checkpoints

| Checkpoint | Question | Evidence to collect | Decision |
| --- | --- | --- | --- |
| After M0 | Is the event loop fun and understandable without Kafka? | Five-minute local demo and replay test | Keep, simplify, or change the garden rules |
| After M1 | Does the UI make system pressure legible? | Screen recording, browser test, latency strip | Adjust controls and visual feedback |
| After M2 | Does Kafka add meaningful behavior? | Stop/restart/replay demo and lag measurements | Keep topic design or revise partitioning |
| After M3 | Is the performance story credible? | Load report and traces | Tune bottleneck or document limit |
| After M4 | Is this a compelling showcase? | Fresh-checkout demo and external walkthrough | Publish, extend, or extract mobile client |

## Feedback Prompts

- Can a viewer understand what changed when event rate increases?
- Is queue lag represented as a meaningful garden consequence rather than an abstract number?
- Do controls feel responsive even when the backend is under pressure?
- Is failure injection interesting enough to repeat?
- Does replay help explain what happened?
- Which metric would a backend engineer want to inspect next?

## Decision Records

Keep short records in `docs/decisions/` in the standalone repository. Each record should include the date, context, options considered, decision, evidence, and what would cause the decision to be revisited.