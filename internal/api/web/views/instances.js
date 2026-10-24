// views/instances.js manages the disposable workstations of the node.
//
// The table carries the actions that change the state of an instance. Every
// action asks the server first; the view never assumes that a request
// worked.

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
import { instanceRemainingText } from "./common.js";
import { openExecModal } from "./exec.js";

export async function mount(container, context) {
  const [instances, templates] = await Promise.all([api.instances(), api.templates()]);

  const view = el("div", { className: "view" });
  const refreshButton = el("button", {
    className: "button button--ghost",
    text: t("app.refresh"),
    attrs: { type: "button" },
  });
  // The button and the form share their state, so both are built together.
  const formToggle = el("button", {
    className: "button button--primary",
    text: t("instances.new"),
    attrs: { type: "button", "aria-expanded": "false", "aria-controls": "instances-form" },
  });

  view.append(
    el("header", { className: "view__header" }, [
      el("div", {}, [
        el("p", { className: "eyebrow", text: t("instances.eyebrow") }),
        el("h1", { text: t("instances.title") }),
        el("p", { className: "view__subtitle", text: t("instances.subtitle") }),
      ]),
      el("div", { className: "view__actions" }, [refreshButton, formToggle]),
    ]),
  );

  const formCard = el("section", { className: "card", attrs: { "aria-labelledby": "instances-form-title" } });
  // The form is opened by the button of the header, not by default.
  formCard.hidden = true;
  const tableHost = el("section", { className: "card", attrs: { "aria-labelledby": "instances-table-title" } });

  view.append(formCard, tableHost);
  container.append(view);

  const form = buildForm(templates, context, formToggle);
  formCard.append(form.node);

  const prefill = context.route.query;
  if (prefill.get("template")) {
    formCard.hidden = false;
    form.toggleButton.setAttribute("aria-expanded", "true");
    form.node.querySelector("#instance-template").value = prefill.get("template");
    if (prefill.get("version")) {
      form.node.querySelector("#instance-version").setAttribute("value", prefill.get("version"));
    }
    toast({ title: t("instances.title"), message: t("templates.prefill", { template: prefill.get("template") }), type: "info" });
  }

  formToggle.addEventListener("click", () => {
    const expanded = formToggle.getAttribute("aria-expanded") === "true";
    formToggle.setAttribute("aria-expanded", expanded ? "false" : "true");
    formCard.hidden = expanded;
    if (!expanded) form.node.querySelector("#instance-template").focus();
  });

  refreshButton.addEventListener("click", () => context.reload());

  const countdown = renderInstances(tableHost, instances, context);
  return () => clearInterval(countdown);
}

