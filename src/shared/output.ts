import ipaddr from "ipaddr.js";

import type { IpRecord } from "./ip";

const compareNumbers = (left: number, right: number): number =>
  left < right ? -1 : left > right ? 1 : 0;

const compareStrings = (left: string, right: string): number =>
  left < right ? -1 : left > right ? 1 : 0;

const compareIps = (left: string, right: string): number => {
  try {
    const leftAddress = ipaddr.parse(left);
    const rightAddress = ipaddr.parse(right);
    const kindComparison = compareStrings(leftAddress.kind(), rightAddress.kind());

    if (kindComparison !== 0) {
      return kindComparison;
    }

    const leftBytes = leftAddress.toByteArray();
    const rightBytes = rightAddress.toByteArray();

    for (let index = 0; index < Math.min(leftBytes.length, rightBytes.length); index += 1) {
      const byteComparison = compareNumbers(leftBytes[index], rightBytes[index]);
      if (byteComparison !== 0) {
        return byteComparison;
      }
    }

    return compareNumbers(leftBytes.length, rightBytes.length);
  } catch {
    return compareStrings(left, right);
  }
};

export function formatGetIpOutput(rows: IpRecord[]): string {
  const sortedRows = rows
    .map((row, index) => ({ row, index }))
    .sort((left, right) => {
      const asnComparison = compareNumbers(left.row.asn, right.row.asn);
      if (asnComparison !== 0) {
        return asnComparison;
      }

      const regionComparison = compareStrings(left.row.region, right.row.region);
      if (regionComparison !== 0) {
        return regionComparison;
      }

      const ipComparison = compareIps(left.row.ip, right.row.ip);
      return ipComparison !== 0 ? ipComparison : left.index - right.index;
    })
    .map(({ row }) => row);

  let previousAsn: number | null = null;
  let previousRegion: string | null = null;
  let groupIndex = 0;

  return sortedRows
    .map((row) => {
      if (row.asn !== previousAsn || row.region !== previousRegion) {
        previousAsn = row.asn;
        previousRegion = row.region;
        groupIndex = 1;
      } else {
        groupIndex += 1;
      }

      const asnLabel = row.asnAlias || String(row.asn);
      return `${row.ip}:${row.port}#${asnLabel} ${row.region} ${String(groupIndex).padStart(2, "0")}`;
    })
    .join("\n");
}
