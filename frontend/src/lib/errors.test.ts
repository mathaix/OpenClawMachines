import { describe, it, expect } from "vitest";
import { ApiError } from "./errors";

describe("ApiError", () => {
  it("extends Error and has correct name", () => {
    const err = new ApiError("not found", "not_found", 404);
    expect(err).toBeInstanceOf(Error);
    expect(err.name).toBe("ApiError");
    expect(err.message).toBe("not found");
  });

  it("exposes code, status, and retryable", () => {
    const err = new ApiError("overloaded", "rate_limit", 429, true);
    expect(err.code).toBe("rate_limit");
    expect(err.status).toBe(429);
    expect(err.retryable).toBe(true);
  });

  it("defaults retryable to false", () => {
    const err = new ApiError("bad input", "validation", 400);
    expect(err.retryable).toBe(false);
  });
});
