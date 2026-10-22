// views/templates.js shows the catalogue of the environments.

import { api } from "../api.js";
import { t } from "../i18n.js";
import { definitionList, el, emptyState, openModal, reportError } from "../ui.js";

export async function mount(container, context) {
  const templates = await api.templates();

  const view = el("div", { className: "view" });
  const refreshButton = el("button", {
    className: "button button--ghost",
    text: t("app.refresh"),
    attrs: { type: "button" },
  });

  view.append(
    el("header", { className: "view__header" }, [
      el("div", {}, [
        el("p", { className: "eyebrow", text: t("templates.eyebrow") }),
        el("h1", { text: t("templates.title") }),
        el("p", { className: "view__subtitle", text: t("templates.subtitle") }),
      ]),
      el("div", { className: "view__actions" }, [refreshButton]),
    ]),
  );

  const grid = el("div", { className: "grid grid--3" });
  view.append(grid);
  container.append(view);

  refreshButton.addEventListener("click", () => context.reload());

  if (!templates.length) {
    grid.append(emptyState({ icon: "▧", title: t("templates.empty") }));
    return;
  }

  for (const template of templates) {
    grid.append(templateCard(template, context));
  }
}

// showTemplate opens the published version of a template.
//
// The digest is the fingerprint of the specification, so an operator can
// tell two versions of the same template apart.
async function showTemplate(template) {
  try {
    const record = await api.template(template.id, template.version);
    const body = definitionList([
      [t("common.id"), record.id],
      [t("common.name"), record.title],
      [t("common.version"), record.version],
      [t("common.runtime"), record.runtime],
      [t("common.image"), record.image],
      [t("templates.digest"), record.digest],
      [t("common.description"), record.description],
    ]);
    const close = el("button", { className: "button button--ghost", text: t("modal.close"), attrs: { type: "button" } });
    const modal = openModal({
      title: t("templates.detailTitle", { id: template.id || "" }),
      subtitle: t("templates.detailHint"),
      body,
      footer: [close],
    });
    close.addEventListener("click", () => modal.close());
  } catch (error) {
    reportError(error, t("templates.details"));
  }
}

function templateCard(template, context) {
  const resources = template.resources && typeof template.resources === "object" ? template.resources : null;
  const runButton = el("button", {
    className: "button button--primary button--small",
    text: t("templates.run"),
    attrs: { type: "button" },
  });
  runButton.addEventListener("click", () => {
    const query = new URLSearchParams();
    query.set("template", template.id || "");
    if (template.version) query.set("version", String(template.version));
    context.navigate(`#/instances?${query.toString()}`);
  });

  const detailsButton = el("button", {
    className: "button button--ghost button--small",
    text: t("templates.details"),
    attrs: { type: "button", "aria-label": t("templates.detailsFor", { template: template.id || "" }) },
  });
  detailsButton.addEventListener("click", () => showTemplate(template));

  const meta = el("dl", { className: "template-card__meta" }, [
    el("div", {}, [
      el("dt", { text: t("common.id") }),
      el("dd", { className: "mono", text: template.id || "" }),
    ]),
    el("div", {}, [
      el("dt", { text: t("common.version") }),
      el("dd", { text: template.version ? String(template.version) : t("common.unknown") }),
    ]),
    el("div", {}, [
      el("dt", { text: t("common.runtime") }),
      el("dd", { text: template.runtime || t("common.unknown") }),
    ]),
    el("div", {}, [
      el("dt", { text: t("common.image") }),
      el("dd", { className: "mono", text: template.image || t("common.none") }),
    ]),
  ]);

  if (resources) {
    const entries = Object.entries(resources).map(([key, value]) =>
      el("div", {}, [
        el("dt", { text: key }),
        el("dd", { text: typeof value === "object" ? JSON.stringify(value) : String(value) }),
      ]),
    );
    for (const entry of entries) meta.append(entry);
  }

  return el("article", { className: "card template-card" }, [
    el("h2", { className: "template-card__title", text: template.title || template.id || t("common.unknown") }),
    el("p", { className: "template-card__description", text: template.description || t("templates.noDescription") }),
    meta,
    el("div", { className: "template-card__actions" }, [runButton, detailsButton]),
  ]);
}
