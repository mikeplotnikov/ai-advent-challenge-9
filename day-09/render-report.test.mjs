#!/usr/bin/env node

import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const root = new URL(".", import.meta.url);
const renderer = new URL("render-report.mjs", root);
const temp = await mkdtemp(join(tmpdir(), "day09-render-"));

function totals(promptTokens, completionTokens, cost) {
	return { calls: 1, promptTokens, completionTokens, reasoningTokens: 0, cachedTokens: 0, cost };
}

function report(run, scenario, mode, correct, total, summarySpend) {
	return { run, scenario, mode, checks: [{ prompt: "q", expected: "x", answer: "x", correct: true }], correct, total, summarySpend };
}

function rejects(args, expected) {
	assert.throws(() => execFileSync(process.execPath, [fileURLToPath(renderer), ...args], { encoding: "utf8", stdio: "pipe" }), (error) =>
		`${error.message}\n${error.stderr || ""}\n${error.stdout || ""}`.includes(expected), expected);
}

try {
	const short = join(temp, "short.jsonl");
	const long = join(temp, "long.jsonl");
	await writeFile(short, [
		report("short-run", "short", "full", 1, totals(100, 10, 0.0001), totals(0, 0, 0)),
		report("short-run", "short", "compressed", 1, totals(130, 20, 0.0002), totals(30, 10, 0.00005)),
	].map(JSON.stringify).join("\n") + "\n");
	await writeFile(long, [
		report("long-run", "long", "full", 1, totals(1000, 100, 0.001), totals(600, 100, 0.0008)),
		report("long-run", "long", "compressed", 1, totals(600, 100, 0.0008), totals(200, 50, 0.0002)),
	].map(JSON.stringify).join("\n") + "\n");

  const output = execFileSync(process.execPath, [fileURLToPath(renderer), short, long], { encoding: "utf8" });
	assert.match(output, /всего токенов/);
	assert.match(output, /Сжатие увеличило общий расход на 40 токенов/);
	assert.match(output, /Сжатие снизило общий расход на 400 токенов/);
	assert.match(output, /Короткая фактура/);
	assert.match(output, /Длинная фактура/);

	const wrongRun = join(temp, "wrong-run.jsonl");
	await writeFile(wrongRun, [
		report("first", "short", "full", 1, totals(1, 1, 0), totals(0, 0, 0)),
		report("second", "short", "compressed", 1, totals(1, 1, 0), totals(0, 0, 0)),
	].map(JSON.stringify).join("\n") + "\n");
	rejects([wrongRun], "reports are from different probe runs");

	const wrongScenario = join(temp, "wrong-scenario.jsonl");
	await writeFile(wrongScenario, [
		report("one-run", "short", "full", 1, totals(1, 1, 0), totals(0, 0, 0)),
		report("one-run", "long", "compressed", 1, totals(1, 1, 0), totals(0, 0, 0)),
	].map(JSON.stringify).join("\n") + "\n");
	rejects([wrongScenario], "modes use different scenarios");

	const wrongChecks = join(temp, "wrong-checks.jsonl");
	await writeFile(wrongChecks, [
		report("one-run", "short", "full", 1, totals(1, 1, 0), totals(0, 0, 0)),
		{ ...report("one-run", "short", "compressed", 1, totals(1, 1, 0), totals(0, 0, 0)), checks: [{ prompt: "q", expected: "x" }, { prompt: "q2", expected: "y" }] },
	].map(JSON.stringify).join("\n") + "\n");
	rejects([wrongChecks], "modes ran different check counts");

	const wrongMarkers = join(temp, "wrong-markers.jsonl");
	await writeFile(wrongMarkers, [
		report("one-run", "short", "full", 1, totals(1, 1, 0), totals(0, 0, 0)),
		{ ...report("one-run", "short", "compressed", 1, totals(1, 1, 0), totals(0, 0, 0)), checks: [{ prompt: "другой вопрос", expected: "x" }] },
	].map(JSON.stringify).join("\n") + "\n");
	rejects([wrongMarkers], "modes ran different exact checks");
	console.log("OK renderer reports input, output, total, summary, both token conclusions, and rejects every incomparable pair");
} finally {
	await rm(temp, { recursive: true, force: true });
}
