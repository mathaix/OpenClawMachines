// Pure unit tests for utility functions — no worker instance needed
import { describe, it, expect } from "vitest";
import {
  extractAccountSlug,
  extractMachinePath,
  getBaseDomain,
  isAllowedOrigin,
} from "../worker.js";

describe("getBaseDomain", () => {
  it("normalizes case and surrounding dots", () => {
    expect(getBaseDomain({ BASE_DOMAIN: ".Example.Com." })).toBe("example.com");
  });

  it("falls back when the configured domain is blank after normalization", () => {
    expect(getBaseDomain({ BASE_DOMAIN: "..." })).toBe("openclawmachines.com");
  });
});

describe("extractAccountSlug", () => {
  it("extracts valid single-level subdomain", () => {
    expect(extractAccountSlug("myteam.openclawmachines.com")).toBe("myteam");
  });

  it("extracts valid single-level subdomain for custom base domain", () => {
    expect(extractAccountSlug("myteam.example.com", "example.com")).toBe("myteam");
  });

  it("extracts numeric subdomain", () => {
    expect(extractAccountSlug("user-9.openclawmachines.com")).toBe("user-9");
  });

  it("returns null for www", () => {
    expect(extractAccountSlug("www.openclawmachines.com")).toBeNull();
  });

  it("returns null for api", () => {
    expect(extractAccountSlug("api.openclawmachines.com")).toBeNull();
  });

  it("returns null for ocm-host-* tunnel subdomains", () => {
    expect(
      extractAccountSlug("ocm-host-1770396856130.openclawmachines.com")
    ).toBeNull();
  });

  it("returns null for m-* per-VM tunnel subdomains", () => {
    expect(extractAccountSlug("m-xjngyk0.openclawmachines.com")).toBeNull();
  });

  it("returns null for ssh-* per-VM tunnel subdomains", () => {
    expect(extractAccountSlug("ssh-xjngyk0.openclawmachines.com")).toBeNull();
  });

  it("returns null for bare domain", () => {
    expect(extractAccountSlug("openclawmachines.com")).toBeNull();
  });

  it("returns null for external domain", () => {
    expect(extractAccountSlug("other.com")).toBeNull();
  });

  it("returns null for multi-level subdomain", () => {
    expect(extractAccountSlug("a.b.openclawmachines.com")).toBeNull();
  });

  it("returns null for empty string", () => {
    expect(extractAccountSlug("")).toBeNull();
  });
});

describe("extractMachinePath", () => {
  it("extracts machine slug and subpath", () => {
    expect(extractMachinePath("/my-machine/files")).toEqual({
      machineSlug: "my-machine",
      subPath: "/files",
    });
  });

  it("handles query strings in subpath", () => {
    expect(extractMachinePath("/my-machine/files?path=/")).toEqual({
      machineSlug: "my-machine",
      subPath: "/files?path=/",
    });
  });

  it("returns null for root path", () => {
    expect(extractMachinePath("/")).toBeNull();
  });

  it("returns slug with default subpath for slug-only path", () => {
    expect(extractMachinePath("/my-machine")).toEqual({
      machineSlug: "my-machine",
      subPath: "/",
    });
  });

  it("handles deeply nested paths", () => {
    expect(extractMachinePath("/my-machine/api/v1/data")).toEqual({
      machineSlug: "my-machine",
      subPath: "/api/v1/data",
    });
  });

  it("returns null for empty string", () => {
    expect(extractMachinePath("")).toBeNull();
  });
});

describe("isAllowedOrigin", () => {
  it("allows https://openclawmachines.com", () => {
    expect(isAllowedOrigin("https://openclawmachines.com")).toBe(true);
  });

  it("allows https://www.openclawmachines.com", () => {
    expect(isAllowedOrigin("https://www.openclawmachines.com")).toBe(true);
  });

  it("allows https subdomain", () => {
    expect(isAllowedOrigin("https://acme.openclawmachines.com")).toBe(true);
  });

  it("allows https custom-domain subdomain", () => {
    expect(isAllowedOrigin("https://acme.example.com", "example.com")).toBe(true);
  });

  it("rejects http (insecure)", () => {
    expect(isAllowedOrigin("http://openclawmachines.com")).toBe(false);
  });

  it("rejects external domain", () => {
    expect(isAllowedOrigin("https://evil.com")).toBe(false);
  });

  it("rejects null origin", () => {
    expect(isAllowedOrigin(null)).toBe(false);
  });

  it("rejects undefined origin", () => {
    expect(isAllowedOrigin(undefined)).toBe(false);
  });

  it("rejects suffix attack (evilopenclawmachines.com)", () => {
    expect(isAllowedOrigin("https://evilopenclawmachines.com")).toBe(false);
  });
});
