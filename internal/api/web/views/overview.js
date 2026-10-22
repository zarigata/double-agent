// views/overview.js shows the state of the node.
//
// The view answers three questions: what the node can do, what it is
// running and what it has produced.

import { api } from "../api.js";
import { t } from "../i18n.js";
import {
  el,
  emptyState,
  formatBytes,
  formatCount,
  formatDateTime,
  formatSeconds,
  meter,
  stateBadge,
} from "../ui.js";
import { jobDurationText } from "./common.js";

export async function mount(container, context) {
  const [capabilities, health, instances, jobs, artifacts] = await Promise.all([
    api.capabilities(),
    api.health(),
    api.instances(),
    api.jobs(),
    api.artifacts(),
  ]);

  context.publishCapabilities(capabilities, health);

  const view = el("div", { className: "view" });
  view.append(
    el("header", { className: "view__header" }, [
      el("div", {}, [
        el("p", { className: "eyebrow", text: t("overview.eyebrow") }),
        el("h1", { text: t("overview.title") }),
        el("p", { className: "view__subtitle", text: t("overview.subtitle") }),
      ]),
      el("div", { className: "view__actions" }, [
        el("button", {
          className: "button button--ghost",
          text: t("app.refresh"),
          attrs: { type: "button", id: "overview-refresh" },
        }),
      ]),
    ]),
  );

  const counters = el("div", { className: "grid grid--3" }, [
    counter(t("overview.instances"), instances.length, t("overview.instancesNote")),
    counter(t("overview.jobs"), jobs.length, t("overview.jobsNote")),
    counter(t("overview.artifacts"), artifacts.length, t("overview.artifactsNote")),
  ]);
  view.append(counters);

  const node = el("div", { className: "grid grid--2" }, [
    runtimeCard(capabilities, health),
    resourcesCard(capabilities),
  ]);
  view.append(node);

  view.append(jobsCard(jobs));
  container.append(view);

  document.querySelector("#overview-refresh")?.addEventListener("click", () => context.reload());
}

function counter(label, value, note) {
  return el("div", { className: "card counter" }, [
    el("span", { className: "counter__value", text: formatCount(value) }),
    el("span", { className: "counter__label", text: label }),
    el("span", { className: "counter__note", text: note }),
  ]);
}

// runtimeCard lists the backends of the node.
function runtimeCard(capabilities, health) {
  const runtimes = Array.isArray(capabilities.runtimes) ? capabilities.runtimes : [];
  const list = el("ul", { className: "runtime-list" });

  if (!runtimes.length) {
    list.append(el("li", { className: "cell--muted", text: t("overview.noRuntimes") }));
  }

  for (const runtime of runtimes) {
    const healthy = runtime.healthy !== false;
    const message = runtime.message || runtime.error || "";
    list.append(
      el("li", {}, [
        stateBadge(healthy ? "ready" : "failed"),
        el("span", { text: runtime.name || t("common.unknown") }),
        message ? el("span", { className: "cell--muted", text: `— ${message}` }) : null,
        el("span", { className: "runtime-list__kind", text: runtime.kind || "" }),
      ]),
    );
  }

  const limits = capabilities.limits || {};
  const network = capabilities.network || {};

  return el("section", { className: "card", attrs: { "aria-labelledby": "overview-runtimes" } }, [
    el("div", { className: "card__header" }, [
      el("div", {}, [
        el("h2", { className: "card__title", text: t("overview.runtimes"), attrs: { id: "overview-runtimes" } }),
        el("p", { className: "card__hint", text: t("overview.runtimesHint") }),
      ]),
    ]),
    list,
    el("dl", { className: "definition" }, [
      el("dt", { text: t("overview.daemon") }),
      el("dd", {}, [stateBadge(health?.status === "ok" ? "ready" : "failed")]),
    ]),
    el("h3", { className: "card__title", text: t("overview.limits") }),
    el("dl", { className: "definition" }, [
      el("dt", { text: t("overview.maxConcurrentJobs") }),
      el("dd", { text: formatCount(limits.max_concurrent_jobs ?? 0) }),
      el("dt", { text: t("overview.maxInstances") }),
      el("dd", { text: formatCount(limits.max_instances ?? 0) }),
      el("dt", { text: t("overview.maxInstancesPerUser") }),
      el("dd", { text: formatCount(limits.max_instances_per_user ?? 0) }),
      el("dt", { text: t("overview.defaultTtl") }),
      el("dd", { text: formatSeconds(limits.default_ttl_seconds ?? 0) }),
      el("dt", { text: t("overview.maxTtl") }),
      el("dd", { text: formatSeconds(limits.max_ttl_seconds ?? 0) }),
      el("dt", { text: t("overview.defaultEgress") }),
      el("dd", { text: network.default_egress || t("common.none") }),
    ]),
  ]);
}

