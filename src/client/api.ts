import type { IpRecord } from "../shared/ip";

export type PageSize = 20 | 50 | 100;

export interface ClientApi {
  getSetupStatus(): Promise<{ initialized: boolean }>;
  setup(password: string): Promise<void>;
  login(password: string): Promise<void>;
  logout(): Promise<void>;
  listRecords(
    page: number,
    pageSize: PageSize,
  ): Promise<{ rows: IpRecord[]; total: number }>;
  saveRecord(record: IpRecord, originalIp?: string): Promise<void>;
  deleteRecord(ip: string): Promise<void>;
  setAsnAlias(asn: number, alias: string | null): Promise<void>;
  getToken(): Promise<{ token: string | null }>;
  setToken(token: string): Promise<void>;
  generateToken(): Promise<{ token: string }>;
  getAgentInstallCommand(): Promise<{ command: string; warning: string }>;
}

type WireIpRecord = Omit<IpRecord, "asnAlias"> & { asn_alias: string | null };

export class ClientApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
    this.name = "ClientApiError";
  }
}

const toIpRecord = (record: WireIpRecord): IpRecord => ({
  ip: record.ip,
  port: record.port,
  asn: record.asn,
  region: record.region,
  asnAlias: record.asn_alias,
});

const toWireRecord = (record: IpRecord): WireIpRecord => ({
  ip: record.ip,
  port: record.port,
  asn: record.asn,
  region: record.region,
  asn_alias: record.asnAlias,
});

export function createClientApi(fetchImplementation: typeof fetch = fetch): ClientApi {
  const request = async <T>(
    path: string,
    init: RequestInit = {},
  ): Promise<T> => {
    const headers = new Headers(init.headers);
    if (init.body !== undefined && !headers.has("Content-Type")) {
      headers.set("Content-Type", "application/json");
    }

    const response = await fetchImplementation(path, {
      ...init,
      headers,
      credentials: "same-origin",
    });

    if (!response.ok) {
      let message = `Request failed (${response.status})`;
      try {
        const body = (await response.json()) as { error?: unknown };
        if (typeof body.error === "string") {
          message = body.error;
        }
      } catch {
        message = response.statusText || message;
      }
      throw new ClientApiError(message, response.status);
    }

    if (response.status === 204) {
      return undefined as T;
    }
    return (await response.json()) as T;
  };

  return {
    getSetupStatus: () => request("/api/setup/status"),
    setup: async (password) => {
      await request("/api/setup", {
        method: "POST",
        body: JSON.stringify({ password }),
      });
    },
    login: async (password) => {
      await request("/api/auth/login", {
        method: "POST",
        body: JSON.stringify({ password }),
      });
    },
    logout: async () => {
      await request("/api/auth/logout", { method: "POST" });
    },
    listRecords: async (page, pageSize) => {
      const result = await request<{
        rows: WireIpRecord[];
        total: number;
      }>(`/api/admin/records?page=${page}&pageSize=${pageSize}`);
      return { rows: result.rows.map(toIpRecord), total: result.total };
    },
    saveRecord: async (record, originalIp) => {
      const path = originalIp === undefined
        ? "/api/admin/records"
        : `/api/admin/records/${encodeURIComponent(originalIp)}`;
      await request(path, {
        method: originalIp === undefined ? "POST" : "PUT",
        body: JSON.stringify(toWireRecord(record)),
      });
    },
    deleteRecord: async (ip) => {
      await request(`/api/admin/records/${encodeURIComponent(ip)}`, {
        method: "DELETE",
      });
    },
    setAsnAlias: async (asn, alias) => {
      const path = `/api/admin/asns/${asn}/alias`;
      if (alias === null) {
        await request(path, { method: "DELETE" });
      } else {
        await request(path, {
          method: "PUT",
          body: JSON.stringify({ alias }),
        });
      }
    },
    getToken: () => request("/api/admin/token"),
    setToken: async (token) => {
      await request("/api/admin/token", {
        method: "PUT",
        body: JSON.stringify({ token }),
      });
    },
    generateToken: () =>
      request("/api/admin/token/generate", { method: "POST" }),
    getAgentInstallCommand: () =>
      request("/api/admin/agent/install-command"),
  };
}

export const clientApi = createClientApi();
