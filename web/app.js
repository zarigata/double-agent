const STORAGE_KEY = "double-agent.locale";

const supportedLocales = {
  en: { nativeName: "English" },
  "pt-BR": { nativeName: "Português do Brasil" },
};

const catalogs = new Map();

function normalizeLocale(locale) {
  const value = String(locale || "").toLowerCase();
  if (value === "pt" || value.startsWith("pt-")) return "pt-BR";
  if (value === "en" || value.startsWith("en-")) return "en";
  return null;
}

function getPreferredLocale() {
  const stored = normalizeLocale(localStorage.getItem(STORAGE_KEY));
  if (stored) return stored;

  for (const locale of navigator.languages || [navigator.language]) {
    const normalized = normalizeLocale(locale);
    if (normalized) return normalized;
  }

  return "en";
}

function readKey(catalog, key) {
  return key.split(".").reduce((value, part) => value?.[part], catalog);
}

async function loadCatalog(locale) {
  if (catalogs.has(locale)) return catalogs.get(locale);
  const response = await fetch(`./locales/${locale}.json`);
  if (!response.ok) throw new Error(`Unable to load locale: ${locale}`);
  const catalog = await response.json();
  catalogs.set(locale, catalog);
  return catalog;
}

async function applyLocale(requestedLocale, announce = false) {
  const locale = normalizeLocale(requestedLocale) || "en";
  const [english, selected] = await Promise.all([
    loadCatalog("en"),
    locale === "en" ? loadCatalog("en") : loadCatalog(locale).catch(() => null),
  ]);

  document.querySelectorAll("[data-i18n]").forEach((element) => {
    const key = element.dataset.i18n;
    const value = readKey(selected, key) ?? readKey(english, key);
    if (typeof value === "string") element.textContent = value;
  });

  document.documentElement.lang = locale;
  document.documentElement.dir = "ltr";
  document.querySelector("#language").value = locale;
  localStorage.setItem(STORAGE_KEY, locale);

  if (announce) {
    const status = document.querySelector("#language-status");
    status.textContent = readKey(selected, "language.changed") ?? readKey(english, "language.changed");
  }
}

async function start() {
  const selector = document.querySelector("#language");
  selector.replaceChildren(
    ...Object.entries(supportedLocales).map(([locale, metadata]) => {
      const option = document.createElement("option");
      option.value = locale;
      option.textContent = metadata.nativeName;
      return option;
    }),
  );

  selector.addEventListener("change", (event) => {
    applyLocale(event.target.value, true).catch(() => applyLocale("en", true));
  });

  await applyLocale(getPreferredLocale());
}

start().catch(() => {
  document.documentElement.lang = "en";
});
