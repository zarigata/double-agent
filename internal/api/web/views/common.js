// views/common.js holds the small helpers that several views share.

import { t } from "../i18n.js";
import { formatMilliseconds } from "../ui.js";

// jobDuration returns the time a job needed.
//
// A job that is still running is measured against the current time, so the
// column keeps moving while the work continues.
export function jobDuration(job) {
  if (!job) return null;
  const started = job.started_at ? new Date(job.started_at).getTime() : NaN;
  if (Number.isNaN(started)) return null;
  const finished = job.finished_at ? new Date(job.finished_at).getTime() : Date.now();
  const delta = finished - started;
  return Number.isFinite(delta) && delta >= 0 ? delta : null;
}

export function jobDurationText(job) {
  const duration = jobDuration(job);
  return duration === null ? "—" : formatMilliseconds(duration);
}

// instanceRemaining returns the seconds an instance has left.
//
// The deadline is the absolute moment the instance expires, so the countdown
// stays correct even when the list is not refreshed.
export function instanceRemaining(instance) {
  if (!instance?.expires_at) return null;
  const deadline = new Date(instance.expires_at).getTime();
  if (Number.isNaN(deadline)) return null;
  return Math.round((deadline - Date.now()) / 1000);
}

export function instanceRemainingText(instance) {
  const remaining = instanceRemaining(instance);
  if (remaining === null) return "—";
  if (remaining <= 0) return t("instances.expired");
  return t("instances.remaining", { time: formatRemaining(remaining) });
}

function formatRemaining(seconds) {
  const absolute = Math.abs(seconds);
  if (absolute >= 86400) return `${Math.round(seconds / 86400)}d`;
  if (absolute >= 3600) return `${Math.round(seconds / 3600)}h`;
  if (absolute >= 60) return `${Math.round(seconds / 60)}m`;
  return `${Math.round(seconds)}s`;
}

// errorCode renders the stable error code of a job.
export function errorCode(value) {
  if (!value) return "—";
  const key = `errors.${value}`;
  const localized = t(key);
  return localized === key ? value : `${value} — ${localized}`;
}
