import type { HelperBatchJob, HelperBatchResponse } from "./types.ts";

export function helperBatchCounts(job: HelperBatchJob) {
  const rows = job.rows.filter((row) => row.phase !== "skipped");
  const done = rows.filter((row) => ["success", "failed", "rolled_back", "cancelled"].includes(row.phase)).length;
  return { total: rows.length, done, failed: rows.filter((row) => ["failed", "rolled_back"].includes(row.phase)).length, cancelled: rows.filter((row) => row.phase === "cancelled").length, percent: rows.length ? Math.round(done / rows.length * 100) : 100 };
}

export function showHelperBatchButton(data?: HelperBatchResponse | null) {
  return data?.job?.phase === "running" || Boolean(data?.version && data.preview.some((row) => row.version && row.version !== data.version));
}
