// ui.js holds the pieces of the interface that every view needs.
//
// Everything that renders a value of the server builds its DOM with
// createElement and textContent: a value is never parsed as markup.

import { t, currentLocale } from "./i18n.js";

const MODAL_FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

// el builds an element.
//
// The options are intentionally small: a class, a text, attributes and
// children. There is no way to inject markup through this helper.
export function el(tag, options = {}, children = []) {
  const node = document.createElement(tag);
  if (options.className) node.className = options.className;
  if (options.text !== undefined && options.text !== null) node.textContent = String(options.text);
  if (options.attrs) {
    for (const [name, value] of Object.entries(options.attrs)) {
      if (value === undefined || value === null || value === false) continue;
      node.setAttribute(name, value === true ? "" : String(value));
    }
  }
  if (options.props) {
    for (const [name, value] of Object.entries(options.props)) {
      if (value === undefined || value === null) continue;
      node[name] = value;
    }
  }
  for (const child of Array.isArray(children) ? children : [children]) {
    if (child === undefined || child === null || child === false) continue;
    node.append(typeof child === "string" ? document.createTextNode(child) : child);
  }
  return node;
}

export function clear(node) {
  node.replaceChildren();
}

// ------------------------------------------------------------- formatting

function numberFormat(options) {
  try {
    return new Intl.NumberFormat(currentLocale(), options);
  } catch {
    return new Intl.NumberFormat("en", options);
  }
}

function relativeFormat() {
  try {
    return new Intl.RelativeTimeFormat(currentLocale(), { numeric: "auto" });
  } catch {
    return new Intl.RelativeTimeFormat("en", { numeric: "auto" });
  }
}

export function formatDateTime(value) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat(currentLocale(), { dateStyle: "medium", timeStyle: "short" }).format(date);
}

export function formatRelativeTime(value) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  const seconds = Math.round((date.getTime() - Date.now()) / 1000);
  const format = relativeFormat();
  if (Math.abs(seconds) < 60) return format.format(seconds, "second");
  if (Math.abs(seconds) < 3600) return format.format(Math.round(seconds / 60), "minute");
  if (Math.abs(seconds) < 86400) return format.format(Math.round(seconds / 3600), "hour");
  return format.format(Math.round(seconds / 86400), "day");
}

// formatSeconds renders a duration such as the remaining lifetime.
export function formatSeconds(seconds) {
  const value = Number(seconds);
  if (!Number.isFinite(value)) return "—";
  const absolute = Math.abs(value);
  let unit = "second";
  let amount = value;
  if (absolute >= 86400) {
    unit = "day";
    amount = value / 86400;
  } else if (absolute >= 3600) {
    unit = "hour";
    amount = value / 3600;
  } else if (absolute >= 60) {
    unit = "minute";
    amount = value / 60;
  }
  return numberFormat({
    style: "unit",
    unit,
    unitDisplay: "short",
    maximumFractionDigits: absolute < 10 ? 1 : 0,
  }).format(amount);
}

// formatMilliseconds renders the duration of a command.
export function formatMilliseconds(milliseconds) {
  const value = Number(milliseconds);
  if (!Number.isFinite(value)) return "—";
  if (value < 1000) {
    return numberFormat({ style: "unit", unit: "millisecond", unitDisplay: "narrow", maximumFractionDigits: 0 }).format(value);
  }
  return numberFormat({ style: "unit", unit: "second", unitDisplay: "short", maximumFractionDigits: 2 }).format(value / 1000);
}

export function formatBytes(bytes) {
  const value = Number(bytes);
  if (!Number.isFinite(value)) return "—";
  return numberFormat({
    style: "unit",
    unit: "byte",
    // The long form keeps the plural of the unit correct in every language.
    unitDisplay: "long",
    maximumFractionDigits: value < 1024 ? 0 : 1,
  }).format(value);
}

export function formatCount(count) {
  return numberFormat().format(Number(count) || 0);
}

// ------------------------------------------------------------------ state

