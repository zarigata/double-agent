// views/jobs.js lists the work the control plane has scheduled.

import { api } from "../api.js";
import { t } from "../i18n.js";
import {
  confirmDialog,
  definitionList,
  el,
  emptyState,
  formatDateTime,
  openModal,
  reportError,
  stateBadge,
  toast,
} from "../ui.js";
import { errorCode, jobDurationText } from "./common.js";

const TERMINAL_STATES = new Set(["completed", "failed", "canceled", "cancelled", "expired"]);

export async function mount(container, context) {
  const jobs = await api.jobs();

  const view = el("div", { className: "view" });
  const refreshButton = el("button", {
    className: "button button--ghost",
    text: t("app.refresh"),
    attrs: { type: "button" },
  });

  view.append(
    el("header", { className: "view__header" }, [
      el("div", {}, [
        el("p", { className: "eyebrow", text: t("jobs.eyebrow") }),
        el("h1", { text: t("jobs.title") }),
        el("p", { className: "view__subtitle", text: t("jobs.subtitle") }),
      ]),
      el("div", { className: "view__actions" }, [refreshButton]),
    ]),
  );

  const tableHost = el("section", { className: "card", attrs: { "aria-labelledby": "jobs-table-title" } });
  view.append(tableHost);
  container.append(view);

  refreshButton.addEventListener("click", () => context.reload());
  renderJobs(tableHost, jobs, context);
}

function renderJobs(host, jobs, context) {
  host.replaceChildren(
    el("div", { className: "card__header" }, [
      el("div", {}, [
        el("h2", { className: "card__title", text: t("jobs.tableTitle"), attrs: { id: "jobs-table-title" } }),
        el("p", { className: "card__hint", text: t("jobs.tableHint") }),
      ]),
    ]),
  );

  if (!jobs.length) {
    host.append(emptyState({ icon: "▤", title: t("jobs.empty") }));
    return;
  }

  const rows = jobs.map((job) => {
    const finished = TERMINAL_STATES.has(String(job.state || "").toLowerCase());
    const cancelButton = el("button", {
      className: "button button--ghost button--small",
      text: t("jobs.cancel"),
      attrs: { type: "button", "aria-label": t("jobs.cancelFor", { id: job.id }) },
    });
    cancelButton.disabled = finished;
    cancelButton.addEventListener("click", () => cancelJob(job, context));

    const detailButton = el("button", {
      className: "link-button mono",
      text: job.id || "",
      attrs: { type: "button", "aria-label": t("jobs.detailsFor", { id: job.id }) },
    });
    detailButton.addEventListener("click", () => showJob(job));

    return el("tr", {}, [
      el("td", { className: "cell--id" }, [detailButton]),
      el("td", { text: job.kind || t("common.unknown") }),
      el("td", {}, [stateBadge(job.state)]),
      el("td", { className: "cell--muted", text: formatDateTime(job.created_at) }),
      el("td", { className: "cell--num cell--muted", text: jobDurationText(job) }),
      el("td", { className: "cell--muted mono", text: job.error_code ? errorCode(job.error_code) : "—" }),
      el("td", { className: "cell--actions" }, [cancelButton]),
    ]);
  });

  const table = el("table", { attrs: { "aria-labelledby": "jobs-table-title" } }, [
    el("thead", {}, [
      el("tr", {}, [
        el("th", { text: t("common.id") }),
        el("th", { text: t("common.kind") }),
        el("th", { text: t("common.state") }),
        el("th", { text: t("common.created") }),
        el("th", { text: t("common.duration") }),
        el("th", { text: t("common.errorCode") }),
        el("th", { className: "cell--actions", text: t("common.actions") }),
      ]),
    ]),
    el("tbody", {}, rows),
  ]);

  host.append(el("div", { className: "table-wrap" }, [table]));
}

// showJob opens the record of one job.
async function showJob(job) {
  try {
    const record = await api.job(job.id);
    const body = definitionList([
      [t("common.id"), record.id],
      [t("common.kind"), record.kind],
      [t("common.state"), stateBadge(record.state)],
      [t("common.template"), record.template_id],
      [t("common.instance"), record.instance_id],
      [t("common.owner"), record.owner_id],
      [t("common.created"), formatDateTime(record.created_at)],
      [t("common.started"), formatDateTime(record.started_at)],
      [t("common.finished"), formatDateTime(record.finished_at)],
      [t("common.duration"), jobDurationText(record)],
      [t("common.errorCode"), record.error_code],
      [t("common.errorMessage"), record.error_message],
    ]);
    const close = el("button", { className: "button button--ghost", text: t("modal.close"), attrs: { type: "button" } });
    const modal = openModal({ title: t("jobs.detailTitle", { id: job.id }), body, footer: [close] });
    close.addEventListener("click", () => modal.close());
  } catch (error) {
    reportError(error, t("jobs.details"));
  }
}

async function cancelJob(job, context) {
  const confirmed = await confirmDialog({
    title: t("jobs.cancel"),
    message: t("jobs.cancelConfirm", { id: job.id }),
    confirmLabel: t("jobs.cancel"),
    tone: "ghost",
  });
  if (!confirmed) return;
  try {
    await api.cancelJob(job.id);
    toast({ title: t("jobs.cancel"), message: t("jobs.canceled", { id: job.id }), type: "success" });
    context.reload();
  } catch (error) {
    reportError(error, t("jobs.cancel"));
  }
}
