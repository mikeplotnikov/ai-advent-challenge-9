#!/usr/bin/env node

// Builds the human-readable Day 9 result only from the two raw JSONL rows written
// by the live probe. Keep the conclusion deliberately narrow: the fixture checks
// planted markers, not arbitrary conversation quality.
import { readFile } from "node:fs/promises";

const here = new URL(".", import.meta.url);
const input = new URL("compression.jsonl", here);

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

function money(value, label) {
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) {
    fail(`${label} must be a non-negative number`);
  }
  return `$${value.toFixed(6)}`;
}

function readTotals(report, field) {
  const total = report[field];
  if (!total || typeof total !== "object") fail(`${report.mode}.${field} is missing`);
  return {
    calls: optionalInteger(total.calls, `${report.mode}.${field}.calls`),
    prompt: integer(total.promptTokens, `${report.mode}.${field}.promptTokens`),
    cached: integer(total.cachedTokens, `${report.mode}.${field}.cachedTokens`),
    cost: money(total.cost, `${report.mode}.${field}.cost`),
  };
}

function readReport(report) {
  if (!report || typeof report !== "object") fail("each JSONL row must be an object");
  if (typeof report.run !== "string" || report.run === "") fail("run is missing");
  if (report.mode !== "full" && report.mode !== "compressed") fail("unknown mode");
  if (!Array.isArray(report.checks) || report.checks.length === 0) fail(`${report.mode}.checks is missing`);
  const correct = integer(report.correct, `${report.mode}.correct`);
  if (correct > report.checks.length) fail(`${report.mode}.correct exceeds checks`);
  return {
    run: report.run,
    mode: report.mode,
    checks: report.checks.length,
    correct,
    total: readTotals(report, "total"),
    summary: readTotals(report, "summarySpend"),
  };
}

const source = await readFile(input, "utf8");
const rows = source.trim().split("\n").filter(Boolean).map((line, index) => {
  try {
    return readReport(JSON.parse(line));
  } catch (error) {
    fail(`line ${index + 1}: ${error.message}`);
  }
});
if (rows.length !== 2) fail(`expected exactly two reports, got ${rows.length}`);

const full = rows.find((row) => row.mode === "full");
const compressed = rows.find((row) => row.mode === "compressed");
if (!full || !compressed) fail("both full and compressed reports are required");
if (full.run !== compressed.run) fail("the reports are from different probe runs");
if (full.checks !== compressed.checks) fail("the modes ran different check counts");

const quality = compressed.correct >= full.correct
  ? "В этой закрытой фактуре сжатый режим сохранил не меньше маркеров, чем полный."
  : "В этой закрытой фактуре сжатый режим сохранил меньше маркеров, чем полный."
const fullCost = Number(full.total.cost.slice(1));
const compressedCost = Number(compressed.total.cost.slice(1));
const economy = compressedCost < fullCost
  ? `Полная стоимость сжатого прогона ниже на $${(fullCost - compressedCost).toFixed(6)}; в ней уже учтён summary.`
  : compressedCost === fullCost
    ? "Полная стоимость режимов одинакова; одного уменьшения размера ответа для вывода об экономии недостаточно."
    : `Сжатый прогон дороже на $${(compressedCost - fullCost).toFixed(6)} с учётом summary; на этой фактуре его нельзя называть экономией.`;

process.stdout.write(`# День 9 — живое сравнение\n\n<!-- Сгенерировано day-09/render-report.mjs из day-09/compression.jsonl.\n     Измеренные значения не редактировать вручную. Прогон: ${full.run}. -->\n\nОдин и тот же закрытый диалог дважды прошёл через DeepSeek: с полной историей и с\nдесятью последними сообщениями плюс отдельное summary. Три точных маркера проверяют\nначало, середину и сырой хвост этой фактуры; это не общая оценка качества модели.\n\n| режим | маркеры | вызовы модели | вход, токенов | из кэша | всего | вызовы summary | summary |\n|---|---:|---:|---:|---:|---:|---:|---:|\n| полная история | ${full.correct}/${full.checks} | ${full.total.calls ?? 'не сообщено'} | ${full.total.prompt} | ${full.total.cached} | ${full.total.cost} | ${full.summary.calls ?? 'не сообщено'} | ${full.summary.cost} |\n| сжатая история | ${compressed.correct}/${compressed.checks} | ${compressed.total.calls ?? 'не сообщено'} | ${compressed.total.prompt} | ${compressed.total.cached} | ${compressed.total.cost} | ${compressed.summary.calls ?? 'не сообщено'} | ${compressed.summary.cost} |\n\n${quality}\n\n${economy}\n\nСырые отчёты поставщика лежат в [compression.jsonl](compression.jsonl). После нового\nпрогона этот файл пересобирается, а не редактируется:\n\n\`\`\`bash\nnode day-09/render-report.mjs > day-09/RESULTS.md\n\`\`\`\n`);
