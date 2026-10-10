import { describe, expect, it } from "vitest";

import { parseGetIpQuery, validateIpInput } from "../../src/shared/ip";

describe("validateIpInput", () => {
  it("accepts and normalizes IPv4 input", () => {
    const result = validateIpInput({
      ip: " 001.002.003.004 ",
      port: 443,
      asn: 906,
      region: " US ",
    });

    expect(result).toEqual({
      ok: true,
      value: {
        ip: "1.2.3.4",
        port: 443,
        asn: 906,
        region: "US",
      },
    });
  });

  it("accepts and normalizes IPv6 input", () => {
    const result = validateIpInput({
      ip: " 2001:0DB8:0:0:0:0:0:1 ",
      port: 1,
      asn: 1,
      region: "JP",
    });

    expect(result).toEqual({
      ok: true,
      value: {
        ip: "2001:db8::1",
        port: 1,
        asn: 1,
        region: "JP",
      },
    });
  });

  it.each([1, 65535])("accepts port boundary %s", (port) => {
    expect(
      validateIpInput({ ip: "1.2.3.4", port, asn: 906, region: "US" }).ok,
    ).toBe(true);
  });

  it.each([0, -1, 65536, 443.5, "443", Number.NaN, Number.POSITIVE_INFINITY])(
    "rejects invalid port %s",
    (port) => {
      expect(
        validateIpInput({ ip: "1.2.3.4", port, asn: 906, region: "US" }).ok,
      ).toBe(false);
    },
  );

  it.each([1, 906, 64512])("accepts positive integer ASN %s", (asn) => {
    expect(
      validateIpInput({ ip: "1.2.3.4", port: 443, asn, region: "US" }).ok,
    ).toBe(true);
  });

  it.each([0, -1, 906.5, "906", Number.NaN, Number.POSITIVE_INFINITY])(
    "rejects invalid ASN %s",
    (asn) => {
      expect(
        validateIpInput({ ip: "1.2.3.4", port: 443, asn, region: "US" }).ok,
      ).toBe(false);
    },
  );

  it.each([
    null,
    undefined,
    [],
    {},
    { ip: "1.2.3.4", port: 443, asn: 906 },
    { ip: "1.2.3.4", port: 443, region: "US" },
    { ip: "1.2.3.4", asn: 906, region: "US" },
    { ip: "1.2.3.4", port: 443, asn: 906, region: "" },
  ])("rejects missing or invalid required fields: %o", (input) => {
    expect(validateIpInput(input).ok).toBe(false);
  });

  it.each(["bad", "1.2.3.999", "2001:db8::1::2", 1234, null])(
    "rejects invalid IP %o",
    (ip) => {
      expect(
        validateIpInput({ ip, port: 443, asn: 906, region: "US" }).ok,
      ).toBe(false);
    },
  );

  it.each(["US West", "US\tWest", "US\nWest", "   "])(
    "rejects region containing whitespace: %o",
    (region) => {
      expect(
        validateIpInput({ ip: "1.2.3.4", port: 443, asn: 906, region }).ok,
      ).toBe(false);
    },
  );

  it("accepts an optional non-empty alias and trims it", () => {
    const result = validateIpInput({
      ip: "1.2.3.4",
      port: 443,
      asn: 906,
      region: "US",
      asnAlias: " DMIT ",
    });

    expect(result).toEqual({
      ok: true,
      value: {
        ip: "1.2.3.4",
        port: 443,
        asn: 906,
        region: "US",
        asnAlias: "DMIT",
      },
    });
  });

  it.each(["", "   ", "DMIT CDN", "DMIT\tCDN", null, 906])(
    "rejects invalid optional alias: %o",
    (asnAlias) => {
      expect(
        validateIpInput({
          ip: "1.2.3.4",
          port: 443,
          asn: 906,
          region: "US",
          asnAlias,
        }).ok,
      ).toBe(false);
    },
  );
});

describe("parseGetIpQuery", () => {
  it("uses an unfiltered query with the default limit", () => {
    expect(parseGetIpQuery(new URLSearchParams())).toEqual({
      ok: true,
      value: { asn: null, limit: 10 },
    });
  });

  it("parses ASN and clamps the limit to 20", () => {
    expect(
      parseGetIpQuery(new URLSearchParams("asn=906&limit=25")),
    ).toEqual({
      ok: true,
      value: { asn: 906, limit: 20 },
    });
  });

  it.each(["", "0", "-1", "1.5", "abc", " 5"])(
    "rejects invalid limit %j",
    (limit) => {
      expect(parseGetIpQuery(new URLSearchParams({ limit })).ok).toBe(false);
    },
  );

  it.each(["", "0", "-1", "906.5", "abc", " 906"])(
    "rejects invalid ASN %j",
    (asn) => {
      expect(parseGetIpQuery(new URLSearchParams({ asn })).ok).toBe(false);
    },
  );
});
