#!/usr/bin/env node

// Builds the Day 10 comparison only from a live JSONL probe. It validates the
// comparison contract before printing any number, so partial or mixed runs cannot
// become a plausible-looking result document.
import { readFile } from "node:fs/promises";

const input = process.argv[2] || new URL("strategies.jsonl", import.meta.url);
const expectedStrategies = ["sliding", "facts", "branching"];

function fail(message) {
  throw new Error(`day-10 report: ${message}`);
}

function integer(value, label) {
  if (!Number.isInteger(value) || value < 0) fail(`${label} must be a non-negative integer`);
  return value;
}

function number(value, label) {
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) {
    fail(`${label} must be a non-negative number`);
  }
  return value;
}

function totals(value, label) {
  if (!value || typeof value !== "object") fail(`${label} is missing`);
  const result = {
    calls: integer(value.calls, `${label}.calls`),
    failed: integer(value.failed, `${label}.failed`),
    prompt: integer(value.promptTokens, `${label}.promptTokens`),
    completion: integer(value.completionTokens, `${label}.completionTokens`),
    reasoning: integer(value.reasoningTokens, `${label}.reasoningTokens`),
    cached: integer(value.cachedTokens, `${label}.cachedTokens`),
    missed: integer(value.missedTokens, `${label}.missedTokens`),
    cost: number(value.cost, `${label}.cost`),
    unpriced: integer(value.unpriced, `${label}.unpriced`),
  };
  if (result.failed > result.calls) fail(`${label}.failed exceeds calls`);
  if (result.cached + result.missed > result.prompt) fail(`${label} cache split exceeds input`);
  if (result.reasoning > result.completion) fail(`${label}.reasoningTokens exceeds completionTokens`);
  return result;
}

function scoreAnswer(check) {
  let answer;
  try { answer = JSON.parse(check.answer); }
  catch { return 0; }
  if (!answer || typeof answer !== "object" || Array.isArray(answer)) return 0;
  const expected = check.expected;
  const rolesBounded = Array.isArray(answer.roles) && answer.roles.length <= 2;
  const fieldChecks = [
    answer.product === expected[0], answer.deadline === expected[1], answer.budget === expected[2],
    rolesBounded && answer.roles.includes(expected[3]),
    rolesBounded && answer.roles.includes(expected[4]),
    answer.integration === expected[5], answer.auth === expected[6],
    answer.notifications === expected[7], answer.rollout === expected[8],
  ];
  const raw = check.answer.toLowerCase();
  return fieldChecks.filter(Boolean).length +
    check.forbidden.filter((marker) => !raw.includes(String(marker).toLowerCase())).length;
}

