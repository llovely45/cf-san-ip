const RIPESTAT_ENDPOINT = "https://stat.ripe.net/data/ris-prefixes/data.json";
const MAX_RESPONSE_BYTES = 5 * 1024 * 1024;

interface RipeStatResponse {
  data?: {
    prefixes?: {
      v4?: {
        originating?: unknown;
      };
    };
  };
}

export async function fetchOriginatingIPv4Prefixes(
  asn: number,
  fetchImplementation: typeof fetch = fetch,
): Promise<string[]> {
  const url = new URL(RIPESTAT_ENDPOINT);
  url.searchParams.set("resource", String(asn));
  url.searchParams.set("types", "o");
  url.searchParams.set("af", "v4");
  url.searchParams.set("noise", "filter");
  url.searchParams.set("list_prefixes", "true");

  const response = await fetchImplementation(url, {
    headers: { Accept: "application/json" },
    signal: AbortSignal.timeout(10_000),
  });
  if (!response.ok) {
    throw new Error("RIPEstat request failed");
  }

  const declaredLength = Number(response.headers.get("Content-Length"));
  if (Number.isFinite(declaredLength) && declaredLength > MAX_RESPONSE_BYTES) {
    throw new Error("RIPEstat response too large");
  }
  const text = await response.text();
  if (new TextEncoder().encode(text).byteLength > MAX_RESPONSE_BYTES) {
    throw new Error("RIPEstat response too large");
  }

  let body: RipeStatResponse;
  try {
    body = JSON.parse(text) as RipeStatResponse;
  } catch {
    throw new Error("RIPEstat response is invalid JSON");
  }

  const prefixes = body.data?.prefixes?.v4?.originating;
  if (!Array.isArray(prefixes) || !prefixes.every((prefix) => typeof prefix === "string")) {
    throw new Error("RIPEstat response is missing IPv4 originating prefixes");
  }
  return prefixes;
}
