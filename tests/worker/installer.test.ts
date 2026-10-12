import { describe, expect, it } from "vitest";

import { createInstallerRoutes } from "../../src/worker/routes/installer";

describe("worker installer route", () => {
  it("fails clearly until a release repository is configured", async () => {
    const routes = createInstallerRoutes();
    const response = await routes.request("https://app.test/install");
    expect(response.status).toBe(503);
    expect(await response.json()).toEqual({ error: "release_repository_not_configured" });
  });

  it.each(["", "owner", "owner/repo/extra", "owner/repo?token=secret"])(
    "rejects an invalid release repository %j",
    async (repository) => {
      const routes = createInstallerRoutes(repository);
      const response = await routes.request("https://app.test/install");
      expect(response.status).toBe(503);
    },
  );

  it("serves the configured scheduler and exact pinned-version installer", async () => {
    const routes = createInstallerRoutes();
    const response = await routes.request(
      "https://app.test/install",
      {},
      { CF_SAN_RELEASE_REPOSITORY: "llovely45/cf-san-ip" },
    );
    const script = await response.text();
    expect(response.status).toBe(200);
    expect(response.headers.get("Content-Type")).toContain("text/x-shellscript");
    expect(response.headers.get("Cache-Control")).toContain("no-store");
    expect(script).toContain("DEFAULT_RELEASE_REPOSITORY='llovely45/cf-san-ip'");
    expect(script).toContain("17 3 * * * root /usr/local/libexec/cf-san-auto-update");
    expect(script).toContain("/etc/periodic/daily/cf-san-auto-update");
    expect(script).toContain('BASE_URL="https://github.com/${RELEASE_REPOSITORY}/releases/download/${UPDATE_VERSION}"');
    expect(script).toContain("--version vMAJOR.MINOR.PATCH");
    expect(script).toContain("--url");
    expect(script).toContain("--token");
    expect(script).not.toContain("api-token");
  });
});
