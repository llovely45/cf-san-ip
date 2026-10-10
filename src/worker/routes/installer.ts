import { Hono } from "hono";

import installScript from "../../../agent/scripts/install.sh?raw";
import type { Env } from "../env";

const RELEASE_REPOSITORY_PATTERN = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/u;

export function isValidReleaseRepository(value: string | undefined): value is string {
  return value !== undefined && RELEASE_REPOSITORY_PATTERN.test(value);
}

export function createInstallerRoutes(fallbackReleaseRepository?: string) {
  const routes = new Hono<{ Bindings: Env }>();

  routes.get("/install", (context) => {
    const releaseRepository =
      context.env?.CF_SAN_RELEASE_REPOSITORY ?? fallbackReleaseRepository;
    if (!isValidReleaseRepository(releaseRepository)) {
      return context.json({ error: "release_repository_not_configured" }, 503);
    }

    const script = installScript.replace(
      "__CF_SAN_RELEASE_REPOSITORY__",
      releaseRepository,
    );
    return context.body(script, 200, {
      "Content-Type": "text/x-shellscript; charset=utf-8",
      "Cache-Control": "no-store, max-age=0",
      "X-Content-Type-Options": "nosniff",
      "Content-Disposition": 'attachment; filename="cf-san-install.sh"',
    });
  });

  return routes;
}
