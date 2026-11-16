/**
 * Small typed fetch wrapper used by every API module (`checkout.ts`,
 * `admin.ts`, `merchant.ts`).
 *
 * - JSON in / JSON out; 204 resolves to `undefined`.
 * - Non-2xx responses throw `ApiError` carrying the backend error envelope
 *   (`code`, `message`, `fields`) and the HTTP `status`.
 * - Network failures throw `ApiError` with `status: 0` and code
 *   `"network_error"`. Aborts are re-thrown untouched (`AbortError`).
 * - Mutations (anything but GET/HEAD) send `X-Requested-With: cryptopay`
 *   (the admin API's CSRF guard).
 * - `credentials: "same-origin"` so the admin session cookie is sent; the
 *   browser always talks to the Next.js origin, which proxies `/api/*`.
 */
import type { ApiErrorBody, ApiErrorCode } from "./types";

export type HttpMethod = "GET" | "HEAD" | "POST" | "PUT" | "PATCH" | "DELETE";

export type QueryValue = string | number | boolean | null | undefined;
export type Query = Record<string, QueryValue>;

/** Client-side error codes in addition to the backend's `ApiErrorCode`s. */
export type ClientErrorCode = "network_error" | "invalid_response" | "http_error";

export class ApiError extends Error {
  override readonly name = "ApiError";
  /** HTTP status; 0 when the request never got a response. */
  readonly status: number;
  readonly code: ApiErrorCode | ClientErrorCode | (string & {});
  /** Per-field validation messages (400/422). */
  readonly fields: Record<string, string> | undefined;

  constructor(init: {
    status: number;
    code: ApiError["code"];
    message: string;
    fields?: Record<string, string>;
    cause?: unknown;
  }) {
    super(init.message, init.cause === undefined ? undefined : { cause: init.cause });
    this.status = init.status;
    this.code = init.code;
    this.fields = init.fields;
  }

  get isNetworkError(): boolean {
    return this.status === 0;
  }
  get isNotFound(): boolean {
    return this.status === 404 || this.code === "not_found";
  }
  get isUnauthorized(): boolean {
    return this.status === 401 || this.code === "unauthorized";
  }
  get isForbidden(): boolean {
    return this.status === 403 || this.code === "forbidden";
  }
  get isConflict(): boolean {
    return this.status === 409 || this.code === "conflict";
  }
  get isRateLimited(): boolean {
    return this.status === 429 || this.code === "rate_limited";
  }
  /** Worth retrying later without user action (network, 429, 5xx). */
  get isTransient(): boolean {
    return this.status === 0 || this.status === 429 || this.status >= 500;
  }
}

export function isApiError(e: unknown): e is ApiError {
  return e instanceof ApiError;
}

export function isAbortError(e: unknown): boolean {
  return (
    typeof e === "object" &&
    e !== null &&
    "name" in e &&
    (e as { name: unknown }).name === "AbortError"
  );
}

/** Fallback code when the body is not the documented error envelope. */
export function codeForStatus(status: number): ApiError["code"] {
  switch (status) {
    case 400:
      return "invalid_request";
    case 401:
      return "unauthorized";
    case 403:
      return "forbidden";
    case 404:
      return "not_found";
    case 409:
      return "conflict";
    case 422:
      return "unprocessable";
    case 429:
      return "rate_limited";
    case 502:
    case 503:
    case 504:
      return "unavailable";
    default:
      return status >= 500 ? "internal" : "http_error";
  }
}

function isErrorBody(v: unknown): v is ApiErrorBody {
  if (typeof v !== "object" || v === null || !("error" in v)) return false;
  const err = (v as { error: unknown }).error;
  return (
    typeof err === "object" &&
    err !== null &&
    typeof (err as { code?: unknown }).code === "string"
  );
}

export function buildQuery(query?: Query): string {
  if (!query) return "";
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === "") continue;
    params.append(key, String(value));
  }
  const s = params.toString();
  return s ? `?${s}` : "";
}

