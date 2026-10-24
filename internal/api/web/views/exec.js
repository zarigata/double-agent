// views/exec.js runs a command inside an instance.
//
// The command is an argument vector: every argument is its own field, so a
// workload can never smuggle a second command into the host. The server
// refuses a shell string for the same reason.

import { api } from "../api.js";
import { t } from "../i18n.js";
import { el, openModal, reportError } from "../ui.js";

// openExecModal shows the dialog of the exec action.
export function openExecModal(options = {}) {
  const { instance, onDone } = options;
  const argv = [];
  const environment = [];

  const argvList = el("div", { className: "stack" });
  const envList = el("div", { className: "stack" });
  const result = el("div", { className: "stack" });

  const cwd = el("input", {
    className: "input",
    attrs: { type: "text", id: "exec-cwd", name: "cwd", spellcheck: "false", autocomplete: "off" },
  });
  const timeout = el("input", {
    className: "input",
    attrs: { type: "number", id: "exec-timeout", name: "timeout", min: "1", step: "1", value: "60" },
  });

  const runButton = el("button", { className: "button button--primary", text: t("exec.run"), attrs: { type: "submit" } });
  const closeButton = el("button", { className: "button button--ghost", text: t("exec.close"), attrs: { type: "button" } });
  const addArgumentButton = el("button", {
    className: "button button--ghost button--small",
    text: t("exec.addArgument"),
    attrs: { type: "button" },
  });
  const addVariableButton = el("button", {
    className: "button button--ghost button--small",
    text: t("exec.addVariable"),
    attrs: { type: "button" },
  });

  const form = el("form", { className: "stack", attrs: { novalidate: true } }, [
    el("div", { className: "field" }, [
      el("label", { text: t("exec.command"), attrs: { for: "exec-argv-1" } }),
      el("p", { className: "field__hint", text: t("exec.commandHint") }),
      argvList,
      el("div", { className: "form-actions" }, [addArgumentButton]),
    ]),
    el("div", { className: "form-grid" }, [
      el("div", { className: "field" }, [
        el("label", { text: t("exec.cwd"), attrs: { for: "exec-cwd" } }),
        cwd,
        el("p", { className: "field__hint", text: t("exec.cwdHint") }),
      ]),
      el("div", { className: "field" }, [
        el("label", { text: t("exec.timeout"), attrs: { for: "exec-timeout" } }),
        timeout,
        el("p", { className: "field__hint", text: t("exec.timeoutHint") }),
      ]),
    ]),
    el("div", { className: "field" }, [
      el("label", { text: t("exec.env") }),
      envList,
      el("div", { className: "form-actions" }, [addVariableButton]),
    ]),
  ]);

  const modal = openModal({
    title: t("exec.title"),
    subtitle: t("exec.subtitle", { instance: instance?.id || "" }),
    body: [form, result],
    footer: [closeButton, runButton],
  });

  closeButton.addEventListener("click", () => modal.close());

  function addArgument(value = "") {
    const index = argv.length + 1;
    const input = el("input", {
      className: "input",
      attrs: { type: "text", id: `exec-argv-${index}`, spellcheck: "false", autocomplete: "off" },
    });
    input.value = value;
    const remove = el("button", {
      className: "button button--ghost button--small button--icon",
      text: "×",
      attrs: { type: "button", "aria-label": t("exec.removeArgument", { index }) },
    });
    const row = el("div", { className: "argv-row" }, [
      el("div", { className: "field" }, [
        el("label", { className: "argv-row__index", text: t("exec.argumentLabel", { index }), attrs: { for: `exec-argv-${index}` } }),
        input,
      ]),
      remove,
    ]);
    remove.addEventListener("click", () => {
      row.remove();
      const position = argv.indexOf(entry);
      if (position >= 0) argv.splice(position, 1);
    });
    const entry = { input };
    argv.push(entry);
    argvList.append(row);
    return input;
  }

  function addVariable(key = "", value = "") {
    const index = environment.length + 1;
    const keyInput = el("input", {
      className: "input",
      attrs: { type: "text", id: `exec-env-key-${index}`, spellcheck: "false", autocomplete: "off" },
    });
    keyInput.value = key;
    const valueInput = el("input", {
      className: "input",
      attrs: { type: "text", id: `exec-env-value-${index}`, spellcheck: "false", autocomplete: "off" },
    });
    valueInput.value = value;
    const remove = el("button", {
      className: "button button--ghost button--small button--icon",
      text: "×",
      attrs: { type: "button", "aria-label": t("exec.removeVariable", { index }) },
    });
    const row = el("div", { className: "env-row" }, [
      el("div", { className: "field" }, [
        el("label", { className: "argv-row__index", text: t("exec.variableLabel", { index }), attrs: { for: `exec-env-key-${index}` } }),
        keyInput,
      ]),
      el("div", { className: "field" }, [
        el("label", { className: "argv-row__index", text: t("exec.valueLabel"), attrs: { for: `exec-env-value-${index}` } }),
        valueInput,
      ]),
      remove,
    ]);
    remove.addEventListener("click", () => {
      row.remove();
      const position = environment.indexOf(entry);
      if (position >= 0) environment.splice(position, 1);
    });
    const entry = { keyInput, valueInput };
    environment.push(entry);
    envList.append(row);
  }

  addArgumentButton.addEventListener("click", () => addArgument());
  addVariableButton.addEventListener("click", () => addVariable());

  addArgument("/bin/sh");
  addArgument("-c");
  addArgument("echo double-agent");

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const values = argv.map((entry) => entry.input.value).filter((value) => value !== "");
    if (!values.length) {
      result.replaceChildren(
        el("p", { className: "form-alert", text: t("exec.noArguments"), attrs: { role: "alert" } }),
      );
      return;
    }

    const env = {};
    for (const entry of environment) {
      const key = entry.keyInput.value.trim();
      if (key) env[key] = entry.valueInput.value;
    }

    runButton.disabled = true;
    runButton.textContent = t("exec.running");
    try {
      const answer = await api.exec(instance.id, {
        argv: values,
        cwd: cwd.value.trim(),
        env,
        timeoutSeconds: Number(timeout.value) || undefined,
      });
      renderResult(result, answer);
      if (typeof onDone === "function") onDone(answer);
    } catch (error) {
      result.replaceChildren(
        el("p", { className: "form-alert", text: error?.message || t("errors.unknown"), attrs: { role: "alert" } }),
      );
      reportError(error, t("exec.title"));
    } finally {
      runButton.disabled = false;
      runButton.textContent = t("exec.run");
    }
  });

  return modal;
}