// STATE_TONES maps a state of the control plane to a tone and a glyph.
//
// The glyph is part of the badge, so the state never depends on the color
// alone.
const STATE_TONES = {
  ready: { tone: "ok", icon: "✔" },
  completed: { tone: "ok", icon: "✔" },
  succeeded: { tone: "ok", icon: "✔" },
  available: { tone: "ok", icon: "✔" },
  active: { tone: "ok", icon: "✔" },
  queued: { tone: "active", icon: "●" },
  preparing: { tone: "active", icon: "●" },
  starting: { tone: "active", icon: "●" },
  running: { tone: "active", icon: "●" },
  executing: { tone: "active", icon: "●" },
  exporting: { tone: "active", icon: "●" },
  requested: { tone: "active", icon: "●" },
  stopping: { tone: "warn", icon: "▲" },
  pending: { tone: "warn", icon: "▲" },
  failed: { tone: "danger", icon: "✕" },
  expired: { tone: "danger", icon: "✕" },
  deleted: { tone: "danger", icon: "✕" },
  canceled: { tone: "muted", icon: "◼" },
  destroyed: { tone: "muted", icon: "◼" },
  stopped: { tone: "muted", icon: "◼" },
  revoked: { tone: "muted", icon: "◼" },
};

// stateBadge renders a state of the control plane.
export function stateBadge(value) {
  const key = String(value || "").trim().toLowerCase();
  const tone = STATE_TONES[key] || { tone: "muted", icon: "◼" };
  const label = t(`state.${key}`);
  const text = label === `state.${key}` ? key || t("state.unknown") : label;
  return el("span", { className: `badge badge--${tone.tone}` }, [
    el("span", { className: "badge__icon", text: tone.icon, attrs: { "aria-hidden": "true" } }),
    el("span", { text }),
  ]);
}

// ------------------------------------------------------------------ toasts

// toast shows a message in the notification area.
//
// An error carries the correlation identifier of the request, because that
// is the reference an operator needs to find the request in the logs.
export function toast(options = {}) {
  const { title, message, type = "info", meta = "" } = options;
  const host = document.querySelector("#toasts");
  if (!host) return;

  const close = el("button", {
    className: "toast__close",
    text: "×",
    attrs: { type: "button", "aria-label": t("toast.dismiss") },
  });
  const node = el("div", { className: `toast toast--${type}`, attrs: { role: type === "error" ? "alert" : "status" } }, [
    el("p", { className: "toast__title", text: title }),
    close,
    message ? el("p", { className: "toast__message", text: message }) : null,
    meta ? el("p", { className: "toast__meta", text: meta }) : null,
  ]);

  close.addEventListener("click", () => node.remove());
  host.append(node);
  setTimeout(() => node.remove(), type === "error" ? 12000 : 6000);
}

// reportError renders a failure of the API.
//
// The stable code and the correlation identifier are shown next to the
// message of the server, so a report to the operator is actionable.
export function reportError(error, title) {
  const message = error?.message || t("errors.unknown");
  const code = error?.code || "unknown";
  const correlation = error?.correlationId;
  const localized = t(`errors.${code}`);
  const label = localized === `errors.${code}` ? t("errors.unknown") : localized;
  const meta = correlation
    ? t("errors.correlation", { id: correlation })
    : t("errors.reference", { code: label });
  toast({
    title: title || t("errors.title"),
    message,
    type: "error",
    meta,
  });
}

// ------------------------------------------------------------------ modals

