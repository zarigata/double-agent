// views/admin.js is the administration section.
//
// The section is only reachable by an administrator; the server refuses the
// calls of everybody else. The secret of a new credential is shown once,
// because the node keeps nothing but its hash.

import { api } from "../api.js";
import { t } from "../i18n.js";
import {
  confirmDialog,
  el,
  emptyState,
  formatDateTime,
  reportError,
  stateBadge,
  toast,
} from "../ui.js";

export async function mount(container, context) {
  const [users, keys, events] = await Promise.all([api.users(), api.keys(), api.audit()]);

  const view = el("div", { className: "view" });
  const refreshButton = el("button", {
    className: "button button--ghost",
    text: t("app.refresh"),
    attrs: { type: "button" },
  });

  view.append(
    el("header", { className: "view__header" }, [
      el("div", {}, [
        el("p", { className: "eyebrow", text: t("admin.eyebrow") }),
        el("h1", { text: t("admin.title") }),
        el("p", { className: "view__subtitle", text: t("admin.subtitle") }),
      ]),
      el("div", { className: "view__actions" }, [refreshButton]),
    ]),
  );

  view.append(usersCard(users, context), keysCard(keys, context), auditCard(events));
  container.append(view);

  refreshButton.addEventListener("click", () => context.reload());
}

// ------------------------------------------------------------------ users

function usersCard(users, context) {
  const card = el("section", { className: "card", attrs: { "aria-labelledby": "admin-users" } });
  card.append(
    el("div", { className: "card__header" }, [
      el("div", {}, [
        el("h2", { className: "card__title", text: t("admin.users"), attrs: { id: "admin-users" } }),
        el("p", { className: "card__hint", text: t("admin.usersHint") }),
      ]),
    ]),
  );

  if (!users.length) {
    card.append(emptyState({ icon: "◇", title: t("admin.noUsers") }));
  } else {
    const rows = users.map((user) => {
      const disableButton = el("button", {
        className: "button button--danger button--small",
        text: t("admin.disable"),
        attrs: { type: "button", "aria-label": t("admin.disableFor", { name: user.username }) },
      });
      disableButton.disabled = Boolean(user.disabled);
      disableButton.addEventListener("click", () => disableUser(user, context));

      return el("tr", {}, [
        el("td", { text: user.username || t("common.unknown") }),
        el("td", { className: "cell--muted", text: roleLabel(user.role) }),
        el("td", {}, [stateBadge(user.disabled ? "disabled" : "active")]),
        el("td", { className: "cell--muted", text: formatDateTime(user.created_at) }),
        el("td", { className: "cell--actions" }, [disableButton]),
      ]);
    });

    card.append(
      el("div", { className: "table-wrap" }, [
        el("table", { attrs: { "aria-labelledby": "admin-users" } }, [
          el("thead", {}, [
            el("tr", {}, [
              el("th", { text: t("common.name") }),
              el("th", { text: t("admin.role") }),
              el("th", { text: t("common.status") }),
              el("th", { text: t("common.created") }),
              el("th", { className: "cell--actions", text: t("common.actions") }),
            ]),
          ]),
          el("tbody", {}, rows),
        ]),
      ]),
    );
  }

  card.append(userForm(context));
  return card;
}

function userForm(context) {
  const username = el("input", {
    className: "input",
    attrs: { type: "text", id: "admin-username", name: "username", autocomplete: "off", required: true },
  });
  const password = el("input", {
    className: "input",
    attrs: { type: "password", id: "admin-password", name: "password", autocomplete: "new-password", required: true },
  });
  const role = el("select", { className: "input", attrs: { id: "admin-role", name: "role" } }, [
    el("option", { text: t("admin.roleUser"), attrs: { value: "user" } }),
    el("option", { text: t("admin.roleAdmin"), attrs: { value: "admin" } }),
  ]);
  const alert = el("p", { className: "form-alert", attrs: { role: "alert", hidden: "hidden" } });
  const submit = el("button", { className: "button button--primary", text: t("admin.createUser"), attrs: { type: "submit" } });

  const form = el("form", { className: "stack", attrs: { novalidate: true } }, [
    el("h3", { className: "card__title", text: t("admin.createUser") }),
    alert,
    el("div", { className: "form-grid" }, [
      el("div", { className: "field" }, [
        el("label", { text: t("admin.username"), attrs: { for: "admin-username" } }),
        username,
      ]),
      el("div", { className: "field" }, [
        el("label", { text: t("admin.password"), attrs: { for: "admin-password" } }),
        password,
        el("p", { className: "field__hint", text: t("admin.passwordHint") }),
      ]),
      el("div", { className: "field" }, [
        el("label", { text: t("admin.role"), attrs: { for: "admin-role" } }),
        role,
      ]),
    ]),
    el("div", { className: "form-actions" }, [submit]),
  ]);

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    alert.hidden = true;
    if (!username.value.trim() || !password.value) {
      alert.textContent = t("admin.missingFields");
      alert.hidden = false;
      return;
    }
    submit.disabled = true;
    try {
      await api.createUser({
        username: username.value.trim(),
        password: password.value,
        role: role.value,
      });
      toast({ title: t("admin.createUser"), message: t("admin.userCreated", { name: username.value.trim() }), type: "success" });
      username.value = "";
      password.value = "";
      context.reload();
    } catch (error) {
      alert.textContent = error?.message || t("errors.unknown");
      alert.hidden = false;
      reportError(error, t("admin.createUser"));
    } finally {
      submit.disabled = false;
    }
  });

  return form;
}

