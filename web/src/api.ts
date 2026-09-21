import type {
  ApiErrorBody,
  AsteriskExtension,
  AsteriskExtensionCandidate,
  AsteriskExtensions,
  AsteriskInbound,
  AsteriskSMS,
  AsteriskSMSMessage,
  AsteriskRegistration,
  AsteriskRoute,
  AsteriskRoutes,
  AsteriskTrunk,
  AsteriskTrunks,
  AsteriskStatus,
  Call,
  CallRecordsResponse,
  CallsResponse,
  LoggingSettings,
  LoginResponse,
  SecuritySettings,
  Session,
} from "./types";
import { tl } from "./lib/i18n";

const CSRF_KEY = "vocat.csrf";

// Authenticated pages and same-origin plugin frames share this signal. Clear
// the mutation token immediately so a revoked session cannot leave stale auth
// state behind in the browser.
export function notifyUnauthorized() {
  try {
    sessionStorage.removeItem(CSRF_KEY);
  } catch {
    /* ignore unavailable storage */
  }
  window.dispatchEvent(new Event("vocat:unauthorized"));
}

function isMutation(method: string) {
  return !["GET", "HEAD", "OPTIONS"].includes(method.toUpperCase());
}

function camelizeKey(key: string) {
  return key.replace(/_([a-z0-9])/g, (_, char: string) => char.toUpperCase());
}

function snakeizeKey(key: string) {
  return key
    .replace(/([a-z0-9])([A-Z])/g, "$1_$2")
    .replace(/-/g, "_")
    .toLowerCase();
}

export function camelize<T>(value: unknown): T {
  if (Array.isArray(value)) return value.map((item) => camelize(item)) as T;
  if (value !== null && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value as Record<string, unknown>).map(([key, item]) => [
        camelizeKey(key),
        camelize(item),
      ]),
    ) as T;
  }
  return value as T;
}

function snakeize(value: unknown): unknown {
  if (Array.isArray(value)) return value.map((item) => snakeize(item));
  if (value !== null && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value as Record<string, unknown>).map(([key, item]) => [
        snakeizeKey(key),
        snakeize(item),
      ]),
    );
  }
  return value;
}

export class ApiError extends Error {
  status: number;
  code: string;
  requestId: string;
  detail: ApiErrorBody;

  constructor(status: number, detail: ApiErrorBody) {
    super(detail.message || detail.error || `${tl("请求失败")}（HTTP ${status}）`);
    this.name = "ApiError";
    this.status = status;
    this.code = detail.code || "";
    this.requestId = detail.requestId || "";
    this.detail = detail;
  }
}

export interface RequestOptions extends Omit<RequestInit, "body"> {
  body?: unknown;
  raw?: boolean;
}

async function refreshCSRFToken(): Promise<boolean> {
  try {
    const response = await fetch("/api/auth/session", {
      method: "GET",
      headers: { Accept: "application/json" },
      credentials: "include",
      cache: "no-store",
    });
    if (!response.ok) {
      if (response.status === 401) notifyUnauthorized();
      return false;
    }
    const payload = await response.json() as { data?: { csrf_token?: string } };
    const token = payload?.data?.csrf_token;
    if (!token) return false;
    sessionStorage.setItem(CSRF_KEY, token);
    return true;
  } catch {
    return false;
  }
}

async function requestAPI<T>(path: string, options: RequestOptions, retryCSRF: boolean): Promise<T> {
  const method = (options.method || "GET").toUpperCase();
  const headers = new Headers(options.headers);
  const formBody = typeof FormData !== "undefined" && options.body instanceof FormData;
  headers.set("Accept", options.raw ? "*/*" : "application/json");
  if (options.body !== undefined && !formBody) headers.set("Content-Type", "application/json");
  if (isMutation(method)) {
    const csrf = sessionStorage.getItem(CSRF_KEY);
    if (csrf) headers.set("X-CSRF-Token", csrf);
  }

  const response = await fetch(path.startsWith("/api") ? path : `/api${path}`, {
    ...options,
    method,
    headers,
    credentials: "include",
    body: options.body === undefined
      ? undefined
      : formBody
        ? options.body as FormData
        : JSON.stringify(snakeize(options.body)),
  });

  if (options.raw) {
    if (response.status === 401) notifyUnauthorized();
    return response as T;
  }
  const contentType = response.headers.get("content-type") || "";
  const payload = contentType.includes("application/json")
    ? await response.json()
    : { message: await response.text() };
  const normalized = camelize<Record<string, unknown>>(payload);
  if (!response.ok) {
    const nested = normalized.error;
    const detail = nested && typeof nested === "object"
      ? {
          ...(nested as ApiErrorBody),
          requestId: (normalized.requestId as string | undefined) || (nested as ApiErrorBody).requestId,
        }
      : normalized as ApiErrorBody;
    if (
      retryCSRF &&
      isMutation(method) &&
      response.status === 403 &&
      detail.code === "invalid_csrf"
    ) {
      if (await refreshCSRFToken()) return requestAPI<T>(path, options, false);
      notifyUnauthorized();
    } else if (response.status === 401) {
      notifyUnauthorized();
    }
    throw new ApiError(response.status, detail);
  }
  return (Object.prototype.hasOwnProperty.call(normalized, "data") ? normalized.data : normalized) as T;
}

