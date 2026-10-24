// views/login.js asks for the credentials of a local account.
//
// The password is sent once over the session cookie; the browser never
// keeps it and the server answers with an HttpOnly cookie.

import { el, reportError } from "../ui.js";
import { t } from "../i18n.js";

export function mount(container, context) {
  const alert = el("p", { className: "form-alert", attrs: { role: "alert", hidden: "hidden" } });
  const username = el("input", {
    className: "input",
    attrs: { type: "text", id: "login-username", name: "username", autocomplete: "username", required: true, spellcheck: "false" },
  });
  const password = el("input", {
    className: "input",
    attrs: { type: "password", id: "login-password", name: "password", autocomplete: "current-password", required: true },
  });
  const submit = el("button", { className: "button button--primary button--block", text: t("login.submit"), attrs: { type: "submit" } });

  const form = el("form", { className: "stack", attrs: { novalidate: true } }, [
    alert,
    el("div", { className: "field" }, [
      el("label", { text: t("login.username"), attrs: { for: "login-username" } }),
      username,
    ]),
    el("div", { className: "field" }, [
      el("label", { text: t("login.password"), attrs: { for: "login-password" } }),
      password,
    ]),
    submit,
  ]);

  const card = el("section", { className: "card login__card", attrs: { "aria-labelledby": "login-title" } }, [
    el("p", { className: "login__brand" }, [
      el("span", { text: "◈", attrs: { "aria-hidden": "true" } }),
      el("span", { className: "eyebrow", text: t("app.brand") }),
    ]),
    el("h1", { className: "login__title", text: t("login.title"), attrs: { id: "login-title" } }),
    el("p", { className: "login__subtitle", text: t("login.subtitle") }),
    form,
    el("p", { className: "login__hint", text: t("login.hint") }),
  ]);

  container.append(el("div", { className: "login" }, [card]));

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    alert.hidden = true;
    alert.textContent = "";

    const name = username.value.trim();
    if (!name || !password.value) {
      alert.textContent = t("login.missing");
      alert.hidden = false;
      return;
    }

    submit.disabled = true;
    submit.textContent = t("login.submitting");
    try {
      await context.signIn(name, password.value);
    } catch (error) {
      alert.textContent = error?.message || t("errors.unknown");
      alert.hidden = false;
      reportError(error, t("login.title"));
      password.focus();
    } finally {
      submit.disabled = false;
      submit.textContent = t("login.submit");
    }
  });

  username.focus();
}