function buildForm(templates, context, toggleButton) {
  const select = el("select", { className: "input", attrs: { id: "instance-template", name: "template_id", required: true } });
  for (const template of templates) {
    const option = el("option", { text: templateLabel(template) });
    option.value = template.id || "";
    select.append(option);
  }
  if (!templates.length) {
    const option = el("option", { text: t("templates.empty") });
    option.value = "";
    select.append(option);
  }

  const version = el("input", {
    className: "input",
    attrs: { type: "text", id: "instance-version", name: "template_version", placeholder: t("instances.latestVersion") },
  });
  const name = el("input", {
    className: "input",
    attrs: { type: "text", id: "instance-name", name: "name", autocomplete: "off" },
  });
  const ttl = el("input", {
    className: "input",
    attrs: { type: "number", id: "instance-ttl", name: "ttl_seconds", min: "1", step: "1" },
  });
  const env = el("textarea", {
    className: "input",
    attrs: { id: "instance-env", name: "env", spellcheck: "false", rows: "4" },
  });
  const secrets = el("input", {
    className: "input",
    attrs: { type: "text", id: "instance-secrets", name: "secret_refs", autocomplete: "off" },
  });
  const idempotency = el("input", {
    className: "input",
    attrs: { type: "text", id: "instance-idempotency", name: "idempotency_key", autocomplete: "off" },
  });

  const alert = el("p", { className: "form-alert", attrs: { role: "alert", hidden: "hidden" } });
  const submit = el("button", { className: "button button--primary", text: t("instances.create"), attrs: { type: "submit" } });

  const node = el("form", { className: "stack", attrs: { id: "instances-form", novalidate: true } }, [
    alert,
    el("div", { className: "card__header" }, [
      el("div", {}, [
        el("h2", { className: "card__title", text: t("instances.formTitle"), attrs: { id: "instances-form-title" } }),
        el("p", { className: "card__hint", text: t("instances.formHint") }),
      ]),
    ]),
    el("div", { className: "form-grid" }, [
      el("div", { className: "field" }, [
        el("label", { text: t("instances.form.template"), attrs: { for: "instance-template" } }),
        select,
      ]),
      el("div", { className: "field" }, [
        el("label", { text: t("instances.form.version"), attrs: { for: "instance-version" } }),
        version,
        el("p", { className: "field__hint", text: t("instances.form.versionHint") }),
      ]),
      el("div", { className: "field" }, [
        el("label", { text: t("instances.form.name"), attrs: { for: "instance-name" } }),
        name,
      ]),
      el("div", { className: "field" }, [
        el("label", { text: t("instances.form.ttl"), attrs: { for: "instance-ttl" } }),
        ttl,
        el("p", { className: "field__hint", text: t("instances.form.ttlHint") }),
      ]),
    ]),
    el("div", { className: "field" }, [
      el("label", { text: t("instances.form.env"), attrs: { for: "instance-env" } }),
      env,
      el("p", { className: "field__hint", text: t("instances.form.envHint") }),
    ]),
    el("div", { className: "form-grid" }, [
      el("div", { className: "field" }, [
        el("label", { text: t("instances.form.secrets"), attrs: { for: "instance-secrets" } }),
        secrets,
        el("p", { className: "field__hint", text: t("instances.form.secretsHint") }),
      ]),
      el("div", { className: "field" }, [
        el("label", { text: t("instances.form.idempotency"), attrs: { for: "instance-idempotency" } }),
        idempotency,
        el("p", { className: "field__hint", text: t("instances.form.idempotencyHint") }),
      ]),
    ]),
    el("div", { className: "form-actions" }, [submit]),
  ]);

  node.addEventListener("submit", async (event) => {
    event.preventDefault();
    alert.hidden = true;

    let environment;
    const rawEnv = env.value.trim();
    if (rawEnv) {
      try {
        environment = JSON.parse(rawEnv);
      } catch (error) {
        alert.textContent = t("instances.form.envInvalid");
        alert.hidden = false;
        env.focus();
        return;
      }
      if (typeof environment !== "object" || environment === null || Array.isArray(environment)) {
        alert.textContent = t("instances.form.envInvalid");
        alert.hidden = false;
        env.focus();
        return;
      }
    }

    submit.disabled = true;
    submit.textContent = t("instances.creating");
    try {
      const answer = await api.createInstance({
        templateId: select.value,
        templateVersion: version.value.trim(),
        name: name.value.trim(),
        ttlSeconds: Number(ttl.value) || undefined,
        env: environment,
        secretRefs: secrets.value.split(",").map((value) => value.trim()).filter(Boolean),
        idempotencyKey: idempotency.value.trim(),
      });
      toast({
        title: t("instances.created"),
        message: answer.jobId ? t("instances.jobCreated", { job: answer.jobId }) : t("instances.created"),
        type: "success",
      });
      context.reload();
    } catch (error) {
      alert.textContent = error?.message || t("errors.unknown");
      alert.hidden = false;
      reportError(error, t("instances.formTitle"));
    } finally {
      submit.disabled = false;
      submit.textContent = t("instances.create");
    }
  });

  return { node, toggleButton };
}

function templateLabel(template) {
  const title = template.title || template.id || t("common.unknown");
  const version = template.version ? ` · ${template.version}` : "";
  return `${title}${version}`;
}