/** Encodes one path segment (`/api/checkout/${seg(id)}`). */
export function seg(value: string | number): string {
  return encodeURIComponent(String(value));
}

export interface RequestOptions {
  query?: Query;
  /** Serialised as JSON. */
  body?: unknown;
  headers?: Record<string, string>;
  signal?: AbortSignal;
}

export interface ApiClientOptions {
  /** Prefix for every path; "" (default) = same origin. */
  baseUrl?: string;
  /** Injected for tests / server-side use. Defaults to the global fetch. */
  fetch?: typeof fetch;
  /** Headers added to every request (e.g. a merchant API key). */
  headers?: Record<string, string>;
}

export interface ApiClient {
  request<T>(method: HttpMethod, path: string, options?: RequestOptions): Promise<T>;
  get<T>(path: string, query?: Query, options?: Omit<RequestOptions, "query">): Promise<T>;
  post<T>(path: string, body?: unknown, options?: Omit<RequestOptions, "body">): Promise<T>;
  put<T>(path: string, body?: unknown, options?: Omit<RequestOptions, "body">): Promise<T>;
  patch<T>(path: string, body?: unknown, options?: Omit<RequestOptions, "body">): Promise<T>;
  delete<T>(path: string, options?: RequestOptions): Promise<T>;
}

export const CSRF_HEADER = "X-Requested-With";
export const CSRF_VALUE = "cryptopay";

export function createApiClient(opts: ApiClientOptions = {}): ApiClient {
  const baseUrl = (opts.baseUrl ?? "").replace(/\/+$/, "");

  async function request<T>(
    method: HttpMethod,
    path: string,
    options: RequestOptions = {},
  ): Promise<T> {
    const doFetch = opts.fetch ?? globalThis.fetch;
    const headers: Record<string, string> = {
      Accept: "application/json",
      ...opts.headers,
      ...options.headers,
    };
    const init: RequestInit = {
      method,
      headers,
      credentials: "same-origin",
      cache: "no-store",
      signal: options.signal,
    };
    if (method !== "GET" && method !== "HEAD") {
      headers[CSRF_HEADER] = CSRF_VALUE;
    }
    if (options.body !== undefined) {
      headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(options.body);
    }

    const url = `${baseUrl}${path}${buildQuery(options.query)}`;
    let res: Response;
    try {
      res = await doFetch(url, init);
    } catch (e) {
      if (isAbortError(e)) throw e;
      throw new ApiError({
        status: 0,
        code: "network_error",
        message: e instanceof Error ? e.message : "Network request failed",
        cause: e,
      });
    }

    if (res.status === 204 || res.status === 205 || method === "HEAD") {
      return undefined as T;
    }

    const text = await res.text();
    let data: unknown = undefined;
    let parsed = false;
    if (text.length > 0) {
      try {
        data = JSON.parse(text);
        parsed = true;
      } catch {
        parsed = false;
      }
    }

    if (!res.ok) {
      if (parsed && isErrorBody(data)) {
        throw new ApiError({
          status: res.status,
          code: data.error.code,
          message: data.error.message || res.statusText || `HTTP ${res.status}`,
          fields: data.error.fields,
        });
      }
      throw new ApiError({
        status: res.status,
        code: codeForStatus(res.status),
        message: res.statusText || `HTTP ${res.status}`,
      });
    }

    if (text.length === 0) return undefined as T;
    if (!parsed) {
      throw new ApiError({
        status: res.status,
        code: "invalid_response",
        message: "Response is not valid JSON",
      });
    }
    return data as T;
  }

  return {
    request,
    get: (path, query, options) => request("GET", path, { ...options, query }),
    post: (path, body, options) => request("POST", path, { ...options, body }),
    put: (path, body, options) => request("PUT", path, { ...options, body }),
    patch: (path, body, options) => request("PATCH", path, { ...options, body }),
    delete: (path, options) => request("DELETE", path, options),
  };
}

/** Default same-origin client used by the browser. */
export const apiClient: ApiClient = createApiClient();