// renderResult shows the outcome of a command.
function renderResult(host, answer) {
  const exitCode = answer?.exit_code ?? 0;
  const stdout = typeof answer?.stdout === "string" ? answer.stdout : "";
  const stderr = typeof answer?.stderr === "string" ? answer.stderr : "";

  const meta = el("dl", { className: "console__meta" }, [
    el("div", {}, [
      el("dt", { text: t("exec.exitCode") }),
      el("dd", { text: String(exitCode) }),
    ]),
    el("div", {}, [
      el("dt", { text: t("exec.duration") }),
      el("dd", { text: formatDuration(answer?.duration_ms) }),
    ]),
  ]);

  host.replaceChildren(
    el("h3", { className: "card__title", text: t("exec.result") }),
    meta,
    answer?.truncated ? el("p", { className: "field__hint", text: t("exec.truncated") }) : null,
    el("div", {}, [
      el("p", { className: "card__hint", text: t("exec.stdout") }),
      stdout
        ? el("pre", { className: "console", text: stdout })
        : el("pre", { className: "console console--empty", text: t("exec.emptyOutput") }),
    ]),
    el("div", {}, [
      el("p", { className: "card__hint", text: t("exec.stderr") }),
      stderr
        ? el("pre", { className: "console console--error", text: stderr })
        : el("pre", { className: "console console--empty", text: t("exec.emptyOutput") }),
    ]),
  );
}

function formatDuration(milliseconds) {
  const value = Number(milliseconds);
  if (!Number.isFinite(value)) return "—";
  if (value < 1000) return `${Math.round(value)} ms`;
  return `${(value / 1000).toFixed(2)} s`;
}