export async function api<T>(path: string, options: RequestOptions = {}): Promise<T> {
  return requestAPI<T>(path, options, true);
}

export async function login(username: string, password: string) {
  const result = await api<LoginResponse & { user?: { username?: string } }>("/auth/login", {
    method: "POST",
    body: { username, password },
  });
  if (result.csrfToken) sessionStorage.setItem(CSRF_KEY, result.csrfToken);
  return result;
}

export async function session() {
  const result = await api<Session & { user?: { username?: string } }>("/auth/session");
  if (result.csrfToken) sessionStorage.setItem(CSRF_KEY, result.csrfToken);
  return {
    ...result,
    username: result.username || result.user?.username || "",
    role: result.role || "Administrator",
  };
}

export async function logout() {
  try {
    await api("/auth/logout", { method: "POST" });
  } finally {
    sessionStorage.removeItem(CSRF_KEY);
  }
}

export function getSecuritySettings() {
  return api<SecuritySettings>("/settings/security");
}

export function updateSecuritySettings(settings: {
  mode: SecuritySettings["mode"];
  allowedCidrs: string[];
  trustProxyHeaders: boolean;
}) {
  return api<SecuritySettings>("/settings/security", { method: "PUT", body: settings });
}

export function getLoggingSettings() {
  return api<LoggingSettings>("/settings/logging");
}

export function updateLoggingSettings(settings: {
  mode: LoggingSettings["mode"];
  count: number;
  days: number;
}) {
  return api<LoggingSettings>("/settings/logging", { method: "PUT", body: settings });
}

// Live PBX state read over AMI. Never throws for an unreachable PBX: the
// payload carries the reason, because "Asterisk is down" is the answer this
// page exists to give rather than a request failure.
export function getAsteriskStatus() {
  return api<AsteriskStatus>("/asterisk/status");
}

// Ending a channel is checked against the live list server-side: AMI's Hangup
// takes a regular expression when the value is wrapped in slashes, and an
// exact match against a real channel is what makes that unreachable.
export function hangupAsteriskChannel(channel: string, cause?: number) {
  return api<{ hungup: boolean; channel: string; cause: number }>("/asterisk/channels/hangup", {
    method: "POST",
    body: { channel, cause },
  });
}

// Registration changes over time. "recording" says whether history is being
// collected at all, so an empty list on a PBX with no manager interface reads
// as off rather than quiet.
export function getAsteriskRegistrations(limit = 50) {
  return api<{ registrations: AsteriskRegistration[]; recording: boolean }>(
    `/asterisk/registrations?limit=${limit}`,
  );
}

export function getAsteriskRoutes() {
  return api<AsteriskRoutes>("/asterisk/routes");
}

export function saveAsteriskRoutes(routes: AsteriskRoute[]) {
  return api<{ saved: boolean; written: boolean; preview: string }>("/asterisk/routes", {
    method: "PUT",
    body: { routes },
  });
}

// Applying reloads only pbx_config, so calls in progress are unaffected.
export function applyAsteriskRoutes() {
  return api<{ applied: boolean; message?: string; at: string }>("/asterisk/routes/apply", {
    method: "POST",
  });
}

// Extension passwords travel one way only: they are sent here and never come
// back, so an entry with no password keeps whatever is already stored.
export function getAsteriskExtensions() {
  return api<AsteriskExtensions>("/asterisk/extensions");
}

export function saveAsteriskExtensions(extensions: AsteriskExtension[], replaceSeeded = false) {
  return api<{ saved: boolean; written: boolean; extensions: AsteriskExtension[]; preview: string }>(
    "/asterisk/extensions",
    { method: "PUT", body: { extensions, replaceSeeded } },
  );
}

// Where a call arriving on a SIM rings. Validated against the configured
// extensions server-side: a ring group naming an account that does not exist
// rings nothing, and a caller who reaches no one is the only other signal.
export function saveAsteriskInbound(inbound: AsteriskInbound) {
  return api<{ saved: boolean; written: boolean; inbound: AsteriskInbound; inboundPreview: string }>(
    "/asterisk/inbound",
    { method: "PUT", body: inbound },
  );
}

// Whether SMS crosses the trunk at all. Saving rewrites the generated SMS
// dialplan, which is built from the extension list as well as the mode, so
// the server refuses a mode it cannot render a working dialplan for.
export function saveAsteriskSMS(sms: AsteriskSMS) {
  return api<{ saved: boolean; written: boolean; sms: AsteriskSMS; smsPreview: string; trunkHost: string }>(
    "/asterisk/sms",
    { method: "PUT", body: sms },
  );
}

// Only the texts that involved an extension. The SMS page already shows
// everything a SIM sent or received, and repeating it here would bury the
// handful of messages this tab exists for.
export function getAsteriskSMSHistory() {
  return api<{ messages: AsteriskSMSMessage[]; limit: number }>("/asterisk/sms/history");
}

