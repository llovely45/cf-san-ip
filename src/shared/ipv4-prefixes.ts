import ipaddr from "ipaddr.js";

export interface PlannedPrefixBlock {
  asn: number;
  sourcePrefix: string;
  blockCidr: string;
}

interface ParsedPrefix {
  canonical: string;
  network: number;
  prefixLength: number;
  last: number;
}

const toUint32 = (bytes: number[]): number =>
  (((bytes[0] ?? 0) << 24) |
    ((bytes[1] ?? 0) << 16) |
    ((bytes[2] ?? 0) << 8) |
    (bytes[3] ?? 0)) >>> 0;

function parsePrefix(value: string): ParsedPrefix | null {
  if (!/^\d{1,3}(?:\.\d{1,3}){3}\/\d{1,2}$/u.test(value)) {
    return null;
  }

  try {
    const [address, prefixLength] = ipaddr.parseCIDR(value);
    if (address.kind() !== "ipv4") {
      return null;
    }

    const length = prefixLength;
    const mask = length === 0 ? 0 : (0xffffffff << (32 - length)) >>> 0;
    const network = (toUint32(address.toByteArray()) & mask) >>> 0;
    const last = (network | (~mask >>> 0)) >>> 0;
    const canonicalAddress = [
      (network >>> 24) & 255,
      (network >>> 16) & 255,
      (network >>> 8) & 255,
      network & 255,
    ].join(".");

    return {
      canonical: `${canonicalAddress}/${length}`,
      network,
      prefixLength: length,
      last,
    };
  } catch {
    return null;
  }
}

const NON_PUBLIC_PREFIXES = [
  "0.0.0.0/8",
  "10.0.0.0/8",
  "100.64.0.0/10",
  "127.0.0.0/8",
  "169.254.0.0/16",
  "172.16.0.0/12",
  "192.0.0.0/24",
  "192.0.2.0/24",
  "192.88.99.0/24",
  "192.168.0.0/16",
  "198.18.0.0/15",
  "198.51.100.0/24",
  "203.0.113.0/24",
  "224.0.0.0/4",
  "240.0.0.0/4",
].map((prefix) => parsePrefix(prefix) as ParsedPrefix);

function isPublicPrefix(prefix: ParsedPrefix): boolean {
  const address = ipaddr.parse(prefix.canonical.split("/")[0] ?? "");
  if (address.kind() !== "ipv4" || address.range() !== "unicast") {
    return false;
  }

  return !NON_PUBLIC_PREFIXES.some(
    (blocked) => prefix.network <= blocked.last && blocked.network <= prefix.last,
  );
}

function formatIPv4(value: number): string {
  return [
    (value >>> 24) & 255,
    (value >>> 16) & 255,
    (value >>> 8) & 255,
    value & 255,
  ].join(".");
}

export function normalizeIPv4Prefixes(prefixes: string[]): string[] {
  const unique = new Map<string, ParsedPrefix>();
  for (const value of prefixes) {
    const parsed = parsePrefix(value);
    if (parsed !== null && isPublicPrefix(parsed)) {
      unique.set(parsed.canonical, parsed);
    }
  }
  return [...unique.values()]
    .sort((left, right) =>
      left.network - right.network ||
      left.prefixLength - right.prefixLength ||
      left.canonical.localeCompare(right.canonical),
    )
    .map((prefix) => prefix.canonical);
}

export function planIPv4Blocks(
  asn: number,
  prefixes: string[],
): PlannedPrefixBlock[] {
  if (!Number.isSafeInteger(asn) || asn <= 0) {
    throw new Error("ASN must be a positive safe integer");
  }

  const blocks = new Map<string, PlannedPrefixBlock & { sourcePrefixLength: number }>();

  for (const rawPrefix of prefixes) {
    const prefix = parsePrefix(rawPrefix);
    if (prefix === null || !isPublicPrefix(prefix)) {
      continue;
    }

    const blockPrefixLength = Math.max(prefix.prefixLength, 24);
    const blockSize = 2 ** (32 - blockPrefixLength);
    const blockCount = 2 ** (blockPrefixLength - prefix.prefixLength);

    for (let index = 0; index < blockCount; index += 1) {
      const network = prefix.network + index * blockSize;
      const blockCidr = `${formatIPv4(network)}/${blockPrefixLength}`;
      const existing = blocks.get(blockCidr);
      if (existing === undefined || prefix.prefixLength > existing.sourcePrefixLength) {
        blocks.set(blockCidr, {
          asn,
          sourcePrefix: prefix.canonical,
          blockCidr,
          sourcePrefixLength: prefix.prefixLength,
        });
      }
    }
  }

  return [...blocks.values()]
    .sort((left, right) => {
      const leftPrefix = parsePrefix(left.blockCidr);
      const rightPrefix = parsePrefix(right.blockCidr);
      return (
        (leftPrefix?.network ?? 0) - (rightPrefix?.network ?? 0) ||
        (leftPrefix?.prefixLength ?? 0) - (rightPrefix?.prefixLength ?? 0)
      );
    })
    .map(({ sourcePrefixLength: _sourcePrefixLength, ...block }) => block);
}

export function isIPv4ScanBlock(sourcePrefix: string, blockCIDR: string): boolean {
  const source = parsePrefix(sourcePrefix);
  const block = parsePrefix(blockCIDR);
  return source !== null && block !== null &&
    source.canonical === sourcePrefix && block.canonical === blockCIDR &&
    isPublicPrefix(source) && isPublicPrefix(block) &&
    block.prefixLength === Math.max(source.prefixLength, 24) &&
    block.network >= source.network && block.last <= source.last;
}

export function isIPv4CIDRWithinCIDR(childCIDR: string, parentCIDR: string): boolean {
  const child = parsePrefix(childCIDR);
  const parent = parsePrefix(parentCIDR);
  return child !== null && parent !== null &&
    child.prefixLength >= parent.prefixLength &&
    child.network >= parent.network &&
    child.last <= parent.last;
}

export function isIPv4WithinCIDR(ip: string, cidr: string): boolean {
  if (!ipaddr.isValid(ip)) {
    return false;
  }
  try {
    const address = ipaddr.parse(ip);
    const prefix = parsePrefix(cidr);
    if (address.kind() !== "ipv4" || prefix === null) {
      return false;
    }
    const value = toUint32(address.toByteArray());
    return value >= prefix.network && value <= prefix.last;
  } catch {
    return false;
  }
}
