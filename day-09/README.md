# Day 9 — context compression

The week-2 agent now has two separate memory layers:

- the latest `N` messages remain verbatim;
- older complete exchanges become one model-written summary, stored separately in the
  session JSON and inserted into the next request instead of their raw text.

Run an ordinary compressed conversation with:

```bash
go run ./day-06 -session demo -keep-last 10
```

`/context` in dialogue mode, or `-context` for a one-shot inspection, reports the raw
tail, the number of compressed messages, the estimated summary weight, and the summary
call subtotal. `-keep-last 0` turns compression off for a control run. `-max-turns` and
`-keep-last` intentionally cannot be combined: the former deletes past exchanges,
whereas the latter preserves their meaning first.

The reproducible live comparison is:

```bash
go run ./day-06 -compression-probe -keep-last 10
```

It writes `compression.jsonl` in this directory. The file contains one JSON report for
the full-history run and one for the compressed run: exact-marker checks from the
beginning, middle, and raw tail of the same fixture; total provider usage; cache split;
and the separately named summary subtotal. A compression run is only cheaper when its
whole total, including summary calls, is lower.

Build the human-readable result from those raw rows, rather than typing measurements
into prose:

```bash
node day-09/render-report.mjs > day-09/RESULTS.md
```

The checked-in `RESULTS.md` is therefore reproducible from `compression.jsonl`. Test
doubles prove request composition and persistence but cannot demonstrate provider token
usage or answer quality.
