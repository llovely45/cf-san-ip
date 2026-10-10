import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import App from "../../src/client/App";
import { createClientApi } from "../../src/client/api";
import type { ClientApi } from "../../src/client/api";
import type { IpRecord } from "../../src/shared/ip";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const firstRecord: IpRecord = {
  ip: "192.0.2.10",
  port: 443,
  asn: 64500,
  region: "US",
  asnAlias: null,
};

function createFakeApi(overrides: Partial<ClientApi> = {}): ClientApi {
  return {
    getSetupStatus: vi.fn().mockResolvedValue({ initialized: false }),
    setup: vi.fn().mockResolvedValue(undefined),
    login: vi.fn().mockResolvedValue(undefined),
    logout: vi.fn().mockResolvedValue(undefined),
    listRecords: vi.fn().mockResolvedValue({ rows: [], total: 0 }),
    saveRecord: vi.fn().mockResolvedValue(undefined),
    deleteRecord: vi.fn().mockResolvedValue(undefined),
    setAsnAlias: vi.fn().mockResolvedValue(undefined),
    getToken: vi.fn().mockResolvedValue({ token: null }),
    setToken: vi.fn().mockResolvedValue(undefined),
    generateToken: vi.fn().mockResolvedValue({ token: "generated-token" }),
    getAgentInstallCommand: vi.fn().mockResolvedValue({
      command: "curl -fsSL 'https://app.test/install' | sudo bash -s -- --url 'https://app.test' --token 'existing-token'",
      warning: "此命令中的 Token 会留在 shell 历史，且可能从进程参数中看到。",
    }),
    ...overrides,
  };
}

function createAuthenticatedApi(overrides: Partial<ClientApi> = {}): ClientApi {
  return createFakeApi({
    getSetupStatus: vi.fn().mockResolvedValue({ initialized: true }),
    listRecords: vi.fn().mockResolvedValue({ rows: [firstRecord], total: 1 }),
    getToken: vi.fn().mockResolvedValue({ token: "existing-token" }),
    ...overrides,
  });
}

function createDeferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

describe("App setup and authentication", () => {
  it("moves from first-run setup to login without creating a session", async () => {
    const user = userEvent.setup();
    const api = createFakeApi();

    render(<App api={api} />);

    expect(
      await screen.findByRole("heading", { name: "初始化管理员密码" }),
    ).toBeTruthy();
    await user.type(screen.getByLabelText("管理员密码"), "new-admin-password");
    await user.type(
      screen.getByLabelText("确认管理员密码"),
      "new-admin-password",
    );
    await user.click(screen.getByRole("button", { name: "初始化管理员密码" }));

    await waitFor(() => expect(api.setup).toHaveBeenCalledWith("new-admin-password"));
    expect(api.login).not.toHaveBeenCalled();
    expect(
      await screen.findByRole("heading", { name: "管理员登录" }),
    ).toBeTruthy();
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
  });

  it("uses an authenticated records read to restore an existing cookie session", async () => {
    const api = createAuthenticatedApi();

    render(<App api={api} />);

    expect(await screen.findByText(firstRecord.ip)).toBeTruthy();
    expect(api.listRecords).toHaveBeenCalledWith(1, 20);
    expect(api.getToken).toHaveBeenCalledOnce();
    expect(screen.queryByRole("heading", { name: "管理员登录" })).toBeNull();
  });

  it("returns initialized but unauthenticated visitors to login on 401", async () => {
    const unauthorized = Object.assign(new Error("Unauthorized"), { status: 401 });
    const api = createFakeApi({
      getSetupStatus: vi.fn().mockResolvedValue({ initialized: true }),
      listRecords: vi.fn().mockRejectedValue(unauthorized),
    });

    render(<App api={api} />);

    expect(
      await screen.findByRole("heading", { name: "管理员登录" }),
    ).toBeTruthy();
  });

  it("logs in and loads the management page without storing the password", async () => {
    const user = userEvent.setup();
    const unauthorized = Object.assign(new Error("Unauthorized"), { status: 401 });
    const api = createFakeApi({
      getSetupStatus: vi.fn().mockResolvedValue({ initialized: true }),
      listRecords: vi
        .fn()
        .mockRejectedValueOnce(unauthorized)
        .mockResolvedValue({ rows: [firstRecord], total: 1 }),
    });

    render(<App api={api} />);

    await user.type(await screen.findByLabelText("管理员密码"), "login-password");
    await user.click(screen.getByRole("button", { name: "登录" }));

    await waitFor(() => expect(api.login).toHaveBeenCalledWith("login-password"));
    expect(await screen.findByText(firstRecord.ip)).toBeTruthy();
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
  });
});

