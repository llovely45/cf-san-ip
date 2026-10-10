import {
  validateIpInput,
  type IpRecord,
  type Validation,
} from "../../shared/ip";
import { formatGetIpOutput } from "../../shared/output";
import { D1IpRepository } from "../repositories/ip-records";

export interface BatchResult {
  accepted: number;
  rejected: number;
  errors: Array<{ index: number; ip?: string; reason: string }>;
}

export class InvalidBatchInputError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "InvalidBatchInputError";
  }
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

function mapWireAlias(input: unknown): unknown {
  if (!isRecord(input)) {
    return input;
  }

  const mapped = { ...input };
  delete mapped.asnAlias;

  if (Object.hasOwn(input, "asn_alias")) {
    mapped.asnAlias = input.asn_alias;
  }

  delete mapped.asn_alias;
  return mapped;
}

function validateRow(input: unknown): Validation<
  Omit<IpRecord, "asnAlias"> & { asnAlias?: string }
> {
  return validateIpInput(mapWireAlias(input));
}

export async function ingestBatch(
  db: D1Database,
  input: unknown,
): Promise<BatchResult> {
  if (!isRecord(input) || !Array.isArray(input.ips)) {
    throw new InvalidBatchInputError("ips must be an array");
  }

  const repository = new D1IpRepository(db);
  const errors: BatchResult["errors"] = [];
  let accepted = 0;

  for (const [index, row] of input.ips.entries()) {
    const validation = validateRow(row);
    if (!validation.ok) {
      const error: BatchResult["errors"][number] = {
        index,
        reason: validation.reason,
      };
      if (isRecord(row) && typeof row.ip === "string") {
        error.ip = row.ip;
      }
      errors.push(error);
      continue;
    }

    const record: IpRecord = {
      ...validation.value,
      asnAlias:
        validation.value.asnAlias ??
        (await repository.findAliasByAsn(validation.value.asn)),
    };
    await repository.upsert(record);
    accepted += 1;
  }

  return { accepted, rejected: errors.length, errors };
}

export async function getIpOutput(
  db: D1Database,
  limit: number,
  asn: number | null,
): Promise<string> {
  const rows = await new D1IpRepository(db).sample(limit, asn);
  return formatGetIpOutput(rows);
}
