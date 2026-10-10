import ipaddr from "ipaddr.js";

export interface IpRecord {
  ip: string;
  port: number;
  asn: number;
  region: string;
  asnAlias: string | null;
}

export type Validation<T> =
  | { ok: true; value: T }
  | { ok: false; reason: string };

type IpInput = Omit<IpRecord, "asnAlias"> & { asnAlias?: string };

const invalid = (reason: string): Validation<never> => ({ ok: false, reason });

const isRecord = (input: unknown): input is Record<string, unknown> =>
  typeof input === "object" && input !== null && !Array.isArray(input);

const isPositiveInteger = (value: unknown): value is number =>
  typeof value === "number" && Number.isSafeInteger(value) && value > 0;

const parsePositiveInteger = (value: string): number | null => {
  if (!/^\d+$/u.test(value)) {
    return null;
  }

  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : null;
};

export function validateIpInput(
  input: unknown,
): Validation<Omit<IpRecord, "asnAlias"> & { asnAlias?: string }> {
  if (!isRecord(input)) {
    return invalid("input must be an object");
  }

  if (typeof input.ip !== "string") {
    return invalid("ip must be a string");
  }

  const ip = input.ip.trim();
  if (!ipaddr.isValid(ip)) {
    return invalid("ip is invalid");
  }

  if (
    typeof input.port !== "number" ||
    !Number.isInteger(input.port) ||
    input.port < 1 ||
    input.port > 65535
  ) {
    return invalid("port must be an integer between 1 and 65535");
  }

  if (!isPositiveInteger(input.asn)) {
    return invalid("asn must be a positive integer");
  }

  if (typeof input.region !== "string") {
    return invalid("region must be a string");
  }

  const region = input.region.trim();
  if (region.length === 0) {
    return invalid("region is required");
  }
  if (/\s/u.test(region)) {
    return invalid("region must not contain whitespace");
  }

  const value: IpInput = {
    ip: ipaddr.parse(ip).toString(),
    port: input.port,
    asn: input.asn,
    region,
  };

  if (input.asnAlias !== undefined) {
    if (typeof input.asnAlias !== "string") {
      return invalid("asnAlias must be a string");
    }

    const asnAlias = input.asnAlias.trim();
    if (asnAlias.length === 0 || /\s/u.test(asnAlias)) {
      return invalid("asnAlias must be a non-empty word");
    }

    value.asnAlias = asnAlias;
  }

  return { ok: true, value };
}

export function parseGetIpQuery(
  params: URLSearchParams,
): Validation<{ asn: number | null; limit: number }> {
  const asnParam = params.get("asn");
  let asn: number | null = null;

  if (asnParam !== null) {
    const parsedAsn = parsePositiveInteger(asnParam);
    if (parsedAsn === null) {
      return invalid("asn must be a positive integer");
    }
    asn = parsedAsn;
  }

  const limitParam = params.get("limit");
  let limit = 10;

  if (limitParam !== null) {
    if (!/^\d+$/u.test(limitParam)) {
      return invalid("limit must be a positive integer");
    }

    const significantDigits = limitParam.replace(/^0+/u, "");
    if (significantDigits.length === 0) {
      return invalid("limit must be a positive integer");
    }

    limit =
      significantDigits.length > 2
        ? 20
        : Math.min(Number(significantDigits), 20);
  }

  return { ok: true, value: { asn, limit } };
}