function parseRow(value, line) {
  if (!value || typeof value !== "object") fail(`line ${line} is not an object`);
  if (typeof value.run !== "string" || value.run === "") fail(`line ${line}: run is missing`);
  if (!expectedStrategies.includes(value.strategy)) fail(`line ${line}: unknown strategy`);
  const checks = Array.isArray(value.checks) ? value.checks : fail(`${value.strategy}.checks is missing`);
  if (checks.length !== 4) fail(`${value.strategy} must contain four final answers`);
  let passed = 0;
  let all = 0;
  let stable = 0;
  const seenChecks = new Set();
  const scenarioChecks = [];
  for (const [index, check] of checks.entries()) {
    if (!check || typeof check !== "object" || typeof check.answer !== "string") {
      fail(`${value.strategy}.checks[${index}] is invalid`);
    }
    if (!["variant-a", "variant-b"].includes(check.branch) || ![1, 2].includes(check.attempt)) {
      fail(`${value.strategy}.checks[${index}] has an unknown branch or attempt`);
    }
    const signature = `${check.branch}:${check.attempt}`;
    if (seenChecks.has(signature)) fail(`${value.strategy} repeats check ${signature}`);
    seenChecks.add(signature);
    if (!Array.isArray(check.expected) || check.expected.length !== 9 ||
        !Array.isArray(check.forbidden) || check.forbidden.length !== 3) {
      fail(`${value.strategy}.checks[${index}] does not carry the 9 + 3 detail contract`);
    }
    scenarioChecks.push({ branch: check.branch, attempt: check.attempt, expected: check.expected, forbidden: check.forbidden });
    const got = integer(check.passed, `${value.strategy}.checks[${index}].passed`);
    const total = integer(check.total, `${value.strategy}.checks[${index}].total`);
    const derived = scoreAnswer(check);
    if (total !== check.expected.length + check.forbidden.length || got !== derived ||
        Boolean(check.correct) !== (derived === total)) {
      fail(`${value.strategy}.checks[${index}] has inconsistent score`);
    }
    passed += got;
    all += total;
    if (check.correct) stable++;
  }
  if (integer(value.passed, `${value.strategy}.passed`) !== passed ||
      integer(value.totalChecks, `${value.strategy}.totalChecks`) !== all ||
      integer(value.stableAnswers, `${value.strategy}.stableAnswers`) !== stable ||
      integer(value.answers, `${value.strategy}.answers`) !== checks.length) {
    fail(`${value.strategy} aggregate score disagrees with raw answers`);
  }
  const spend = totals(value.spend, `${value.strategy}.spend`);
  const factSpend = totals(value.factSpend, `${value.strategy}.factSpend`);
  for (const field of ["calls", "failed", "prompt", "completion", "reasoning", "cached", "missed", "unpriced", "cost"]) {
    if (factSpend[field] > spend[field] + Number.EPSILON) fail(`${value.strategy}.factSpend exceeds total ${field}`);
  }
  const usability = value.usability;
  if (!usability || typeof usability !== "object") fail(`${value.strategy}.usability is missing`);
  const entered = integer(usability.enteredMessages, `${value.strategy}.enteredMessages`);
  const replayed = integer(usability.replayedMessages, `${value.strategy}.replayedMessages`);
  const actions = integer(usability.controlActions, `${value.strategy}.controlActions`);
  const providerCalls = integer(usability.providerCalls, `${value.strategy}.providerCalls`);
  if (providerCalls !== spend.calls || replayed > entered) fail(`${value.strategy} usability disagrees with spend`);
  return {
    ...value,
    window: integer(value.windowMessages, `${value.strategy}.windowMessages`),
    checks, checkSignature: JSON.stringify(scenarioChecks), spend, factSpend,
    entered, replayed, actions, unique: entered - replayed,
  };
}

function money(row) {
  return row.spend.unpriced ? `не меньше $${row.spend.cost.toFixed(6)}` : `$${row.spend.cost.toFixed(6)}`;
}

function tokens(row) {
  // Provider completion_tokens already includes its reasoning-token subset.
  return row.spend.prompt + row.spend.completion;
}

function row(name, item) {
  return `| ${name} | ${item.passed}/${item.totalChecks} | ${item.stableAnswers}/${item.answers} | ${item.spend.calls} | ${item.factSpend.calls} | ${item.spend.prompt} | ${tokens(item)} | ${money(item)} | ${item.entered} | ${item.replayed} | ${item.actions} |`;
}

