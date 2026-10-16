import assert from "node:assert/strict";
import test from "node:test";
import { helperBatchCounts, showHelperBatchButton } from "../../frontend/src/helper-batch-state.ts";
import type { HelperBatchJob } from "../../frontend/src/types.ts";

test("batch view counts failures and cancellation without including skipped servers", () => {
  const job: HelperBatchJob = { id: "test", phase: "running", started_at: "2026-10-16T16:00:00Z", updated_at: "2026-10-16T16:01:00Z", stopped: false,
    rows: ["success", "failed", "rolled_back", "cancelled", "downloading", "queued", "skipped"].map((phase, i) => ({ server_id: String(i), name: String(i), version: "v0.7.1", phase })) };
  assert.deepEqual(helperBatchCounts(job), { total: 6, done: 4, failed: 2, cancelled: 1, percent: 67 });
  assert.equal(showHelperBatchButton({ success: true, version: "v0.7.4", preview: [], job }), true);
  assert.equal(showHelperBatchButton({ success: true, version: "v0.7.4", preview: [{ server_id: "5", name: "Boil Hinet 马年", version: "v0.7.4", phase: "skipped" }] }), false);
  assert.equal(showHelperBatchButton({ success: true, version: "v0.7.4", preview: [{ server_id: "2", name: "Boil HKT 99u", version: "v0.7.1", phase: "queued" }] }), true);
});
