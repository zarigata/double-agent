// i18n.js loads the catalogs of the control-plane interface.
//
// The contract is the one of the landing page: nested JSON catalogs, a
// "data-i18n" attribute per element, English as the canonical fallback and
// the language preference in localStorage. The dashboard adds the runtime
// lookup t() because most of its labels are produced by JavaScript.

const STORAGE_KEY = "double-agent.locale";

const supportedLocales = {
  en: { nativeName: "English" },
  "pt-BR": { nativeName: "Português do Brasil" },
};

const catalogs = new Map();
const listeners = new Set();
let activeLocale = "en";

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
  // The catalog lives next to this module, so the loader does not depend on
  // the path the daemon happens to mount the interface at.
  const response = await fetch(new URL(`./locales/${locale}.json`, import.meta.url));
  if (!response.ok) throw new Error(`Unable to load locale: ${locale}`);
  const catalog = await response.json();
  catalogs.set(locale, catalog);
  return catalog;
}

// translate writes the localized text of one element.
//
// The attribute names are derived from the data-i18n attribute so that a
// placeholder or an accessible name can be localized as well.
function translate(element, catalog, fallback) {
  const key = element.dataset.i18n;
  const value = readKey(catalog, key) ?? readKey(fallback, key);
  if (typeof value === "string") element.textContent = value;
}

// The attributes that may carry a localized value. The list is explicit
// because the name of the data attribute is camel cased by the DOM.
const ATTRIBUTE_TARGETS = {
  i18nAriaLabel: "aria-label",
  i18nAriaDescription: "aria-description",
  i18nPlaceholder: "placeholder",
  i18nTitle: "title",
};

function translateAttributes(element, catalog, fallback) {
  for (const [source, target] of Object.entries(ATTRIBUTE_TARGETS)) {
    const key = element.dataset[source];
    if (!key) continue;
    const value = readKey(catalog, key) ?? readKey(fallback, key);
    if (typeof value === "string") element.setAttribute(target, value);
  }
}

// t returns the localized text of a key.
//
// The lookup never throws: a key that is missing from the catalog returns
// the key itself, which makes the gap visible without breaking the view.
export function t(key, vars) {
  const catalog = catalogs.get(activeLocale) || {};
  const fallback = catalogs.get("en") || {};
  const value = readKey(catalog, key) ?? readKey(fallback, key);
  if (typeof value !== "string") return key;
  if (!vars) return value;
  return value.replace(/\{(\w+)\}/g, (match, name) =>
    Object.prototype.hasOwnProperty.call(vars, name) ? String(vars[name]) : match,
  );
}

// currentLocale returns the locale that is in effect.
export function currentLocale() {
  return activeLocale;
}

// onLocaleChange registers a listener that runs after every change.
//
// The views rebuild their tables when the language changes, so the listener
// is how a rendered view learns that its labels are stale.
export function onLocaleChange(listener) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export async function applyLocale(requestedLocale, announce = false) {
  const locale = normalizeLocale(requestedLocale) || "en";
  const [english, selected] = await Promise.all([
    loadCatalog("en"),
    locale === "en" ? loadCatalog("en") : loadCatalog(locale).catch(() => null),
  ]);

  const catalog = selected || english;
  activeLocale = locale;

  document.querySelectorAll("[data-i18n]").forEach((element) => {
    translate(element, catalog, english);
  });
  document.querySelectorAll("[data-i18n-aria-label], [data-i18n-placeholder], [data-i18n-title]").forEach(
    (element) => {
      translateAttributes(element, catalog, english);
    },
  );

  document.documentElement.lang = locale;
  document.documentElement.dir = "ltr";
  const selector = document.querySelector("#language");
  if (selector) selector.value = locale;
  localStorage.setItem(STORAGE_KEY, locale);

  if (announce) {
    const status = document.querySelector("#language-status");
    if (status) status.textContent = t("language.changed");
  }

  for (const listener of listeners) {
    try {
      listener(locale);
    } catch (error) {
      console.error("a locale listener failed", error);
    }
  }
}

export async function startI18n() {
  const selector = document.querySelector("#language");
  if (selector) {
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
  }

  await applyLocale(getPreferredLocale());
}
