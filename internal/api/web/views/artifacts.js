// views/artifacts.js lists the files the workloads produced.

import { api, artifactDownloadURL } from "../api.js";
import { t } from "../i18n.js";
import { el, emptyState, formatBytes, formatDateTime, stateBadge } from "../ui.js";

export async function mount(container, context) {
  const artifacts = await api.artifacts();

  const view = el("div", { className: "view" });
  const refreshButton = el("button", {
    className: "button button--ghost",
    text: t("app.refresh"),
    attrs: { type: "button" },
  });

  view.append(
    el("header", { className: "view__header" }, [
      el("div", {}, [
        el("p", { className: "eyebrow", text: t("artifacts.eyebrow") }),
        el("h1", { text: t("artifacts.title") }),
        el("p", { className: "view__subtitle", text: t("artifacts.subtitle") }),
      ]),
      el("div", { className: "view__actions" }, [refreshButton]),
    ]),
  );

  const tableHost = el("section", { className: "card", attrs: { "aria-labelledby": "artifacts-table-title" } });
  view.append(tableHost);
  container.append(view);

  refreshButton.addEventListener("click", () => context.reload());
  renderArtifacts(tableHost, artifacts);
}

function renderArtifacts(host, artifacts) {
  host.replaceChildren(
    el("div", { className: "card__header" }, [
      el("div", {}, [
        el("h2", { className: "card__title", text: t("artifacts.tableTitle"), attrs: { id: "artifacts-table-title" } }),
        el("p", { className: "card__hint", text: t("artifacts.tableHint") }),
      ]),
    ]),
  );

  if (!artifacts.length) {
    host.append(emptyState({ icon: "◆", title: t("artifacts.empty") }));
    return;
  }

  const rows = artifacts.map((artifact) => {
    const available = String(artifact.state || "").toLowerCase() === "available";
    const link = el("a", {
      className: "button button--ghost button--small",
      text: t("artifacts.download"),
      attrs: { href: artifactDownloadURL(artifact.id), download: artifact.name || "" },
    });
    if (!available) {
      link.setAttribute("aria-disabled", "true");
      link.removeAttribute("href");
    }

    return el("tr", {}, [
      el("td", { className: "cell--id mono", text: artifact.id || "" }),
      el("td", { text: artifact.name || t("common.unknown") }),
      el("td", { className: "cell--num cell--muted", text: formatBytes(artifact.size) }),
      el("td", { className: "cell--muted", text: artifact.mime || t("common.unknown") }),
      el("td", {}, [stateBadge(artifact.state)]),
      el("td", { className: "cell--muted", text: formatDateTime(artifact.created_at) }),
      el("td", { className: "cell--actions" }, [link]),
    ]);
  });

  const table = el("table", { attrs: { "aria-labelledby": "artifacts-table-title" } }, [
    el("thead", {}, [
      el("tr", {}, [
        el("th", { text: t("common.id") }),
        el("th", { text: t("common.name") }),
        el("th", { text: t("common.size") }),
        el("th", { text: t("common.mime") }),
        el("th", { text: t("common.state") }),
        el("th", { text: t("common.created") }),
        el("th", { className: "cell--actions", text: t("common.actions") }),
      ]),
    ]),
    el("tbody", {}, rows),
  ]);

  host.append(el("div", { className: "table-wrap" }, [table]));
}
