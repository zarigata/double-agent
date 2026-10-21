import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL(".", import.meta.url));
const locales = ["en", "pt-BR"];

function flatten(value, prefix = "") {
  return Object.entries(value).flatMap(([key, child]) => {
    const path = prefix ? `${prefix}.${key}` : key;
    return child && typeof child === "object" ? flatten(child, path) : [path];
  });
}

const catalogs = await Promise.all(
  locales.map(async (locale) => {
    const source = await readFile(`${root}/locales/${locale}.json`, "utf8");
    return [locale, JSON.parse(source)];
  }),
);

const canonicalKeys = new Set(flatten(catalogs[0][1]));
let failed = false;

for (const [locale, catalog] of catalogs.slice(1)) {
  const keys = new Set(flatten(catalog));
  const missing = [...canonicalKeys].filter((key) => !keys.has(key));
  const extra = [...keys].filter((key) => !canonicalKeys.has(key));
  if (missing.length || extra.length) {
    failed = true;
    console.error(`${locale}: catalog mismatch`, { missing, extra });
  }
}

const html = await readFile(`${root}/index.html`, "utf8");
const runtimeKeys = ["language.changed"];
const usedKeys = [
  ...html.matchAll(/data-i18n="([^"]+)"/g),
].map((match) => match[1]).concat(runtimeKeys);
const unknown = usedKeys.filter((key) => !canonicalKeys.has(key));
const unused = [...canonicalKeys].filter((key) => !usedKeys.includes(key));

if (unknown.length || unused.length) {
  failed = true;
  console.error("HTML/catalog mismatch", { unknown, unused });
}

if (failed) process.exit(1);
console.log(`i18n validation passed: ${locales.length} locales, ${canonicalKeys.size} keys`);