// resourcesCard shows the capacity of the host.
function resourcesCard(capabilities) {
  const host = capabilities.host || null;
  const body = el("div", {});

  if (!host) {
    body.append(el("p", { className: "cell--muted", text: t("overview.noHostData") }));
  } else {
    const cpuTotal = Number(host.cpu_total) || 0;
    const cpuAvailable = Number(host.cpu_available) || 0;
    const memoryTotal = Number(host.memory_total_mb) || 0;
    const memoryAvailable = Number(host.memory_available_mb) || 0;
    const diskTotal = Number(host.disk_total_mb) || 0;
    const diskAvailable = Number(host.disk_available_mb) || 0;

    body.append(
      meter(t("overview.cpu"), Math.max(0, cpuTotal - cpuAvailable), cpuTotal),
      meter(t("overview.memory"), Math.max(0, memoryTotal - memoryAvailable), memoryTotal),
      meter(t("overview.disk"), Math.max(0, diskTotal - diskAvailable), diskTotal),
    );

    const gpus = Array.isArray(host.gpus) ? host.gpus : [];
    body.append(el("h3", { className: "card__title", text: t("overview.gpus") }));
    if (!gpus.length) {
      body.append(el("p", { className: "cell--muted", text: t("overview.noGpus") }));
    } else {
      const list = el("ul", { className: "runtime-list" });
      for (const gpu of gpus) {
        list.append(
          el("li", {}, [
            el("span", { text: gpu.name || gpu.model || t("common.unknown") }),
            el("span", { className: "runtime-list__kind", text: gpu.memory_mb ? formatBytes(gpu.memory_mb) : "" }),
          ]),
        );
      }
      body.append(list);
    }
  }

  return el("section", { className: "card", attrs: { "aria-labelledby": "overview-resources" } }, [
    el("div", { className: "card__header" }, [
      el("div", {}, [
        el("h2", { className: "card__title", text: t("overview.resources"), attrs: { id: "overview-resources" } }),
        el("p", { className: "card__hint", text: t("overview.resourcesHint") }),
      ]),
    ]),
    body,
  ]);
}

// jobsCard lists the last jobs of the node.
function jobsCard(jobs) {
  const recent = [...jobs]
    .sort((a, b) => new Date(b.created_at || 0).getTime() - new Date(a.created_at || 0).getTime())
    .slice(0, 8);

  const body = el("div", { className: "card" });
  body.append(
    el("div", { className: "card__header" }, [
      el("div", {}, [
        el("h2", { className: "card__title", text: t("overview.recentJobs"), attrs: { id: "overview-recent-jobs-title" } }),
        el("p", { className: "card__hint", text: t("overview.recentJobsHint") }),
      ]),
    ]),
  );

  if (!recent.length) {
    body.append(emptyState({ icon: "▤", title: t("overview.noJobs") }));
  } else {
    const rows = recent.map((job) =>
      el("tr", {}, [
        el("td", { className: "cell--id mono", text: job.id || "" }),
        el("td", { text: job.kind || t("common.unknown") }),
        el("td", {}, [stateBadge(job.state)]),
        el("td", { className: "cell--muted", text: formatDateTime(job.created_at) }),
        el("td", { className: "cell--num cell--muted", text: jobDurationText(job) }),
      ]),
    );
    const table = el("table", { attrs: { "aria-labelledby": "overview-recent-jobs-title" } }, [
      el("thead", {}, [
        el("tr", {}, [
          el("th", { text: t("common.id") }),
          el("th", { text: t("common.kind") }),
          el("th", { text: t("common.state") }),
          el("th", { text: t("common.created") }),
          el("th", { text: t("common.duration") }),
        ]),
      ]),
      el("tbody", {}, rows),
    ]);
    body.append(el("div", { className: "table-wrap" }, [table]));
  }

  return body;
}
