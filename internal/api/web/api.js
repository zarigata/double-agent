// api.js is the client of the control-plane REST API.
//
// The browser authenticates with an HttpOnly session cookie, so the client
// never sees a bearer token. Every state changing request carries the CSRF
// token of the session in the X-CSRF-Token header; the token is kept in
// memory only and is never written to localStorage.

import { t } from "./i18n.js";

const API_BASE = "/api/v1";

let csrfToken = "";

// unauthorizedHandlers are called when the server refuses the session.
const unauthorizedHandlers = new Set();

export class ApiError extends Error {
  constructor(message, details = {}) {
    super(message);
    this.name = "ApiError";
    this.code = details.code || "unknown";
    this.status = details.status || 0;
    this.correlationId = details.correlationId || "";
    this.retry = Boolean(details.retry);
    this.details = details.details || null;
  }
}

// onUnauthorized registers a callback for an expired session.
export function onUnauthorized(handler) {
  unauthorizedHandlers.add(handler);
  return () => unauthorizedHandlers.delete(handler);
}

// readInjectedToken reads the CSRF token that the daemon injected into the
// page. The token may live in a meta tag or in a global; the first one that
// carries a value wins.
export function readInjectedToken() {
  const meta = document.querySelector('meta[name="csrf-token"]');
  const fromMeta = meta?.getAttribute("content")?.trim();
  if (fromMeta) return fromMeta;

  const global = typeof window === "undefined" ? "" : window.__DOUBLE_AGENT_CSRF__;
  return typeof global === "string" ? global.trim() : "";
}

// setCsrfToken keeps the token of the current session in memory.
export function setCsrfToken(token) {
  if (typeof token === "string" && token.trim()) csrfToken = token.trim();
}

export function getCsrfToken() {
  return csrfToken;
}

// request performs one call against the API.
//
// Every failure leaves the client as an ApiError that carries the stable
// error code and the correlation identifier of the request, so the views can
// report the failure without guessing.
async function request(path, options = {}) {
  const { method = "GET", body, signal, headers: extraHeaders, raw = false } = options;
  const headers = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET" && method !== "HEAD" && method !== "OPTIONS") {
    const token = getCsrfToken() || readInjectedToken();
    if (token) headers["X-CSRF-Token"] = token;
  }
  Object.assign(headers, extraHeaders || {});

  let response;
  try {
    response = await fetch(`${API_BASE}${path}`, {
      method,
      headers,
      credentials: "same-origin",
      body: body === undefined ? undefined : JSON.stringify(body),
      signal,
    });
  } catch (cause) {
    if (cause?.name === "AbortError") throw cause;
    throw new ApiError(t("errors.network"), { code: "network_error" });
  }

  const correlationId = response.headers.get("X-Request-Id") || "";
  if (response.status === 401) {
    for (const handler of unauthorizedHandlers) handler();
  }
  if (!response.ok) throw await toApiError(response, correlationId);
  if (raw) return response;
  if (response.status === 204) return null;

  const contentType = response.headers.get("Content-Type") || "";
  if (!contentType.includes("application/json")) return null;
  return response.json();
}

async function toApiError(response, correlationId) {
  let payload = null;
  try {
    payload = await response.json();
  } catch {
    // The body is not JSON; the status carries the only information left.
  }
  const envelope = payload?.error || {};
  return new ApiError(envelope.message || response.statusText || t("errors.unknown"), {
    code: envelope.code || `http_${response.status}`,
    status: response.status,
    correlationId: envelope.correlation_id || correlationId,
    retry: Boolean(envelope.retry),
    details: envelope.details || null,
  });
}

// list reads a collection from a payload that may be wrapped or bare.
function list(payload, key) {
  if (Array.isArray(payload)) return payload;
  if (Array.isArray(payload?.[key])) return payload[key];
  return [];
}

// one reads a record from a payload that may be wrapped or bare.
//
// The daemon serves some records bare and others wrapped in an envelope;
// the views should not have to care which one they received.
function one(payload, key) {
  if (payload && typeof payload === "object" && payload[key] && typeof payload[key] === "object") {
    return payload[key];
  }
  return payload && typeof payload === "object" ? payload : {};
}