// openModal shows a dialog over the application.
//
// The dialog traps the focus, closes on Escape and restores the focus of
// the element that opened it.
export function openModal(options = {}) {
  const { title, subtitle = "", body, footer = [], onClose } = options;
  const host = document.querySelector("#modal-root");
  if (!host) return { close() {} };

  const previous = document.activeElement;
  const closeButton = el("button", {
    className: "button button--ghost button--small modal__close",
    text: "×",
    attrs: { type: "button", "aria-label": t("modal.close") },
  });
  const dialog = el(
    "div",
    { className: "modal__dialog", attrs: { role: "dialog", "aria-modal": "true", "aria-label": title } },
    [
      el("div", { className: "modal__header" }, [
        el("div", {}, [
          el("h2", { className: "modal__title", text: title }),
          subtitle ? el("p", { className: "modal__subtitle", text: subtitle }) : null,
        ]),
        closeButton,
      ]),
      el("div", { className: "modal__body" }, Array.isArray(body) ? body : [body]),
      footer.length ? el("div", { className: "modal__footer" }, footer) : null,
    ],
  );
  const overlay = el("div", { className: "modal" }, [dialog]);

  function close() {
    overlay.remove();
    document.removeEventListener("keydown", onKeydown, true);
    if (previous instanceof HTMLElement) previous.focus();
    if (typeof onClose === "function") onClose();
  }

  function onKeydown(event) {
    if (event.key === "Escape") {
      event.stopPropagation();
      close();
      return;
    }
    if (event.key !== "Tab") return;
    const focusable = [...dialog.querySelectorAll(MODAL_FOCUSABLE)].filter((node) => node.offsetParent !== null);
    if (!focusable.length) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  closeButton.addEventListener("click", close);
  overlay.addEventListener("mousedown", (event) => {
    if (event.target === overlay) close();
  });
  document.addEventListener("keydown", onKeydown, true);

  host.append(overlay);
  const target = dialog.querySelector("input, textarea, select, button:not(.modal__close)");
  if (target instanceof HTMLElement) target.focus();
  return { close, dialog };
}

// confirmDialog asks the operator to confirm a destructive action.
export function confirmDialog(options = {}) {
  const { title, message, confirmLabel, onConfirm, tone = "danger" } = options;
  return new Promise((resolve) => {
    // The promise may be settled more than once: closing the dialog fires the
    // onClose callback of the modal and the button of the outcome resolves the
    // promise as well. Only the first resolution counts, so the outcome the
    // operator chose is always the one that is returned.
    let settled = false;
    const settle = (value) => {
      if (settled) return;
      settled = true;
      resolve(value);
    };
    const confirmButton = el("button", {
      className: `button button--${tone}`,
      text: confirmLabel || t("common.confirm"),
      attrs: { type: "button" },
    });
    const cancelButton = el("button", {
      className: "button button--ghost",
      text: t("common.cancel"),
      attrs: { type: "button" },
    });
    const modal = openModal({
      title,
      body: el("p", { className: "view__subtitle", text: message }),
      footer: [cancelButton, confirmButton],
      // A dialog that is dismissed without an explicit choice is a refusal.
      onClose: () => settle(false),
    });

    cancelButton.addEventListener("click", () => {
      modal.close();
      settle(false);
    });
    confirmButton.addEventListener("click", () => {
      modal.close();
      settle(true);
    });
  });
}

// ------------------------------------------------------------ empty states

export function emptyState(options = {}) {
  const { icon = "◇", title, body = "" } = options;
  return el("div", { className: "empty" }, [
    el("span", { className: "empty__icon", text: icon, attrs: { "aria-hidden": "true" } }),
    el("p", { className: "empty__title", text: title }),
    body ? el("p", { className: "empty__body", text: body }) : null,
  ]);
}

export function skeletonBlock(height = "1rem", width = "100%") {
  return el("div", { className: "skeleton", attrs: { style: `height:${height};width:${width}` } });
}

// ------------------------------------------------------------------ misc

export function definitionList(entries) {
  const list = el("dl", { className: "definition" });
  for (const [term, value] of entries) {
    if (value === undefined || value === null || value === "") continue;
    list.append(el("dt", { text: term }));
    list.append(typeof value === "string" ? el("dd", { text: value }) : el("dd", {}, value));
  }
  return list;
}

// meter renders the usage of a resource of the host.
export function meter(name, used, total) {
  const totalValue = Number(total) || 0;
  const usedValue = Number(used) || 0;
  const ratio = totalValue > 0 ? Math.min(1, Math.max(0, usedValue / totalValue)) : 0;
  const tone = ratio > 0.9 ? "danger" : ratio > 0.7 ? "warn" : "";
  return el("div", { className: "resource" }, [
    el("div", { className: "resource__row" }, [
      el("span", { className: "resource__name", text: name }),
      el("span", {
        className: "resource__value",
        text: t("overview.used", {
          used: formatCount(usedValue),
          total: formatCount(totalValue),
        }),
      }),
    ]),
    el(
      "div",
      {
        className: "meter",
        attrs: {
          role: "meter",
          "aria-valuenow": String(Math.round(ratio * 100)),
          "aria-valuemin": "0",
          "aria-valuemax": "100",
          "aria-label": name,
        },
      },
      [el("div", { className: `meter__fill${tone ? ` meter__fill--${tone}` : ""}`, attrs: { style: `width:${ratio * 100}%` } })],
    ),
  ]);
}
