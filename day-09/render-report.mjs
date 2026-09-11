#!/usr/bin/env node

// Builds the human-readable Day 9 result only from raw JSONL rows written by the
// live probe. It can join the short control and the long-history scenario, but it
// never blends their totals: each pair remains one like-for-like comparison.
import { readFile } from "node:fs/promises";

const here = new URL(".", import.meta.url);
const defaultInput = new URL("compression.jsonl", here);
const inputs = process.argv.slice(2);

function fail(message) {
  throw new Error(`day-09 report: ${message}`);
}

function integer(value, label) {
  if (!Number.isInteger(value) || value < 0) fail(`${label} must be a non-negative integer`);
  return value;
}

function optionalInteger(value, label) {
  if (value === null || value === undefined) return null;
  return integer(value, label);
}

function number(value, label) {
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) {
    fail(`${label} must be a non-negative number`);
  }
  return value;
}

function money(value) {
  return `$${value.toFixed(6)}`;
}

function readTotals(report, field) {
  const total = report[field];
  if (!total || typeof total !== "object") fail(`${report.mode}.${field} is missing`);
  const prompt = integer(total.promptTokens, `${report.mode}.${field}.promptTokens`);
  const completion = integer(total.completionTokens ?? 0, `${report.mode}.${field}.completionTokens`);
  const reasoning = integer(total.reasoningTokens ?? 0, `${report.mode}.${field}.reasoningTokens`);
  const tokens = prompt + completion + reasoning;
  const reportedTokens = optionalInteger(total.totalTokens, `${report.mode}.${field}.totalTokens`);
  if (reportedTokens !== null && reportedTokens !== tokens) {
    fail(`${report.mode}.${field}.totalTokens disagrees with input + output + reasoning`);
  }
  return {
    calls: optionalInteger(total.calls, `${report.mode}.${field}.calls`),
    prompt,
    completion,
    reasoning,
    tokens,
    cached: integer(total.cachedTokens, `${report.mode}.${field}.cachedTokens`),
    cost: number(total.cost, `${report.mode}.${field}.cost`),
  };
}

function readReport(report) {
  if (!report || typeof report !== "object") fail("each JSONL row must be an object");
  if (typeof report.run !== "string" || report.run === "") fail("run is missing");
  if (report.mode !== "full" && report.mode !== "compressed") fail("unknown mode");
  if (!Array.isArray(report.checks) || report.checks.length === 0) fail(`${report.mode}.checks is missing`);
  const checks = report.checks.map((check, index) => {
    if (!check || typeof check !== "object") fail(`${report.mode}.checks[${index}] is invalid`);
    if (typeof check.prompt !== "string" || check.prompt === "") fail(`${report.mode}.checks[${index}].prompt is missing`);
    if (typeof check.expected !== "string" || check.expected === "") fail(`${report.mode}.checks[${index}].expected is missing`);
    return [check.prompt, check.expected];
  });
  const correct = integer(report.correct, `${report.mode}.correct`);
  if (correct > report.checks.length) fail(`${report.mode}.correct exceeds checks`);
  const scenario = report.scenario === undefined ? "short" : report.scenario;
  if (typeof scenario !== "string" || scenario === "") fail(`${report.mode}.scenario is invalid`);
  return {
    run: report.run,
    scenario,
    mode: report.mode,
    checks: checks.length,
    checkSignature: JSON.stringify(checks),
    correct,
    total: readTotals(report, "total"),
    summary: readTotals(report, "summarySpend"),
  };
}

