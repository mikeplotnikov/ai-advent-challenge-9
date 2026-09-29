# Day 10 — three context strategies

The same 15-message requirements scenario is measured with three mutually exclusive
context policies and no summary:

- `sliding`: only the newest `N` user/assistant messages remain;
- `facts`: the same raw window plus structured Sticky Facts, refreshed by a separate
  model call after every user message;
- `branching`: one exact checkpoint is copied into two independent branches that can
  be switched without replaying the common requirements.

`N` counts individual messages, not exchanges, and must be even. The default is 10.
Sticky Facts keep only explicit user requirements. Corrections replace old values and
deletion requires an explicit request. Their update calls are included in the total
provider usage and are also shown as a named subtotal.

Run the live comparison from the repository root:

```bash
go run ./day-10 -out day-10/strategies.jsonl
node day-10/render-report.mjs day-10/strategies.jsonl > day-10/RESULTS.md
```

The first command needs `DEEPSEEK_API_KEY`. It makes real provider calls and writes
all final answers plus their token/cost accounting to JSONL. The second command
validates that the file contains all three strategies from one run and derives the
human-readable comparison. Do not type measured numbers into `RESULTS.md` by hand.

The scenario holds five shared requirements, adds three decisions in each of two
variants, then asks the same final question twice in each variant. Branching enters
those 15 unique messages once. Sliding and Facts have no checkpoint, so the second
variant is a fresh conversation with the five shared requirements replayed: 20 entered
messages, including five explicit replays. This user-effort difference is part of the
measurement rather than hidden from it.

For an ordinary interactive conversation, the same implementation is available through
the week-2 CLI:

```bash
go run ./day-06 -context-strategy sliding -window-messages 10 -session sliding-demo
go run ./day-06 -context-strategy facts -window-messages 10 -session facts-demo
go run ./day-06 -context-strategy branching -session branches-demo
```

Branching commands are `/checkpoint NAME`, `/branch NAME CHECKPOINT`, `/switch NAME`,
and `/branches`. `/context` shows the selected policy and its stored state. Use a
different session name when changing strategies; the agent refuses to reinterpret an
existing snapshot under another policy.