// The numbers VoCat already learned from each SIM's IMS registration, so an
// extension per SIM does not mean copying digits between two screens.
export function getAsteriskExtensionCandidates() {
  return api<{ candidates: AsteriskExtensionCandidate[] }>("/asterisk/extensions/candidates");
}

// Applying reloads res_pjsip, which is what owns endpoints, auths and AORs.
// Reloading the dialplan instead would report success and change nothing.
export function applyAsteriskExtensions() {
  return api<{ applied: boolean; message?: string; at: string; note?: string }>(
    "/asterisk/extensions/apply",
    { method: "POST" },
  );
}

// External SIP trunks. The credential is write-only like an extension's: a
// trunk sent with no password keeps whatever is already stored.
export function getAsteriskTrunks() {
  return api<AsteriskTrunks>("/asterisk/trunks");
}

export function saveAsteriskTrunks(trunks: AsteriskTrunk[]) {
  return api<{ saved: boolean; written: boolean; trunks: AsteriskTrunk[]; preview: string }>(
    "/asterisk/trunks",
    { method: "PUT", body: { trunks } },
  );
}

// Applying reloads res_pjsip and pbx_config, because one save writes a PJSIP
// object list and a dialplan.
export function applyAsteriskTrunks() {
  return api<{ applied: boolean; at: string }>("/asterisk/trunks/apply", { method: "POST" });
}

// Digits go into a live call as RFC 4733 telephone events, over the call's own
// RTP stream. They cannot be sent as audio tones: an IMS call usually
// negotiates AMR, which mangles a pair of pure tones past recognition.
export function sendCallDTMF(deviceId: string, callId: string, digits: string) {
  return api<{ sent: boolean; digits: string; callId: string }>(
    `/devices/${encodeURIComponent(deviceId)}/calls/dtmf`,
    { method: "POST", body: { callId, digits } },
  );
}

// Call history. Read-only by design: records are written by observing calls,
// and an API that could edit them would make the history a claim rather than
// a record.
export function listCallRecords(params: {
  deviceId?: string;
  direction?: string;
  disposition?: string;
  search?: string;
  limit?: number;
  offset?: number;
} = {}) {
  const query = new URLSearchParams();
  if (params.deviceId) query.set("device_id", params.deviceId);
  if (params.direction) query.set("direction", params.direction);
  if (params.disposition) query.set("disposition", params.disposition);
  if (params.search) query.set("search", params.search);
  if (params.limit) query.set("limit", String(params.limit));
  if (params.offset) query.set("offset", String(params.offset));
  const suffix = query.toString();
  return api<CallRecordsResponse>(`/calls/records${suffix ? `?${suffix}` : ""}`);
}

export function listCalls(deviceId: string) {
  return api<CallsResponse>(`/devices/${encodeURIComponent(deviceId)}/calls`);
}

// durationSeconds arms a server-side automatic hang-up: 0 disables it, and the
// server rejects anything above 600.
export function dialCall(deviceId: string, number: string, durationSeconds = 0) {
  return api<{ callId: string; call?: Call; transport: string }>(
    `/devices/${encodeURIComponent(deviceId)}/calls/dial`,
    { method: "POST", body: { number, durationSeconds } },
  );
}

export function answerCall(deviceId: string, callId: string) {
  return api<{ callId: string; transport: string }>(
    `/devices/${encodeURIComponent(deviceId)}/calls/answer`,
    { method: "POST", body: { callId } },
  );
}

export function hangupCall(deviceId: string, callId: string) {
  return api<{ callId: string; transport: string }>(
    `/devices/${encodeURIComponent(deviceId)}/calls/hangup`,
    { method: "POST", body: { callId } },
  );
}

// The media bridge is a WebSocket, so it cannot go through api(): it carries
// the session cookie automatically and is same-origin checked by the server.
export function callMediaURL(deviceId: string, callId: string) {
  const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
  const query = new URLSearchParams({ call_id: callId });
  return `${scheme}//${window.location.host}/api/devices/${encodeURIComponent(deviceId)}/calls/media?${query}`;
}

// The same endpoint over http(s). A WebSocket upgrade that the server refuses
// gives script no status, so this fetches the identical URL to read the error
// body the handshake discarded.
export function callMediaProbeURL(deviceId: string, callId: string) {
  const query = new URLSearchParams({ call_id: callId });
  return `/api/devices/${encodeURIComponent(deviceId)}/calls/media?${query}`;
}

export function apiMessage(error: unknown) {
  if (error instanceof ApiError) {
    const suffix = error.requestId ? `（${tl("请求")} ${error.requestId}）` : "";
    return `${error.message}${suffix}`;
  }
  if (error instanceof Error) return error.message;
  return tl("请求未完成，检查服务状态后重试");
}

export function eventStreamURL(path: string, params?: URLSearchParams) {
  const suffix = params?.toString();
  return `${path.startsWith("/api") ? path : `/api${path}`}${suffix ? `?${suffix}` : ""}`;
}