async function readPair(input) {
  const source = await readFile(input, "utf8");
  const rows = source.trim().split("\n").filter(Boolean).map((line, index) => {
    try {
      return readReport(JSON.parse(line));
    } catch (error) {
      fail(`${input.toString()} line ${index + 1}: ${error.message}`);
    }
  });
  if (rows.length !== 2) fail(`${input.toString()}: expected exactly two reports, got ${rows.length}`);

  const full = rows.find((row) => row.mode === "full");
  const compressed = rows.find((row) => row.mode === "compressed");
  if (!full || !compressed) fail(`${input.toString()}: both full and compressed reports are required`);
  if (full.run !== compressed.run) fail(`${input.toString()}: reports are from different probe runs`);
  if (full.scenario !== compressed.scenario) fail(`${input.toString()}: modes use different scenarios`);
  if (full.checks !== compressed.checks) fail(`${input.toString()}: modes ran different check counts`);
  if (full.checkSignature !== compressed.checkSignature) fail(`${input.toString()}: modes ran different exact checks`);
  return { full, compressed };
}

function scenarioTitle(scenario) {
  if (scenario === "short") return "Короткая фактура: контроль накладных расходов";
  if (scenario === "long") return "Длинная фактура: подробные записи и короткие решения";
  return `Сценарий ${scenario}`;
}

function qualityConclusion(full, compressed) {
  return compressed.correct >= full.correct
    ? "В этой закрытой фактуре сжатый режим сохранил не меньше проверяемых маркеров, чем полный."
    : "В этой закрытой фактуре сжатый режим сохранил меньше проверяемых маркеров, чем полный.";
}

function tokenConclusion(full, compressed) {
  const delta = compressed.total.tokens - full.total.tokens;
  if (delta < 0) {
    return `Сжатие снизило общий расход на ${-delta} токенов. В сравнении учтены вход, выход, рассуждения и все вызовы summary.`;
  }
  if (delta > 0) {
    return `Сжатие увеличило общий расход на ${delta} токенов. В сравнении учтены вход, выход, рассуждения и все вызовы summary.`;
  }
  return "Общий расход токенов одинаков; одного уменьшения размера следующего запроса для вывода об экономии недостаточно.";
}

function costConclusion(full, compressed) {
  const delta = compressed.total.cost - full.total.cost;
  if (delta < 0) return `Стоимость сжатого прогона ниже на ${money(-delta)}; это дополнительная метрика, не замена токенам.`;
  if (delta > 0) return `Стоимость сжатого прогона выше на ${money(delta)}; это дополнительная метрика, не замена токенам.`;
  return "Стоимость режимов одинакова; это не меняет токенный вывод выше.";
}

function row(label, report) {
  return `| ${label} | ${report.correct}/${report.checks} | ${report.total.calls ?? "не сообщено"} | ${report.total.prompt} | ${report.total.completion} | ${report.total.reasoning} | ${report.total.tokens} | ${report.total.cached} | ${report.summary.calls ?? "не сообщено"} | ${report.summary.tokens} | ${money(report.total.cost)} |`;
}

const pairs = await Promise.all((inputs.length ? inputs : [defaultInput]).map(readPair));
const scenarios = new Set();
for (const pair of pairs) {
  if (scenarios.has(pair.full.scenario)) fail(`scenario ${pair.full.scenario} was supplied more than once`);
  scenarios.add(pair.full.scenario);
}

const sections = pairs.map(({ full, compressed }) => `## ${scenarioTitle(full.scenario)}

Один и тот же закрытый диалог дважды прошёл через DeepSeek: с полной историей и с
десятью последними сообщениями плюс отдельное summary. Маркеры проверяют только эту
фактуру, а не качество модели вообще.

| режим | маркеры | вызовы модели | вход | выход | рассуждения | всего токенов | из кэша | вызовы summary | токены summary | стоимость |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
${row("полная история", full)}
${row("сжатая история", compressed)}

${qualityConclusion(full, compressed)}

${tokenConclusion(full, compressed)}

${costConclusion(full, compressed)}`).join("\n\n");

process.stdout.write(`# День 9 — живое сравнение

<!-- Сгенерировано day-09/render-report.mjs из сырых JSONL-строк. Измеренные
     значения не редактировать вручную. -->

${sections}

Сырые отчёты поставщика лежат рядом с этим файлом. Пересобрать отчёт можно так:

\`\`\`bash
node day-09/render-report.mjs day-09/compression.jsonl day-09/compression-long.jsonl > day-09/RESULTS.md
\`\`\`
`);