describe("paginated records", () => {
  it("renders one server page and offers only the three allowed page sizes", async () => {
    const api = createAuthenticatedApi({
      listRecords: vi.fn().mockResolvedValue({
        rows: [firstRecord],
        total: 81,
      }),
    });

    render(<App api={api} />);

    const table = await screen.findByRole("table", { name: "IP 记录" });
    expect(within(table).getByText(firstRecord.ip)).toBeTruthy();
    expect(within(table).queryAllByRole("row")).toHaveLength(2);
    expect(
      Array.from(
        (screen.getByLabelText("每页条数") as HTMLSelectElement).options,
      ).map((option) => option.value),
    ).toEqual(["20", "50", "100"]);
    expect(api.listRecords).toHaveBeenCalledWith(1, 20);
  });

  it("requests the next page instead of preloading records", async () => {
    const user = userEvent.setup();
    const secondRecord = { ...firstRecord, ip: "192.0.2.20" };
    const listRecords = vi.fn(async (page: number) => ({
      rows: page === 1 ? [firstRecord] : [secondRecord],
      total: 40,
    }));
    const api = createAuthenticatedApi({ listRecords });

    render(<App api={api} />);
    await screen.findByText(firstRecord.ip);
    expect(listRecords).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole("button", { name: "下一页" }));

    expect(await screen.findByText(secondRecord.ip)).toBeTruthy();
    expect(screen.queryByText(firstRecord.ip)).toBeNull();
    expect(listRecords).toHaveBeenLastCalledWith(2, 20);
    expect(listRecords).toHaveBeenCalledTimes(2);
  });

  it("fetches the current page again when the page size changes", async () => {
    const user = userEvent.setup();
    const listRecords = vi.fn().mockResolvedValue({ rows: [firstRecord], total: 1 });
    const api = createAuthenticatedApi({ listRecords });

    render(<App api={api} />);
    await screen.findByText(firstRecord.ip);
    await user.selectOptions(screen.getByLabelText("每页条数"), "50");

    await waitFor(() => expect(listRecords).toHaveBeenLastCalledWith(1, 50));
  });

  it("ignores an older page-size response that resolves after the latest one", async () => {
    const user = userEvent.setup();
    const olderResult = createDeferred<{ rows: IpRecord[]; total: number }>();
    const latestResult = createDeferred<{ rows: IpRecord[]; total: number }>();
    const olderRecord = { ...firstRecord, ip: "192.0.2.50" };
    const latestRecord = { ...firstRecord, ip: "192.0.2.100" };
    const listRecords = vi
      .fn()
      .mockResolvedValueOnce({ rows: [firstRecord], total: 120 })
      .mockReturnValueOnce(olderResult.promise)
      .mockReturnValueOnce(latestResult.promise);
    const api = createAuthenticatedApi({ listRecords });

    render(<App api={api} />);
    await screen.findByText(firstRecord.ip);

    const pageSize = screen.getByLabelText("每页条数") as HTMLSelectElement;
    await user.selectOptions(pageSize, "50");
    await user.selectOptions(pageSize, "100");
    expect(listRecords).toHaveBeenNthCalledWith(2, 1, 50);
    expect(listRecords).toHaveBeenNthCalledWith(3, 1, 100);

    await act(async () => {
      latestResult.resolve({ rows: [latestRecord], total: 120 });
    });
    expect(await screen.findByText(latestRecord.ip)).toBeTruthy();
    await waitFor(() => expect(pageSize.value).toBe("100"));

    await act(async () => {
      olderResult.resolve({ rows: [olderRecord], total: 120 });
    });

    expect(screen.getByText(latestRecord.ip)).toBeTruthy();
    expect(screen.queryByText(olderRecord.ip)).toBeNull();
    expect(pageSize.value).toBe("100");
  });

  it("saves edits as an update using the original IP key", async () => {
    const user = userEvent.setup();
    const updatedRecord = { ...firstRecord, ip: "192.0.2.11", port: 8443 };
    const api = createAuthenticatedApi({
      saveRecord: vi.fn().mockResolvedValue(undefined),
    });

    render(<App api={api} />);
    await screen.findByText(firstRecord.ip);
    await user.click(
      screen.getByRole("button", { name: `编辑 ${firstRecord.ip}` }),
    );
    await user.clear(screen.getByLabelText("IP 地址"));
    await user.type(screen.getByLabelText("IP 地址"), updatedRecord.ip);
    await user.clear(screen.getByLabelText("端口"));
    await user.type(screen.getByLabelText("端口"), String(updatedRecord.port));
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(api.saveRecord).toHaveBeenCalledWith(updatedRecord, firstRecord.ip),
    );
  });

  it("requires deletion confirmation before removing a record", async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, "confirm");
    confirm.mockReturnValueOnce(false).mockReturnValueOnce(true);
    const api = createAuthenticatedApi();

    render(<App api={api} />);
    await screen.findByText(firstRecord.ip);

    await user.click(
      screen.getByRole("button", { name: `删除 ${firstRecord.ip}` }),
    );
    expect(api.deleteRecord).not.toHaveBeenCalled();

    await user.click(
      screen.getByRole("button", { name: `删除 ${firstRecord.ip}` }),
    );
    await waitFor(() => expect(api.deleteRecord).toHaveBeenCalledWith(firstRecord.ip));
  });
});