export const api = {
  async login(username, password) {
    const payload = await request("/auth/login", {
      method: "POST",
      body: { username, password },
    });
    setCsrfToken(payload?.csrf_token || "");
    return {
      user: one(payload, "user"),
      csrfToken: payload?.csrf_token || "",
      scopes: Array.isArray(payload?.scopes) ? payload.scopes : [],
    };
  },

  async logout() {
    return request("/auth/logout", { method: "POST" });
  },

  async me() {
    const payload = await request("/auth/me");
    setCsrfToken(payload?.csrf_token || "");
    return {
      user: one(payload, "user"),
      scopes: Array.isArray(payload?.scopes) ? payload.scopes : [],
    };
  },

  async capabilities() {
    const payload = await request("/capabilities");
    return {
      runtimes: list(payload, "runtimes"),
      templates: list(payload, "templates"),
      limits: payload?.limits || {},
      network: payload?.network || {},
      host: payload?.host || null,
    };
  },

  async templates() {
    return list(await request("/templates"), "templates");
  },

  async template(id, version) {
    const query = version ? `?version=${encodeURIComponent(version)}` : "";
    return one(await request(`/templates/${encodeURIComponent(id)}${query}`), "template");
  },

  async instances() {
    return list(await request("/instances"), "instances");
  },

  async instance(id) {
    return one(await request(`/instances/${encodeURIComponent(id)}`), "instance");
  },

  async createInstance(payload) {
    const body = {
      template_id: payload.templateId,
      template_version: payload.templateVersion || undefined,
      name: payload.name || undefined,
      env: payload.env || undefined,
      ttl_seconds: payload.ttlSeconds || undefined,
      secret_refs: payload.secretRefs?.length ? payload.secretRefs : undefined,
      idempotency_key: payload.idempotencyKey || undefined,
    };
    const answer = await request("/instances", { method: "POST", body });
    const job = one(answer, "job");
    return {
      jobId: answer?.job_id || job?.id || "",
      instanceId: answer?.instance_id || job?.instance_id || "",
      state: answer?.state || job?.state || "",
      job,
    };
  },

  async exec(id, payload) {
    const body = {
      argv: payload.argv,
      cwd: payload.cwd || undefined,
      env: payload.env && Object.keys(payload.env).length ? payload.env : undefined,
      timeout_seconds: payload.timeoutSeconds || undefined,
    };
    return request(`/instances/${encodeURIComponent(id)}/exec`, { method: "POST", body });
  },

  async stopInstance(id, graceSeconds) {
    return request(`/instances/${encodeURIComponent(id)}/stop`, {
      method: "POST",
      body: { grace_seconds: graceSeconds || undefined },
    });
  },

  async destroyInstance(id) {
    return request(`/instances/${encodeURIComponent(id)}`, { method: "DELETE" });
  },

  async console(id) {
    return one(await request(`/instances/${encodeURIComponent(id)}/console`), "console");
  },

  async jobs() {
    return list(await request("/jobs"), "jobs");
  },

  async job(id) {
    return one(await request(`/jobs/${encodeURIComponent(id)}`), "job");
  },

  async cancelJob(id) {
    return request(`/jobs/${encodeURIComponent(id)}/cancel`, { method: "POST" });
  },

  async artifacts() {
    return list(await request("/artifacts"), "artifacts");
  },

  async users() {
    return list(await request("/admin/users"), "users");
  },

  async createUser(payload) {
    return request("/admin/users", {
      method: "POST",
      body: { username: payload.username, password: payload.password, role: payload.role || "user" },
    });
  },

  async disableUser(id) {
    return request(`/admin/users/${encodeURIComponent(id)}/disable`, { method: "POST" });
  },

  async keys() {
    return list(await request("/admin/keys"), "keys");
  },

  async createKey(payload) {
    const answer = await request("/admin/keys", {
      method: "POST",
      body: {
        name: payload.name,
        scopes: payload.scopes?.length ? payload.scopes : undefined,
        expires_at: payload.expiresAt || undefined,
        expires_in_seconds: payload.expiresInSeconds || undefined,
      },
    });
    const key = one(answer, "key");
    return { key, secret: answer?.secret || key?.secret || "" };
  },

  async revokeKey(id) {
    return request(`/admin/keys/${encodeURIComponent(id)}`, { method: "DELETE" });
  },

  async audit() {
    return list(await request("/audit"), "events");
  },

  async health() {
    return request("/healthz");
  },
};

// artifactDownloadURL builds the link of a file.
//
// The link is a plain GET of an authenticated endpoint: the session cookie
// travels with it, so no capability token is put into the address.
export function artifactDownloadURL(id) {
  return `${API_BASE}/artifacts/${encodeURIComponent(id)}/download`;
}
