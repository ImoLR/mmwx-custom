export const MAX_API_ERROR_LENGTH = 240;

function compact(value: string) {
  return value.replace(/\s+/g, " ").trim();
}

function truncate(value: string) {
  if (value.length <= MAX_API_ERROR_LENGTH) return value;
  return `${value.slice(0, MAX_API_ERROR_LENGTH - 6).trimEnd()}…（已截断）`;
}

function looksLikeHTML(contentType: string, body: string) {
  const normalizedType = contentType.toLowerCase();
  const normalizedBody = body.trimStart().slice(0, 256).toLowerCase();
  return normalizedType.includes("text/html")
    || normalizedBody.startsWith("<!doctype html")
    || normalizedBody.startsWith("<html")
    || normalizedBody.includes("<head>");
}

function extractMessage(body: string) {
  const text = body.trim();
  if (!text) return "";
  try {
    const parsed = JSON.parse(text) as { error?: unknown; message?: unknown };
    const candidate = typeof parsed.error === "string" ? parsed.error : typeof parsed.message === "string" ? parsed.message : "";
    return compact(candidate);
  } catch {
    return compact(text);
  }
}

export function formatHTTPError(status: number, contentType: string | null | undefined, body: string) {
  if (looksLikeHTML(contentType ?? "", body)) {
    if ([502, 503, 504].includes(status)) return `请求失败：上游服务暂时不可用（HTTP ${status}）`;
    return `请求失败（HTTP ${status}）`;
  }
  const message = extractMessage(body);
  if (!message && [502, 503, 504].includes(status)) return `请求失败：上游服务暂时不可用（HTTP ${status}）`;
  if (!message) return `请求失败（HTTP ${status}）`;
  return truncate(message);
}

export function parseJSONBody<T>(status: number, contentType: string | null | undefined, body: string): T {
  if (status < 200 || status >= 300) throw new Error(formatHTTPError(status, contentType, body));
  try {
    return JSON.parse(body) as T;
  } catch {
    throw new Error(`响应格式无效（HTTP ${status}）`);
  }
}
