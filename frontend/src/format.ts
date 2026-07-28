export function formatGB(value?: number | null) {
  if (value == null || Number.isNaN(value)) return "--";
  if (value === 0) return "0 GB";
  return `${trimNumber(value)} GB`;
}

export function formatBytes(bytes?: number | null) {
  if (bytes == null || Number.isNaN(bytes)) return "--";
  const abs = Math.abs(bytes);
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = abs;
  let idx = 0;
  while (value >= 1024 && idx < units.length - 1) {
    value /= 1024;
    idx += 1;
  }
  const signed = bytes < 0 ? "-" : "";
  return `${signed}${trimNumber(value)} ${units[idx]}`;
}

export function formatSpeed(bytesPerSecond?: number | null) {
  if (bytesPerSecond == null) return "--";
  return `${formatBytes(bytesPerSecond)}/s`;
}

export function formatDurationSince(value?: string | null) {
  if (!value) return "--";
  const then = new Date(value).getTime();
  if (!Number.isFinite(then)) return "--";
  const seconds = Math.max(0, Math.floor((Date.now() - then) / 1000));
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  if (days > 0) return `${days}天 ${hours}小时`;
  const minutes = Math.floor((seconds % 3600) / 60);
  if (hours > 0) return `${hours}小时 ${minutes}分钟`;
  return `${minutes}分钟`;
}

export function todayUTC() {
  return new Date().toISOString().slice(0, 10);
}

function trimNumber(value: number) {
  return value >= 100 ? value.toFixed(0) : value >= 10 ? value.toFixed(1) : value.toFixed(2).replace(/\.?0+$/, "");
}
