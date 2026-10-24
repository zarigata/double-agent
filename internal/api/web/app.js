// app.js is the entry point of the control-plane interface.
//
// The application is a small single page application: a hash router mounts
// one view at a time, the session lives in an HttpOnly cookie and the
// CSRF token of the session is kept in memory.

import { api, onUnauthorized, readInjectedToken, setCsrfToken } from "./api.js";
import { onLocaleChange, startI18n, t } from "./i18n.js";
import { el, reportError, skeletonBlock, toast } from "./ui.js";
import { views } from "./views/index.js";

const ROUTES = ["overview", "instances", "jobs", "artifacts", "templates", "admin"];
const DEFAULT_ROUTE = "overview";

const state = {
  user: null,
  scopes: [],
  capabilities: null,
  cleanup: null,
  route: { name: DEFAULT_ROUTE, query: new URLSearchParams() },
};

const viewRoot = document.querySelector("#view-root");
const main = document.querySelector("#main");
const sidebar = document.querySelector("#sidebar");
const navAdmin = document.querySelector("#nav-admin");
const userMenu = document.querySelector("#user-menu");
const userMenuButton = document.querySelector("#user-menu-button");
const userMenuPanel = document.querySelector("#user-menu-panel");
const userMenuName = document.querySelector("#user-menu-name");
const userMenuUser = document.querySelector("#user-menu-user");
const userMenuRole = document.querySelector("#user-menu-role");
const signOutButton = document.querySelector("#sign-out");
const nodeChip = document.querySelector("#node-chip");
const nodeChipText = document.querySelector("#node-chip-text");

// context is the service the views use to reach the application.
const context = {
  get session() {
    return { user: state.user, scopes: state.scopes };
  },
  get isAdmin() {
    return isAdmin();
  },
  get route() {
    return state.route;
  },
  navigate,
  reload: () => render(),
  publishCapabilities,
  signIn,
};

// ------------------------------------------------------------------ routing