// ------------------------------------------------------------------- keys

function keysCard(keys, context) {
  const card = el("section", { className: "card", attrs: { "aria-labelledby": "admin-keys" } });
  card.append(
    el("div", { className: "card__header" }, [
      el("div", {}, [
        el("h2", { className: "card__title", text: t("admin.keys"), attrs: { id: "admin-keys" } }),
        el("p", { className: "card__hint", text: t("admin.keysHint") }),
      ]),
    ]),
  );

  const secretHost = el("div", {});

  if (!keys.length) {
    card.append(emptyState({ icon: "⚿", title: t("admin.noKeys") }));
  } else {
    const rows = keys.map((key) => {
      const revoked = Boolean(key.revoked_at);
      const revokeButton = el("button", {
        className: "button button--danger button--small",
        text: t("admin.revoke"),
        attrs: { type: "button", "aria-label": t("admin.revokeFor", { name: key.name }) },
      });
      revokeButton.disabled = revoked;
      revokeButton.addEventListener("click", () => revokeKey(key, context));

      return el("tr", {}, [
        el("td", { text: key.name || t("common.unknown") }),
        el("td", { className: "cell--id mono", text: key.prefix || "" }),
        el("td", { className: "cell--muted", text: (key.scopes || []).join(", ") || t("common.none") }),
        el("td", {}, [stateBadge(revoked ? "revoked" : "active")]),
        el("td", { className: "cell--muted", text: formatDateTime(key.created_at) }),
        el("td", { className: "cell--muted", text: key.expires_at ? formatDateTime(key.expires_at) : t("common.never") }),
        el("td", { className: "cell--muted", text: key.last_used_at ? formatDateTime(key.last_used_at) : t("common.never") }),
        el("td", { className: "cell--actions" }, [revokeButton]),
      ]);
    });

    card.append(
      el("div", { className: "table-wrap" }, [
        el("table", { attrs: { "aria-labelledby": "admin-keys" } }, [
          el("thead", {}, [
            el("tr", {}, [
              el("th", { text: t("common.name") }),
              el("th", { text: t("admin.keyPrefix") }),
              el("th", { text: t("admin.keyScopes") }),
              el("th", { text: t("common.status") }),
              el("th", { text: t("common.created") }),
              el("th", { text: t("common.expires") }),
              el("th", { text: t("admin.lastUsed") }),
              el("th", { className: "cell--actions", text: t("common.actions") }),
            ]),
          ]),
          el("tbody", {}, rows),
        ]),
      ]),
    );
  }

  const form = keyForm(context, secretHost);
  card.append(form, secretHost);
  return card;
}

