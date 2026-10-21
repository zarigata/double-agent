import { access, readdir, readFile } from "node:fs/promises";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(fileURLToPath(new URL("..", import.meta.url)));

async function collectMarkdown(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    if (entry.name === ".git" || entry.name === "node_modules") continue;
    const path = join(directory, entry.name);
    if (entry.isDirectory()) files.push(...await collectMarkdown(path));
    else if (entry.name.endsWith(".md")) files.push(path);
  }
  return files;
}

const missing = [];
const files = await collectMarkdown(root);

for (const file of files) {
  const source = await readFile(file, "utf8");
  for (const match of source.matchAll(/!?(?:\[[^\]]*\])\(([^)]+)\)/g)) {
    const target = match[1].trim().replace(/^<|>$/g, "").split("#")[0];
    if (!target || /^(?:https?:|mailto:|data:)/.test(target)) continue;
    const decoded = decodeURIComponent(target);
    try {
      await access(resolve(dirname(file), decoded));
    } catch {
      missing.push(`${relative(root, file)} -> ${target}`);
    }
  }
}

if (missing.length) {
  console.error("Broken local Markdown links:\n" + missing.join("\n"));
  process.exit(1);
}

console.log(`Documentation validation passed: ${files.length} Markdown files`);