describe("ASN alias and token settings", () => {
  it("saves an ASN alias and refreshes the current page", async () => {
    const user = userEvent.setup();
    const api = createAuthenticatedApi();

    render(<App api={api} />);
    await screen.findByText(firstRecord.ip);
    const aliasPanel = screen.getByRole("region", { name: "ASN 别名" });
    await user.type(within(aliasPanel).getByLabelText("ASN"), "64500");
    await user.type(within(aliasPanel).getByLabelText("别名"), "ExampleNet");
    await user.click(within(aliasPanel).getByRole("button", { name: "保存别名" }));

    await waitFor(() => expect(api.setAsnAlias).toHaveBeenCalledWith(64500, "ExampleNet"));
    expect(api.listRecords).toHaveBeenCalledTimes(2);
  });

  it("sets, clears the form for, and displays the current plaintext token", async () => {
    const user = userEvent.setup();
    let savedToken = "old-token";
    const api = createAuthenticatedApi({
      setToken: vi.fn(async (newToken: string) => {
        savedToken = newToken;
      }),
      getToken: vi.fn(async () => ({ token: savedToken })),
    });

    render(<App api={api} />);
    const tokenPanel = await screen.findByRole("region", { name: "Token 设置" });
    await waitFor(() => expect(within(tokenPanel).getByLabelText("当前 API Token").textContent).toBe("old-token"));
    await user.type(within(tokenPanel).getByLabelText("设置 API Token"), "replacement-token");
    await user.click(within(tokenPanel).getByRole("button", { name: "保存 Token" }));

    await waitFor(() => expect(api.setToken).toHaveBeenCalledWith("replacement-token"));
    expect((within(tokenPanel).getByLabelText("设置 API Token") as HTMLInputElement).value).toBe("");
    await waitFor(() =>
      expect(within(tokenPanel).getByLabelText("当前 API Token").textContent).toBe(
        "replacement-token",
      ),
    );
  });

  it("generates and visibly copies a plaintext API token", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    const clipboardDescriptor = Object.getOwnPropertyDescriptor(navigator, "clipboard");
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    const api = createAuthenticatedApi({
      generateToken: vi.fn().mockResolvedValue({ token: "generated-token" }),
    getAgentInstallCommand: vi.fn().mockResolvedValue({
      command: "curl -fsSL 'https://app.test/install' | sudo bash -s -- --url 'https://app.test' --token 'existing-token'",
      warning: "此命令中的 Token 会留在 shell 历史，且可能从进程参数中看到。",
    }),
    });

    try {
      render(<App api={api} />);
      const tokenPanel = await screen.findByRole("region", { name: "Token 设置" });
      await user.type(within(tokenPanel).getByLabelText("设置 API Token"), "manual-draft");
      await user.click(within(tokenPanel).getByRole("button", { name: "生成 Token" }));

      await waitFor(() =>
        expect(within(tokenPanel).getByLabelText("当前 API Token").textContent).toBe(
          "generated-token",
        ),
      );
      expect(
        (within(tokenPanel).getByLabelText("设置 API Token") as HTMLInputElement)
          .value,
      ).toBe("");
      expect(api.generateToken).toHaveBeenCalledOnce();
      await user.click(within(tokenPanel).getByRole("button", { name: "复制 Token" }));
      expect(writeText).toHaveBeenCalledWith("generated-token");
    } finally {
      if (clipboardDescriptor === undefined) {
        Reflect.deleteProperty(navigator, "clipboard");
      } else {
        Object.defineProperty(navigator, "clipboard", clipboardDescriptor);
      }
    }
  });
});

