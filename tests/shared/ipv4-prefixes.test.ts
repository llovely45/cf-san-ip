import { describe, expect, it } from "vitest";

import { isIPv4CIDRWithinCIDR, isIPv4ScanBlock, isIPv4WithinCIDR, normalizeIPv4Prefixes, planIPv4Blocks } from "../../src/shared/ipv4-prefixes";

describe("planIPv4Blocks", () => {
  it("splits a wider prefix into ascending /24 blocks", () => {
    expect(planIPv4Blocks(64512, ["8.8.0.0/23"])).toEqual([
      { asn: 64512, sourcePrefix: "8.8.0.0/23", blockCidr: "8.8.0.0/24" },
      { asn: 64512, sourcePrefix: "8.8.0.0/23", blockCidr: "8.8.1.0/24" },
    ]);
  });

  it.each([
    ["8.8.8.0/24", "8.8.8.0/24"],
    ["8.8.8.128/25", "8.8.8.128/25"],
  ])("keeps narrower prefix %s intact", (prefix, blockCidr) => {
    expect(planIPv4Blocks(64512, [prefix])).toEqual([
      { asn: 64512, sourcePrefix: prefix, blockCidr },
    ]);
  });

  it("sorts numerically and deterministically deduplicates overlapping prefixes", () => {
    expect(
      planIPv4Blocks(64512, ["8.8.1.0/24", "8.8.0.0/23"]),
    ).toEqual([
      { asn: 64512, sourcePrefix: "8.8.0.0/23", blockCidr: "8.8.0.0/24" },
      { asn: 64512, sourcePrefix: "8.8.1.0/24", blockCidr: "8.8.1.0/24" },
    ]);
  });

  it.each([
    "10.0.0.0/8",
    "100.64.0.0/10",
    "127.0.0.0/8",
    "169.254.0.0/16",
    "172.16.0.0/12",
    "192.0.2.0/24",
    "192.168.0.0/16",
    "198.18.0.0/15",
    "224.0.0.0/4",
    "240.0.0.0/4",
    "8.0.0.0/5",
  ])("rejects non-public or mixed-scope prefix %s", (prefix) => {
    expect(planIPv4Blocks(64512, [prefix])).toEqual([]);
  });

  it.each(["not-a-prefix", "8.8.8.8", "2001:db8::/32", "8.8.8.0/33"])(
    "rejects malformed or non-IPv4 prefix %s",
    (prefix) => {
      expect(planIPv4Blocks(64512, [prefix])).toEqual([]);
    },
  );

  it.each([0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1])(
    "throws for invalid ASN %s",
    (asn) => {
      expect(() => planIPv4Blocks(asn, ["8.8.8.0/24"])).toThrow(
        "ASN must be a positive safe integer",
      );
    },
  );
});


describe("isIPv4WithinCIDR", () => {
  it("checks canonical IPv4 membership and rejects IPv6 or malformed inputs", () => {
    expect(isIPv4WithinCIDR("8.8.8.255", "8.8.8.0/24")).toBe(true);
    expect(isIPv4WithinCIDR("8.8.9.1", "8.8.8.0/24")).toBe(false);
    expect(isIPv4WithinCIDR("2001:db8::1", "8.8.8.0/24")).toBe(false);
    expect(isIPv4WithinCIDR("bad", "8.8.8.0/24")).toBe(false);
  });
});


describe("normalizeIPv4Prefixes", () => {
  it("keeps the complete public source-prefix set compact and canonical", () => {
    expect(normalizeIPv4Prefixes(["8.8.1.0/24", "8.8.0.1/23", "8.8.1.0/24", "10.0.0.0/8"]))
      .toEqual(["8.8.0.0/23", "8.8.1.0/24"]);
  });
});

describe("isIPv4CIDRWithinCIDR", () => {
  it("checks complete CIDR containment rather than only the network address", () => {
    expect(isIPv4CIDRWithinCIDR("8.8.8.0/24", "8.8.0.0/16")).toBe(true);
    expect(isIPv4CIDRWithinCIDR("8.8.0.0/16", "8.8.8.0/24")).toBe(false);
    expect(isIPv4CIDRWithinCIDR("8.8.0.0/23", "8.8.0.0/24")).toBe(false);
  });
});


describe("isIPv4ScanBlock", () => {
  it("accepts only aligned public blocks of the planner's size within the source", () => {
    expect(isIPv4ScanBlock("8.8.0.0/23", "8.8.0.0/24")).toBe(true);
    expect(isIPv4ScanBlock("8.8.0.0/23", "8.8.1.0/24")).toBe(true);
    expect(isIPv4ScanBlock("8.8.0.0/23", "8.8.0.0/23")).toBe(false);
    expect(isIPv4ScanBlock("8.8.0.0/24", "8.8.0.0/25")).toBe(false);
    expect(isIPv4ScanBlock("8.8.0.1/23", "8.8.0.0/24")).toBe(false);
    expect(isIPv4ScanBlock("10.0.0.0/24", "10.0.0.0/24")).toBe(false);
  });
});
