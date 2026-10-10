import { describe, expect, it } from "vitest";

import { formatGetIpOutput } from "../../src/shared/output";

describe("formatGetIpOutput", () => {
  it("formats an aliased record without a trailing newline", () => {
    expect(
      formatGetIpOutput([
        { ip: "1.2.3.4", port: 443, asn: 906, region: "US", asnAlias: "DMIT" },
      ]),
    ).toBe("1.2.3.4:443#DMIT US 01");
  });

  it("falls back to the ASN when no alias is present", () => {
    expect(
      formatGetIpOutput([
        { ip: "2001:db8::1", port: 443, asn: 906, region: "US", asnAlias: null },
      ]),
    ).toBe("2001:db8::1:443#906 US 01");
  });

  it("sorts by ASN, region, and numeric IP while numbering each group", () => {
    expect(
      formatGetIpOutput([
        { ip: "10.0.0.1", port: 443, asn: 906, region: "US", asnAlias: "DMIT" },
        { ip: "2.0.0.1", port: 443, asn: 906, region: "US", asnAlias: "DMIT" },
        { ip: "1.2.3.5", port: 8443, asn: 906, region: "US", asnAlias: "DMIT" },
        { ip: "1.2.3.4", port: 443, asn: 906, region: "US", asnAlias: "DMIT" },
        { ip: "1.1.1.1", port: 443, asn: 64512, region: "DE", asnAlias: null },
        { ip: "1.2.3.6", port: 443, asn: 906, region: "CA", asnAlias: "DMIT" },
      ]),
    ).toBe(
      [
        "1.2.3.6:443#DMIT CA 01",
        "1.2.3.4:443#DMIT US 01",
        "1.2.3.5:8443#DMIT US 02",
        "2.0.0.1:443#DMIT US 03",
        "10.0.0.1:443#DMIT US 04",
        "1.1.1.1:443#64512 DE 01",
      ].join("\n"),
    );
  });

  it("returns an empty string for no rows", () => {
    expect(formatGetIpOutput([])).toBe("");
  });
});
