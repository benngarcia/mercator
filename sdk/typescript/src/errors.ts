import type { ErrorResponse, Violation } from "./types.js";

export type MercatorRequestInfo = {
  method: string;
  path: string;
  url: string;
};

export class MercatorAPIError extends Error {
  readonly code: string;
  readonly details?: Violation[];
  readonly request: MercatorRequestInfo;
  readonly responseBody: unknown;
  readonly status: number;

  constructor(message: string, options: {
    code: string;
    details?: Violation[];
    request: MercatorRequestInfo;
    responseBody: unknown;
    status: number;
  }) {
    super(message);
    this.name = "MercatorAPIError";
    this.code = options.code;
    this.details = options.details;
    this.request = options.request;
    this.responseBody = options.responseBody;
    this.status = options.status;
  }
}

export class MercatorRequestError extends Error {
  readonly cause: unknown;
  readonly request: MercatorRequestInfo;

  constructor(message: string, options: { cause: unknown; request: MercatorRequestInfo }) {
    super(message);
    this.name = "MercatorRequestError";
    this.cause = options.cause;
    this.request = options.request;
  }
}

export function errorResponseFromBody(body: unknown): ErrorResponse | undefined {
  if (!isObject(body)) {
    return undefined;
  }
  const code = body["code"];
  const message = body["message"];
  if (typeof code !== "string" || typeof message !== "string") {
    return undefined;
  }
  const details = Array.isArray(body["details"]) ? body["details"].filter(isViolation) : undefined;
  return { code, message, details };
}

function isViolation(value: unknown): value is Violation {
  if (!isObject(value)) {
    return false;
  }
  return typeof value["code"] === "string" && typeof value["message"] === "string";
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