// renderInstances draws the table of instances.
//
// The countdown of the lifetime is refreshed by a timer, so the column stays
// correct without asking the server again.
function renderInstances(host, instances, context) {
  host.replaceChildren(
    el("div", { className: "card__header" }, [
      el("div", {}, [
        el("h2", { className: "card__title", text: t("instances.tableTitle"), attrs: { id: "instances-table-title" } }),
        el("p", { className: "card__hint", text: t("instances.tableHint") }),
      ]),
    ]),
  );

  if (!instances.length) {
    host.append(emptyState({ icon: "▣", title: t("instances.empty") }));
    return null;
  }

  const countdownCells = [];
  const rows = instances.map((instance) => {
    const countdown = el("td", { className: "cell--num cell--muted", text: instanceRemainingText(instance) });
    countdownCells.push({ instance, cell: countdown });

    const detailButton = el("button", {
      className: "link-button mono",
      text: instance.id || "",
      attrs: { type: "button", "aria-label": t("instances.detailsFor", { id: instance.id }) },
    });
    detailButton.addEventListener("click", () => showInstance(instance));

    const execButton = el("button", {
      className: "button button--ghost button--small",
      text: t("instances.exec"),
      attrs: { type: "button", "aria-label": t("instances.execFor", { id: instance.id }) },
    });
    const consoleButton = el("button", {
      className: "button button--ghost button--small",
      text: t("instances.console"),
      attrs: { type: "button", "aria-label": t("instances.consoleFor", { id: instance.id }) },
    });
    const stopButton = el("button", {
      className: "button button--ghost button--small",
      text: t("instances.stop"),
      attrs: { type: "button", "aria-label": t("instances.stopFor", { id: instance.id }) },
    });
    const destroyButton = el("button", {
      className: "button button--danger button--small",
      text: t("instances.destroy"),
      attrs: { type: "button", "aria-label": t("instances.destroyFor", { id: instance.id }) },
    });

    // Every action is bound to the instance of its own row, so the table
    // may grow a column without breaking the wiring.
    execButton.addEventListener("click", () => openExecModal({ instance, onDone: () => context.reload() }));
    consoleButton.addEventListener("click", () => openConsole(instance));
    stopButton.addEventListener("click", () => stopInstance(instance, context));
    destroyButton.addEventListener("click", () => destroyInstance(instance, context));

    return el("tr", {}, [
      el("td", { className: "cell--id" }, [detailButton]),
      el("td", { text: instance.template_id || t("common.unknown") }),
      el("td", {}, [stateBadge(instance.state)]),
      el("td", { className: "cell--muted", text: instance.runtime || t("common.unknown") }),
      el("td", { className: "cell--muted", text: formatDateTime(instance.created_at) }),
      countdown,
      el("td", { className: "cell--actions" }, [execButton, consoleButton, stopButton, destroyButton]),
    ]);
  });

  const table = el("table", { attrs: { "aria-labelledby": "instances-table-title" } }, [
    el("thead", {}, [
      el("tr", {}, [
        el("th", { text: t("common.id") }),
        el("th", { text: t("common.template") }),
        el("th", { text: t("common.state") }),
        el("th", { text: t("common.runtime") }),
        el("th", { text: t("common.created") }),
        el("th", { text: t("common.expiresIn") }),
        el("th", { className: "cell--actions", text: t("common.actions") }),
      ]),
    ]),
    el("tbody", {}, rows),
  ]);

  host.append(el("div", { className: "table-wrap" }, [table]));

  return setInterval(() => {
    for (const entry of countdownCells) entry.cell.textContent = instanceRemainingText(entry.instance);
  }, 1000);
}

// showInstance opens the record of one instance.
async function showInstance(instance) {
  try {
    const record = await api.instance(instance.id);
    const body = definitionList([
      [t("common.id"), record.id],
      [t("common.name"), record.name],
      [t("common.template"), record.template_id],
      [t("common.runtime"), record.runtime],
      [t("common.state"), stateBadge(record.state)],
      [t("common.job"), record.job_id],
      [t("common.owner"), record.owner_id],
      [t("common.created"), formatDateTime(record.created_at)],
      [t("common.updated"), formatDateTime(record.updated_at)],
      [t("common.expires"), formatDateTime(record.expires_at)],
    ]);
    const close = el("button", { className: "button button--ghost", text: t("modal.close"), attrs: { type: "button" } });
    const modal = openModal({ title: t("instances.detailTitle", { id: instance.id }), body, footer: [close] });
    close.addEventListener("click", () => modal.close());
  } catch (error) {
    reportError(error, t("instances.details"));
  }
}

async function openConsole(instance) {
  try {
    const endpoint = await api.console(instance.id);
    const url = endpoint?.url || "";
    if (!url) {
      toast({ title: t("instances.console"), message: t("instances.consoleUnavailable"), type: "warn" });
      return;
    }
    window.open(url, "_blank", "noopener,noreferrer");
    toast({ title: t("instances.console"), message: url, type: "info" });
  } catch (error) {
    reportError(error, t("instances.console"));
  }
}

async function stopInstance(instance, context) {
  const confirmed = await confirmDialog({
    title: t("instances.stop"),
    message: t("instances.stopConfirm", { id: instance.id }),
    confirmLabel: t("instances.stop"),
    tone: "ghost",
  });
  if (!confirmed) return;
  try {
    await api.stopInstance(instance.id);
    toast({ title: t("instances.stop"), message: t("instances.stopped", { id: instance.id }), type: "success" });
    context.reload();
  } catch (error) {
    reportError(error, t("instances.stop"));
  }
}

async function destroyInstance(instance, context) {
  const confirmed = await confirmDialog({
    title: t("instances.destroy"),
    message: t("instances.destroyConfirm", { id: instance.id }),
    confirmLabel: t("instances.destroy"),
  });
  if (!confirmed) return;
  try {
    await api.destroyInstance(instance.id);
    toast({ title: t("instances.destroy"), message: t("instances.destroyed", { id: instance.id }), type: "success" });
    context.reload();
  } catch (error) {
    reportError(error, t("instances.destroy"));
  }
}