describe("same-origin API client", () => {
  it("normalizes asn_alias rows and sends same-origin cookie credentials", async () => {
    const wireRecord = {
      ip: firstRecord.ip,
      port: firstRecord.port,
      asn: firstRecord.asn,
      region: firstRecord.region,
      asn_alias: "ExampleNet",
    };
    const fetcher = vi.fn(async () =>
      new Response(JSON.stringify({ rows: [wireRecord], total: 1 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    const api = createClientApi(fetcher as typeof fetch);

    const result = await api.listRecords(1, 20);

    expect(result.rows).toEqual([{ ...firstRecord, asnAlias: "ExampleNet" }]);
    expect(fetcher).toHaveBeenCalledWith(
      "/api/admin/records?page=1&pageSize=20",
      expect.objectContaining({ credentials: "same-origin" }),
    );
  });

  it("renames using the encoded original IP route and the new IP body", async () => {
    const fetcher = vi.fn(async () =>
      new Response(JSON.stringify({ record: { ...firstRecord, ip: "2001:db8::2" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    const api = createClientApi(fetcher as typeof fetch);
    const renamedRecord: IpRecord = {
      ...firstRecord,
      ip: "2001:db8::2",
      asnAlias: "ExampleNet",
    };

    await api.saveRecord(renamedRecord, "2001:db8::1");

    expect(fetcher).toHaveBeenCalledWith(
      "/api/admin/records/2001%3Adb8%3A%3A1",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({
          ip: "2001:db8::2",
          port: 443,
          asn: 64500,
          region: "US",
          asn_alias: "ExampleNet",
        }),
      }),
    );
  });
});

describe("agent installer panel", () => {
  it("shows a copyable one-command install line and the token visibility warning", async () => {
    const api = createAuthenticatedApi();
    render(<App api={api} />);

    expect(await screen.findByRole("heading", { name: "安装 cf-san Agent" })).toBeTruthy();
    expect(await screen.findByText(/curl -fsSL/)).toBeTruthy();
    expect(await screen.findByText(/shell 历史/)).toBeTruthy();
    expect(await screen.findByRole("button", { name: "复制安装命令" })).toBeTruthy();
  });

  it("copies the install command when the clipboard is available", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    const clipboardDescriptor = Object.getOwnPropertyDescriptor(navigator, "clipboard");
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    const api = createAuthenticatedApi();

    try {
      render(<App api={api} />);
      await user.click(await screen.findByRole("button", { name: "复制安装命令" }));
      expect(writeText).toHaveBeenCalledWith(expect.stringContaining("--token 'existing-token'"));
      expect(await screen.findByText("安装命令已复制。")).toBeTruthy();
    } finally {
      if (clipboardDescriptor === undefined) {
        delete (navigator as Navigator & { clipboard?: Clipboard }).clipboard;
      } else {
        Object.defineProperty(navigator, "clipboard", clipboardDescriptor);
      }
    }
  });

  it("explains when the Worker release repository has not been configured", async () => {
    const api = createAuthenticatedApi({
      getAgentInstallCommand: vi.fn().mockRejectedValue(
        new Error("release_repository_not_configured"),
      ),
    });
    render(<App api={api} />);
    expect(await screen.findByText(/CF_SAN_RELEASE_REPOSITORY=owner\/repository/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "复制安装命令" })).toBeNull();
  });
});