const source = await readFile(input, "utf8");
const lines = source.trim().split("\n").filter(Boolean);
if (lines.length !== 3) fail(`expected three JSONL rows, got ${lines.length}`);
const parsed = lines.map((line, index) => {
  try { return parseRow(JSON.parse(line), index + 1); }
  catch (error) { fail(`line ${index + 1}: ${error.message}`); }
});
const runs = new Set(parsed.map((item) => item.run));
if (runs.size !== 1) fail("rows belong to different live runs");
const byStrategy = Object.fromEntries(parsed.map((item) => [item.strategy, item]));
for (const strategy of expectedStrategies) if (!byStrategy[strategy]) fail(`${strategy} row is missing`);
if (byStrategy.sliding.window !== byStrategy.facts.window || byStrategy.sliding.window < 2 || byStrategy.sliding.window % 2) {
  fail("sliding and facts must use the same even window");
}
if (byStrategy.branching.window !== 0) fail("branching must not report a raw window");
for (const strategy of expectedStrategies) {
  if (byStrategy[strategy].unique !== 15) fail(`${strategy} did not run the same 15-message scenario`);
  if (byStrategy[strategy].checkSignature !== byStrategy.sliding.checkSignature) {
    fail(`${strategy} used different final detail checks`);
  }
}
const expectedUX = {
  sliding: { entered: 20, replayed: 5, actions: 0, calls: 20, factCalls: 0 },
  facts: { entered: 20, replayed: 5, actions: 0, calls: 40, factCalls: 20 },
  branching: { entered: 15, replayed: 0, actions: 5, calls: 15, factCalls: 0 },
};
for (const strategy of expectedStrategies) {
  const item = byStrategy[strategy];
  const want = expectedUX[strategy];
  if (item.entered !== want.entered || item.replayed !== want.replayed || item.actions !== want.actions ||
      item.spend.calls !== want.calls || item.factSpend.calls !== want.factCalls) {
    fail(`${strategy} spend or UX counters do not match the fixed scenario`);
  }
  if (strategy !== "facts" && Object.values(item.factSpend).some((value) => value !== 0)) {
    fail(`${strategy} contains a non-zero facts subtotal`);
  }
}

const names = { sliding: "скользящее окно", facts: "Sticky Facts + окно", branching: "ветвление" };
const rows = expectedStrategies.map((strategy) => row(names[strategy], byStrategy[strategy])).join("\n");
const bestQuality = Math.max(...parsed.map((item) => item.passed));
const qualityWinners = parsed.filter((item) => item.passed === bestQuality).map((item) => names[item.strategy]).join(", ");
const leastTokens = parsed.reduce((best, item) => tokens(item) < tokens(best) ? item : best);
const leastEffort = parsed.reduce((best, item) => item.entered < best.entered ? item : best);

process.stdout.write(`# День 10 — сравнение стратегий контекста

<!-- Сгенерировано day-10/render-report.mjs из одного живого JSONL-прогона.
     Измеренные значения не редактировать вручную. -->

Один сценарий из 15 уникальных пользовательских сообщений выполнен тремя способами.
Каждый вариант ТЗ запрошен дважды: качество — доля точных обязательных маркеров без
деталей из соседней ветки; стабильность — сколько полных ответов прошло все проверки.

| стратегия | детали | стабильность | вызовы | из них facts | вход | всего токенов | стоимость | вводов | повторов | действий с ветками |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
${rows}

- Лучший результат по проверяемым деталям: ${qualityWinners} — ${bestQuality}/${byStrategy.sliding.totalChecks}.
- Минимальный общий расход токенов: ${names[leastTokens.strategy]} — ${tokens(leastTokens)}.
- Меньше всего пользовательских вводов: ${names[leastEffort.strategy]} — ${leastEffort.entered}; повторы общих требований: ${leastEffort.replayed}.
- UX-интерпретация ограничена этим сценарием: «вводы» показывают повторный набор текста, а «действия с ветками» — локальные checkpoint/fork/switch без вызова модели.

В расход включены provider prompt_tokens + completion_tokens всех вызовов. Скрытые
reasoning-токены уже входят в completion и второй раз не прибавляются. Для Sticky Facts
отдельные обновления памяти не считаются бесплатными: они входят в общий итог и
дополнительно показаны в колонке «из них facts».

Сырые ответы и учёт поставщика находятся в \`day-10/strategies.jsonl\`. Пересборка:

\`\`\`bash
node day-10/render-report.mjs day-10/strategies.jsonl > day-10/RESULTS.md
\`\`\`
`);