// parseRoute reads the hash of the address.
//
// A route may carry a query, for example the template that the form of a
// new instance is prefilled with.
function parseRoute() {
  const hash = window.location.hash.replace(/^#\/?/, "");
  const [path, query] = hash.split("?");
  const name = path.split("/")[0];
  return {
    name: ROUTES.includes(name) ? name : DEFAULT_ROUTE,
    query: new URLSearchParams(query || ""),
  };
}

function navigate(target) {
  if (window.location.hash === target) {
    render();
    return;
  }
  window.location.hash = target;
}

function isAdmin() {
  if (String(state.user?.role || "").toLowerCase() === "admin") return true;
  return (state.scopes || []).includes("admin");
}

// ----------------------------------------------------------------- session

async function signIn(username, password) {
  const answer = await api.login(username, password);
  state.user = answer.user || null;
  state.scopes = answer.scopes || [];
  setCsrfToken(answer.csrfToken);
  updateUserMenu();
  navigate("#/overview");
}

async function signOut() {
  try {
    await api.logout();
  } catch {
    // The cookie is cleared by the browser when the session is gone; the
    // local state is reset in any case.
  }
  state.user = null;
  state.scopes = [];
  state.capabilities = null;
  setCsrfToken("");
  closeUserMenu();
  updateUserMenu();
  navigate("#/overview");
}

async function restoreSession() {
  try {
    const answer = await api.me();
    state.user = answer.user || null;
    state.scopes = answer.scopes || [];
    return Boolean(state.user);
  } catch (error) {
    if (error?.status !== 401) reportError(error, t("app.sessionTitle"));
    state.user = null;
    return false;
  }
}

// ----------------------------------------------------------------- chrome

function updateUserMenu() {
  const signedIn = Boolean(state.user);
  userMenu.hidden = !signedIn;
  sidebar.hidden = !signedIn;

  navAdmin.hidden = !signedIn || !isAdmin();

  if (!signedIn) {
    userMenuName.textContent = "";
    return;
  }

  userMenuName.textContent = state.user.username || t("common.unknown");
  userMenuUser.textContent = t("app.signedInAs", { username: state.user.username || "" });
  userMenuRole.textContent = t("app.role", { role: roleLabel(state.user.role) });
}

function roleLabel(role) {
  const value = String(role || "").toLowerCase();
  if (value === "admin") return t("app.roleAdmin");
  if (value === "user") return t("app.roleUser");
  return role || t("common.unknown");
}

// publishCapabilities remembers what the node can do.
//
// The header shows whether the node is healthy without asking the server
// again on every view.
function publishCapabilities(capabilities, health) {
  state.capabilities = capabilities || null;
  if (!nodeChip || !nodeChipText) return;

  const runtimes = Array.isArray(capabilities?.runtimes) ? capabilities.runtimes : [];
  const alive = !health || health.status === "ok";
  if (!runtimes.length) {
    nodeChip.hidden = false;
    nodeChip.className = `node-chip node-chip--${alive ? "ok" : "down"}`;
    nodeChipText.textContent = alive ? t("app.nodeUnknown") : t("app.nodeDegraded");
    return;
  }

  const healthy = alive && runtimes.every((runtime) => runtime.healthy !== false);
  nodeChip.hidden = false;
  nodeChip.className = `node-chip node-chip--${healthy ? "ok" : "down"}`;
  nodeChipText.textContent = healthy ? t("app.nodeReady") : t("app.nodeDegraded");
}

function openUserMenu() {
  userMenuPanel.hidden = false;
  userMenuButton.setAttribute("aria-expanded", "true");
}

function closeUserMenu() {
  userMenuPanel.hidden = true;
  userMenuButton.setAttribute("aria-expanded", "false");
}

// ---------------------------------------------------------------- rendering

function cleanupView() {
  if (typeof state.cleanup === "function") {
    try {
      state.cleanup();
    } catch (error) {
      console.error("the cleanup of the view failed", error);
    }
  }
  state.cleanup = null;
}

function showSkeleton() {
  viewRoot.replaceChildren(
    el("div", { className: "view" }, [
      skeletonBlock("2rem", "14rem"),
      skeletonBlock("1rem", "22rem"),
      el("div", { className: "grid grid--3" }, [skeletonBlock("6rem"), skeletonBlock("6rem"), skeletonBlock("6rem")]),
      skeletonBlock("12rem"),
    ]),
  );
}

function markActiveRoute(name) {
  for (const link of document.querySelectorAll("[data-nav]")) {
    if (link.dataset.nav === name) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  }
}

async function render() {
  cleanupView();
  state.route = parseRoute();
  markActiveRoute(state.route.name);

  if (!state.user) {
    showSkeleton();
    await mountView("login");
    return;
  }

  if (state.route.name === "admin" && !isAdmin()) {
    toast({ title: t("app.sessionTitle"), message: t("errors.permission_denied"), type: "error" });
    navigate("#/overview");
    return;
  }

  showSkeleton();
  await mountView(state.route.name);
}

async function mountView(name) {
  const view = views[name] || views[DEFAULT_ROUTE];
  main.setAttribute("aria-busy", "true");
  try {
    viewRoot.replaceChildren();
    const cleanup = await view.mount(viewRoot, context);
    state.cleanup = typeof cleanup === "function" ? cleanup : null;
  } catch (error) {
    viewRoot.replaceChildren(
      el("div", { className: "view" }, [
        el("section", { className: "card" }, [
          el("h1", { className: "card__title", text: t("errors.title") }),
          el("p", { className: "view__subtitle", text: error?.message || t("errors.unknown") }),
          error?.correlationId
            ? el("p", { className: "field__hint", text: t("errors.correlation", { id: error.correlationId }) })
            : null,
        ]),
      ]),
    );
    reportError(error);
  } finally {
    main.removeAttribute("aria-busy");
  }
}

// -------------------------------------------------------------------- boot

async function start() {
  await startI18n();
  setCsrfToken(readInjectedToken());

  onUnauthorized(() => {
    if (!state.user) return;
    state.user = null;
    state.scopes = [];
    updateUserMenu();
    toast({ title: t("app.sessionTitle"), message: t("errors.authentication_failed"), type: "error" });
    render();
  });

  onLocaleChange(() => {
    updateUserMenu();
    if (state.user) render();
  });

  userMenuButton.addEventListener("click", () => {
    if (userMenuPanel.hidden) openUserMenu();
    else closeUserMenu();
  });

  document.addEventListener("click", (event) => {
    if (userMenuPanel.hidden) return;
    if (!userMenu.contains(event.target)) closeUserMenu();
  });

  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && !userMenuPanel.hidden) {
      closeUserMenu();
      userMenuButton.focus();
    }
  });

  signOutButton.addEventListener("click", () => {
    closeUserMenu();
    signOut();
  });

  window.addEventListener("hashchange", () => render());

  const authenticated = await restoreSession();
  updateUserMenu();
  if (!window.location.hash) {
    // The route is written without a hashchange, so the first view is
    // rendered exactly once.
    history.replaceState(null, "", "#/overview");
  }
  await render();
}

start().catch((error) => {
  console.error("the interface could not start", error);
});
