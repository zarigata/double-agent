// test-i18n.mjs validates the catalogs of the control-plane interface.
//
// The checks are the ones of the landing page plus the runtime keys: the
// labels that JavaScript builds with a dynamic key have to be registered,
// otherwise a gap in a catalog would stay invisible.

import { readFile, readdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { join, relative } from "node:path";

const root = fileURLToPath(new URL("../internal/api/web/", import.meta.url));
const locales = ["en", "pt-BR"];

// runtimeKeys are the keys the application asks for with a dynamic name.
//
// The state of an instance and the code of an error come from the server,
// so the catalog has to know them before the first answer arrives.
const runtimeKeys = [
  "state.active",
  "state.available",
  "state.canceled",
  "state.completed",
  "state.destroyed",
  "state.disabled",
  "state.expired",
  "state.executing",
  "state.failed",
  "state.pending",
  "state.preparing",
  "state.queued",
  "state.ready",
  "state.requested",
  "state.revoked",
  "state.running",
  "state.starting",
  "state.stopped",
  "state.stopping",
  "state.succeeded",
  "state.unknown",
  "errors.authentication_failed",
  "errors.permission_denied",
  "errors.quota_exceeded",
  "errors.capability_unavailable",
  "errors.runtime_error",
  "errors.job_timeout",
  "errors.artifact_expired",
  "errors.human_action_required",
  "errors.not_found",
  "errors.invalid_request",
  "errors.conflict",
  "errors.internal_error",
];

// runtimePrefixes are the namespaces a dynamic key may come from.
const runtimePrefixes = ["state.", "errors."];

function flatten(value, prefix = "") {
  return Object.entries(value).flatMap(([key, child]) => {
    const path = prefix ? `${prefix}.${key}` : key;
    return child && typeof child === "object" ? flatten(child, path) : [path];
  });
}

// sources collects the files that may use a key.
async function sources(directory) {
  const found = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) {
      found.push(...(await sources(path)));
      continue;
    }
    if (!entry.name.endsWith(".js")) continue;
    if (entry.name.startsWith("test-")) continue;
    found.push(path);
  }
  return found.sort();
}

const catalogs = await Promise.all(
  locales.map(async (locale) => {
    const source = await readFile(`${root}/locales/${locale}.json`, "utf8");
    return [locale, JSON.parse(source)];
  }),
);

const canonicalKeys = new Set(flatten(catalogs[0][1]));
let failed = false;

// The catalogs have to carry exactly the same keys.
for (const [locale, catalog] of catalogs.slice(1)) {
  const keys = new Set(flatten(catalog));
  const missing = [...canonicalKeys].filter((key) => !keys.has(key));
  const extra = [...keys].filter((key) => !canonicalKeys.has(key));
  if (missing.length || extra.length) {
    failed = true;
    console.error(`${locale}: catalog mismatch`, { missing, extra });
  }
}

// No label may be empty.
for (const [locale, catalog] of catalogs) {
  const empty = [];
  for (const key of canonicalKeys) {
    const value = key.split(".").reduce((node, part) => node?.[part], catalog);
    if (typeof value !== "string" || value.trim() === "") empty.push(key);
  }
  if (empty.length) {
    failed = true;
    console.error(`${locale}: empty labels`, { empty });
  }
}

const html = await readFile(`${root}/index.html`, "utf8");
const files = await sources(root);
const used = new Set();

for (const source of [html, ...(await Promise.all(files.map((file) => readFile(file, "utf8"))))]) {
  for (const match of source.matchAll(/data-i18n(?:-[a-z-]+)?="([^"]+)"/g)) used.add(match[1]);
  for (const match of source.matchAll(/(?<![A-Za-z0-9_$.])t\(\s*"([^"]+)"/g)) used.add(match[1]);
}

for (const key of runtimeKeys) used.add(key);

// A dynamic key has to come from a registered namespace.
const dynamicPrefixes = [];
for (const source of await Promise.all(files.map((file) => readFile(file, "utf8")))) {
  for (const match of source.matchAll(/(?<![A-Za-z0-9_$.])t\(\s*`([^`]+)`/g)) {
    const prefix = match[1].split("${")[0];
    if (!runtimePrefixes.some((known) => prefix.startsWith(known) || known.startsWith(prefix))) {
      dynamicPrefixes.push(prefix);
    }
  }
}
if (dynamicPrefixes.length) {
  failed = true;
  console.error("a dynamic key comes from an unregistered namespace", { dynamicPrefixes });
}

// Every key that is used has to exist, and every key that exists has to be
// used somewhere.
const unknown = [...used].filter((key) => !canonicalKeys.has(key));
const unused = [...canonicalKeys].filter((key) => !used.has(key));

if (unknown.length || unused.length) {
  failed = true;
  console.error("catalog/usage mismatch", { unknown, unused });
}

if (failed) process.exit(1);
console.log(
  `i18n validation passed: ${locales.length} locales, ${canonicalKeys.size} keys, ` +
    `${used.size} keys in use, ${runtimeKeys.length} runtime keys, ${files.length} sources`,
);
for (const file of files) console.log(`  scanned ${relative(root, file)}`);
