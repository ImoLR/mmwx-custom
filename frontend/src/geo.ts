import { fetchGeoLookup } from "./api";
import type { GeoLookupResponse, RemoteServer } from "./types";

export type RegionDisplay = {
  known: boolean;
  loading?: boolean;
  flag: string;
  label: string;
};

const countryLabels: Record<string, string> = {
  CN: "中国",
  HK: "香港",
  MO: "澳门",
  TW: "台湾",
  JP: "日本",
  KR: "韩国",
  KP: "朝鲜",
  MN: "蒙古",
  SG: "新加坡",
  TH: "泰国",
  VN: "越南",
  PH: "菲律宾",
  MY: "马来西亚",
  ID: "印尼",
  MM: "缅甸",
  KH: "柬埔寨",
  IN: "印度",
  PK: "巴基斯坦",
  BD: "孟加拉",
  KZ: "哈萨克斯坦",
  TR: "土耳其",
  AE: "阿联酋",
  SA: "沙特",
  IL: "以色列",
  US: "美国",
  CA: "加拿大",
  MX: "墨西哥",
  BR: "巴西",
  AR: "阿根廷",
  CL: "智利",
  CO: "哥伦比亚",
  GB: "英国",
  DE: "德国",
  FR: "法国",
  NL: "荷兰",
  IT: "意大利",
  ES: "西班牙",
  RU: "俄罗斯",
  UA: "乌克兰",
  PL: "波兰",
  SE: "瑞典",
  NO: "挪威",
  FI: "芬兰",
  CH: "瑞士",
  AT: "奥地利",
  IE: "爱尔兰",
  PT: "葡萄牙",
  CZ: "捷克",
  RO: "罗马尼亚",
  HU: "匈牙利",
  LU: "卢森堡",
  IS: "冰岛",
  AU: "澳大利亚",
  NZ: "新西兰",
  ZA: "南非",
  EG: "埃及",
  NG: "尼日利亚",
  KE: "肯尼亚",
};

const regionCache = new Map<string, Promise<RegionDisplay>>();
const maxConcurrentLookups = 4;
let activeLookups = 0;
const lookupQueue: Array<() => void> = [];

export function serverRegionFromFields(server: RemoteServer): RegionDisplay | null {
  const code = server.country_code?.trim().toUpperCase();
  const label = server.region_name || server.region || server.country || (code ? countryLabels[code] : "");

  if (!code && !label) return null;
  return {
    known: true,
    flag: flagFromCountryCode(code),
    label: label || code || "地区未知",
  };
}

export function serverRegionAddress(server: RemoteServer) {
  return [server.pull_address, server.domain, server.ip_address, server.ip_address_v6].find((value) => Boolean(value?.trim()))?.trim() ?? "";
}

export function unknownRegion(): RegionDisplay {
  return { known: false, flag: "", label: "地区未知" };
}

export function loadingRegion(): RegionDisplay {
  return { known: false, loading: true, flag: "", label: "检测中" };
}

export function lookupServerRegion(server: RemoteServer, signal?: AbortSignal): Promise<RegionDisplay> {
  void signal;
  const fromFields = serverRegionFromFields(server);
  if (fromFields) return Promise.resolve(fromFields);

  const address = serverRegionAddress(server);
  if (!address) return Promise.resolve(unknownRegion());

  const cached = regionCache.get(address);
  if (cached) return cached;

  const request = withLookupSlot(undefined, () => fetchGeoLookup(address))
    .then(regionFromLookupResponse)
    .catch(() => unknownRegion());
  regionCache.set(address, request);
  return request;
}

async function withLookupSlot<T>(signal: AbortSignal | undefined, task: () => Promise<T>) {
  await acquireLookupSlot(signal);
  try {
    return await task();
  } finally {
    releaseLookupSlot();
  }
}

function acquireLookupSlot(signal?: AbortSignal) {
  if (activeLookups < maxConcurrentLookups) {
    activeLookups += 1;
    return Promise.resolve();
  }
  return new Promise<void>((resolve, reject) => {
    const run = () => {
      signal?.removeEventListener("abort", onAbort);
      activeLookups += 1;
      resolve();
    };
    const onAbort = () => {
      const index = lookupQueue.indexOf(run);
      if (index >= 0) lookupQueue.splice(index, 1);
      reject(new DOMException("Aborted", "AbortError"));
    };
    if (signal?.aborted) {
      reject(new DOMException("Aborted", "AbortError"));
      return;
    }
    signal?.addEventListener("abort", onAbort, { once: true });
    lookupQueue.push(run);
  });
}

function releaseLookupSlot() {
  activeLookups = Math.max(0, activeLookups - 1);
  const next = lookupQueue.shift();
  if (next) next();
}

function regionFromLookupResponse(response: GeoLookupResponse): RegionDisplay {
  const code = response.country_code?.trim().toUpperCase();
  const label = response.country || (code ? countryLabels[code] : "");
  if (!code && !label) return unknownRegion();
  return {
    known: true,
    flag: response.flag || flagFromCountryCode(code),
    label: label || code || "地区未知",
  };
}

function flagFromCountryCode(code?: string | null) {
  const normalized = code?.trim().toUpperCase();
  if (!normalized || !/^[A-Z]{2}$/.test(normalized)) return "";
  return String.fromCodePoint(...[...normalized].map((char) => 0x1f1a5 + char.charCodeAt(0)));
}
