import maxmind from "maxmind";

const server = Bun.serve({
  port: process.env.PORT || "3000",

  async fetch(req, server) {
    const url = new URL(req.url);
    const format = url.searchParams.get("format") || url.searchParams.get("fmt") || url.pathname.split(".")[1] || "text";
    const callback = url.searchParams.get("callback") || url.searchParams.get("cb") || "callback";
    const showDetails = !/^\/(?:$|ip)/i.test(url.pathname);

    const clientIp = req.headers.get("cf-connecting-ipv6") || req.headers.get("cf-connecting-ip") || req.headers.get("x-forwarded-for") || req.headers.get("x-real-ip") || server.requestIP(req)?.address || "localhost";
    const ip = url.searchParams.get("ip") || clientIp;
    const details = showDetails ? lookupDetails({ip, isSearching: ip !== clientIp, req}) : undefined;
    const payload = { ip, ...details };
    const timestamp = new Date().toISOString();

    // DEBUG
    if (process.env.DEBUG !== "false") {
      console.log(`[${timestamp}] ✅ ${req.method} request from ${clientIp}`);
    }

    const commonHeaders = {
      "Connection": "keep-alive",
      "Keep-Alive": "timeout=5, max=1000",
      "Access-Control-Allow-Origin": "*",
      "X-Powered-By": `${process.env.POWERED_BY || req.headers.get("host")}`,
      "X-Client-IP": clientIp,
    };

    // JSON
    if (format === "json") {
      return new Response(`${JSON.stringify(payload)}\n`, { headers: { ...commonHeaders, "Content-Type": "application/json" } });
    }

    // JSONP
    if (format === "jsonp") {
      return new Response(`${callback}(${JSON.stringify(payload)});\n`, { headers: { ...commonHeaders, "Content-Type": "application/javascript" } });
    }

    // XML
    if (format === "xml") {
      return new Response(serializeXML(payload), { headers: { ...commonHeaders, "Content-Type": "application/xml" } });
    }

    // TEXT
    return new Response(serializeText(payload), { headers: { ...commonHeaders, "Content-Type": "text/plain" } });

  },
});

console.log(`🚀 Server running at http://${server.hostname}:${server.port}\n`);

// --- Utils ---
// -------------

const FLAG_UNICODE_POSITION = 127397;
//  Get Country Flag
export function getFlag(countryCode: string) {
  const regex = new RegExp("^[A-Z]{2}$").test(countryCode);
  if (!countryCode || !regex) return undefined;
  try {
    return String.fromCodePoint(
      ...countryCode.split("").map((char) =>
        FLAG_UNICODE_POSITION + char.charCodeAt(0)
      ),
    );
  } catch (error) {
    return undefined;
  }
}

// Serialize Text
function serializeText(payload: Record<string, unknown>): string {
  const entries = Object.entries(payload).filter(
    ([, value]) => value != null,
  );

  if (entries.length === 1) {
    const [, value] = entries[0]!;
    return `${String(value)}\n`;
  }

  const sorted = entries.sort(([a], [b]) =>
    a === "ip" ? -1 : b === "ip" ? 1 : 0,
  );

  let out = "";
  for (const [key, value] of sorted) {
    out += `${key}: ${value}\n`;
  }

  return out;
}

// Serialize XML
function serializeXML(payload: Record<string, unknown>): string {
  let xml = `<?xml version="1.0" encoding="UTF-8"?>\n` +
    `<response>`;

  for (const [key, value] of Object.entries(payload)) {
    if (value == null) continue;

    xml += `\n  <${key}>${Bun.escapeHTML(String(value))}</${key}>`;
  }

  xml += `\n</response>\n`;
  return xml;
}

// Lookup Details
const filepath = process.env.GEOIPUPDATE_DB_PATH || "/app/.data/db"
const asnReader = await maxmind.open(filepath + "/GeoLite2-ASN.mmdb", { cache: { max: 10_000 }, watchForUpdates: true });
const cityReader = await maxmind.open(filepath + "/GeoLite2-City.mmdb", { cache: { max: 10_000 }, watchForUpdates: true });
function lookupDetails({ ip, isSearching = false, req}: { ip: string, isSearching?: boolean, req?: Request }) {
  const r: any = { ...asnReader.get(ip), ...cityReader.get(ip) };
  const cf = !isSearching ? req?.headers : undefined;
  
  if (!r) return undefined;

  return {
    flag: getFlag(r?.country?.iso_code || cf?.get("cf-ipcountry")),
    continentCode: r?.continent?.code || cf?.get("cf-ipcontinent"),
    continent: r?.continent?.names?.en,
    countryCode: r?.country?.iso_code || cf?.get("cf-ipcountry"),
    country: r?.country?.names?.en,
    regionCode: r?.subdivisions?.[0]?.iso_code || cf?.get("cf-region-code"),
    region: r?.subdivisions?.[0]?.names?.en || cf?.get("cf-region"),
    city: r?.city?.names?.en || cf?.get("cf-ipcity"),
    postalCode: r?.postal?.code || cf?.get("cf-postal-code"),
    latitude: r?.location?.latitude || cf?.get("cf-iplatitude"),
    longitude: r?.location?.longitude || cf?.get("cf-iplongitude"),
    timezone: r?.location?.time_zone || cf?.get("cf-timezone"),
    asn: r?.autonomous_system_number || cf?.get("x-asn"),
    asRegion: cf?.get("cf-ray")?.split("-")[1],
    asOrganization: r?.autonomous_system_organization || cf?.get("cf-asorganization"),
    userAgent: cf?.get("user-agent")
  };
}