function keyForm(context, secretHost) {
  const name = el("input", {
    className: "input",
    attrs: { type: "text", id: "admin-key-name", name: "name", autocomplete: "off" },
  });
  const scopes = el("input", {
    className: "input",
    attrs: { type: "text", id: "admin-key-scopes", name: "scopes", autocomplete: "off" },
  });
  const alert = el("p", { className: "form-alert", attrs: { role: "alert", hidden: "hidden" } });
  const submit = el("button", { className: "button button--primary", text: t("admin.createKey"), attrs: { type: "submit" } });

  const form = el("form", { className: "stack", attrs: { novalidate: true } }, [
    el("h3", { className: "card__title", text: t("admin.createKey") }),
    alert,
    el("div", { className: "form-grid" }, [
      el("div", { className: "field" }, [
        el("label", { text: t("admin.keyName"), attrs: { for: "admin-key-name" } }),
        name,
      ]),
      el("div", { className: "field" }, [
        el("label", { text: t("admin.keyScopes"), attrs: { for: "admin-key-scopes" } }),
        scopes,
        el("p", { className: "field__hint", text: t("admin.keyScopesHint") }),
      ]),
    ]),
    el("div", { className: "form-actions" }, [submit]),
  ]);

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    alert.hidden = true;
    submit.disabled = true;
    try {
      const answer = await api.createKey({
        name: name.value.trim(),
        scopes: scopes.value.split(",").map((value) => value.trim()).filter(Boolean),
      });
      name.value = "";
      scopes.value = "";
      renderSecret(secretHost, answer.secret);
      context.reload();
    } catch (error) {
      alert.textContent = error?.message || t("errors.unknown");
      alert.hidden = false;
      reportError(error, t("admin.createKey"));
    } finally {
      submit.disabled = false;
    }
  });

  return form;
}

// renderSecret shows the secret of a new credential.
//
// The secret is not kept anywhere: the node stores its hash, so the operator
// has to copy the value while it is on the screen.
function renderSecret(host, secret) {
  host.replaceChildren();
  if (!secret) return;

  const value = el("code", { className: "secret__value", text: secret });
  const copyButton = el("button", {
    className: "button button--ghost button--small",
    text: t("common.copy"),
    attrs: { type: "button" },
  });
  copyButton.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(secret);
      copyButton.textContent = t("common.copied");
    } catch {
      copyButton.textContent = t("common.copyFailed");
    }
  });

  host.append(
    el("div", { className: "secret", attrs: { role: "group", "aria-label": t("admin.keySecret") } }, [
      el("p", { className: "card__title", text: t("admin.keySecret") }),
      el("p", { className: "secret__warning", text: t("admin.keySecretWarning") }),
      value,
      el("div", { className: "secret__actions" }, [copyButton]),
    ]),
  );
  copyButton.focus();
}

// ------------------------------------------------------------------ audit

function auditCard(events) {
  const card = el("section", { className: "card", attrs: { "aria-labelledby": "admin-audit" } });
  card.append(
    el("div", { className: "card__header" }, [
      el("div", {}, [
        el("h2", { className: "card__title", text: t("admin.audit"), attrs: { id: "admin-audit" } }),
        el("p", { className: "card__hint", text: t("admin.auditHint") }),
      ]),
    ]),
  );

  if (!events.length) {
    card.append(emptyState({ icon: "▤", title: t("admin.auditEmpty") }));
    return card;
  }

  const rows = events.map((event) => {
    const allowed = String(event.decision || "").toLowerCase() === "allow";
    return el("tr", {}, [
      el("td", { className: "cell--muted", text: formatDateTime(event.ts || event.timestamp) }),
      el("td", { className: "cell--muted", text: event.actor_type || t("common.unknown") }),
      el("td", { text: event.action || t("common.unknown") }),
      el("td", { className: "cell--muted", text: event.target_id || t("common.none") }),
      el("td", {}, [
        el("span", {
          className: `audit-decision audit-decision--${allowed ? "allow" : "deny"}`,
          text: allowed ? t("admin.allowed") : t("admin.denied"),
        }),
      ]),
      el("td", { className: "cell--muted", text: event.result || t("common.none") }),
      el("td", { className: "cell--id mono cell--muted", text: event.correlation_id || "" }),
    ]);
  });

  card.append(
    el("div", { className: "table-wrap" }, [
      el("table", { attrs: { "aria-labelledby": "admin-audit" } }, [
        el("thead", {}, [
          el("tr", {}, [
            el("th", { text: t("common.created") }),
            el("th", { text: t("admin.actor") }),
            el("th", { text: t("admin.action") }),
            el("th", { text: t("admin.target") }),
            el("th", { text: t("admin.decision") }),
            el("th", { text: t("common.result") }),
            el("th", { text: t("admin.correlation") }),
          ]),
        ]),
        el("tbody", {}, rows),
      ]),
    ]),
  );

  return card;
}

function roleLabel(role) {
  const value = String(role || "").toLowerCase();
  if (value === "admin") return t("admin.roleAdmin");
  if (value === "user") return t("admin.roleUser");
  return role || t("common.unknown");
}
