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

  it("serves a no-store installer configured for the matching GitHub repository", async () => {
    const routes = createInstallerRoutes("example/cf-san");
    const response = await routes.request("https://app.test/install");
    const script = await response.text();
    expect(response.status).toBe(200);
    expect(response.headers.get("Content-Type")).toContain("text/x-shellscript");
    expect(response.headers.get("Cache-Control")).toContain("no-store");
    expect(script).toContain("example/cf-san");
    expect(script).toContain("--url");
    expect(script).toContain("--token");
    expect(script).not.toContain("api-token");
  });
});
