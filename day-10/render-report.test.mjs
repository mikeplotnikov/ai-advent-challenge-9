import assert from "node:assert/strict";
import { mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";

const empty = () => ({ calls: 0, failed: 0, promptTokens: 0, completionTokens: 0, reasoningTokens: 0, cachedTokens: 0, missedTokens: 0, cost: 0, unpriced: 0 });
const expected = (branch) => [
  "ATLAS-17", "2026-10-21", "450000 RUB", "employee", "admin", "Google Calendar",
  branch === "variant-a" ? "SSO" : "email/password",
  branch === "variant-a" ? "Telegram" : "email notifications",
  branch === "variant-a" ? "20 employees" : "60 employees",
];
const forbidden = (branch) => branch === "variant-a"
  ? ["email/password", "email notifications", "60 employees"]
  : ["SSO", "Telegram", "20 employees"];
const answer = (branch) => JSON.stringify({
  product: "ATLAS-17", deadline: "2026-10-21", budget: "450000 RUB", roles: ["employee", "admin"],
  integration: "Google Calendar", auth: expected(branch)[6], notifications: expected(branch)[7], rollout: expected(branch)[8],
});
const row = (strategy) => ({
  run: "fixture", strategy, windowMessages: strategy === "branching" ? 0 : 10,
  checks: Array.from({ length: 4 }, (_, index) => {
    const branch = index < 2 ? "variant-a" : "variant-b";
    return { branch, attempt: index % 2 + 1, answer: answer(branch), expected: expected(branch), forbidden: forbidden(branch), passed: 12, total: 12, correct: true };
  }),
  passed: 48, totalChecks: 48, stableAnswers: 4, answers: 4,
  spend: { ...empty(), calls: strategy === "facts" ? 40 : strategy === "branching" ? 15 : 20, promptTokens: 100, missedTokens: 100, completionTokens: 20, reasoningTokens: 7, cost: 0.01 },
  factSpend: strategy === "facts" ? { ...empty(), calls: 20, promptTokens: 40, missedTokens: 40, completionTokens: 5, reasoningTokens: 2, cost: 0.003 } : empty(),
  usability: { enteredMessages: strategy === "branching" ? 15 : 20, replayedMessages: strategy === "branching" ? 0 : 5, controlActions: strategy === "branching" ? 5 : 0, providerCalls: strategy === "facts" ? 40 : strategy === "branching" ? 15 : 20 },
});

const dir = await mkdtemp(join(tmpdir(), "day10-report-"));
const input = join(dir, "input.jsonl");
await writeFile(input, [row("sliding"), row("facts"), row("branching")].map(JSON.stringify).join("\n") + "\n");
const script = new URL("render-report.mjs", import.meta.url);
let run = spawnSync(process.execPath, [script.pathname, input], { encoding: "utf8" });
assert.equal(run.status, 0, run.stderr);
assert.match(run.stdout, /Sticky Facts \+ окно/);
assert.match(run.stdout, /15; повторы общих требований: 0/);
assert.match(run.stdout, /\| 100 \| 120 \|/, 'reasoning subset was added to completion a second time');

const broken = row("branching");
broken.usability.enteredMessages = 14;
await writeFile(input, [row("sliding"), row("facts"), broken].map(JSON.stringify).join("\n") + "\n");
run = spawnSync(process.execPath, [script.pathname, input], { encoding: "utf8" });
assert.notEqual(run.status, 0, "partial scenario was accepted");
assert.match(run.stderr, /same 15-message scenario/);

const forged = row("facts");
forged.checks[0].answer = "{}";
await writeFile(input, [row("sliding"), forged, row("branching")].map(JSON.stringify).join("\n") + "\n");
run = spawnSync(process.execPath, [script.pathname, input], { encoding: "utf8" });
assert.notEqual(run.status, 0, "precomputed score was trusted over the raw answer");
assert.match(run.stderr, /inconsistent score/);

const missingFactCall = row("facts");
missingFactCall.factSpend.calls = 19;
await writeFile(input, [row("sliding"), missingFactCall, row("branching")].map(JSON.stringify).join("\n") + "\n");
run = spawnSync(process.execPath, [script.pathname, input], { encoding: "utf8" });
assert.notEqual(run.status, 0, "incomplete fact-call subtotal was accepted");
assert.match(run.stderr, /fixed scenario/);

const inventedRole = row("sliding");
inventedRole.checks[0].answer = inventedRole.checks[0].answer.replace('"admin"]', '"admin","superadmin"]');
await writeFile(input, [inventedRole, row("facts"), row("branching")].map(JSON.stringify).join("\n") + "\n");
run = spawnSync(process.execPath, [script.pathname, input], { encoding: "utf8" });
assert.notEqual(run.status, 0, "an invented role kept a precomputed perfect score");
assert.match(run.stderr, /inconsistent score/);

await readFile(input, "utf8");
console.log("OK Day 10 report validates one complete comparable run");
