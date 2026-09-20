// Fails the build when a Chinese UI string has no English translation.
//
// This exists because nothing else catches it. The dictionary falls back to
// showing the Chinese source string when a key is absent, by design, so a
// missing entry is not a type error, not a runtime error, and not visible at
// all unless you switch the UI to English and look at the right page. A
// rename that orphans its entry, or an edit that drops one, ships silently.
//
// Only plain-literal t("...") calls are checked. A template literal cannot be
// a dictionary key, so those are outside what this can see.
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

const root = new URL("../src", import.meta.url).pathname;
const dictionary = join(root, "lib", "i18n-en.ts");

function walk(directory) {
  const out = [];
  for (const entry of readdirSync(directory)) {
    const path = join(directory, entry);
    if (statSync(path).isDirectory()) out.push(...walk(path));
    else if (/\.tsx?$/.test(entry)) out.push(path);
  }
  return out;
}

const dictionaryText = readFileSync(dictionary, "utf8");
const keys = new Set();
for (const match of dictionaryText.matchAll(/^\s*(?:"((?:[^"\\]|\\.)*)"|([^\s":]+))\s*:/gm)) {
  keys.add((match[1] ?? match[2] ?? "").replaceAll('\\"', '"'));
}

const hasChinese = /[一-鿿]/;
const missing = new Map();
for (const path of walk(root)) {
  if (path.includes("i18n")) continue;
  for (const match of readFileSync(path, "utf8").matchAll(/\bt\(\s*"((?:[^"\\]|\\.)*)"\s*[,)]/g)) {
    const literal = match[1].replaceAll('\\"', '"');
    if (hasChinese.test(literal) && !keys.has(literal)) {
      missing.set(literal, path.slice(root.length + 1));
    }
  }
}

if (missing.size > 0) {
  console.error(`\n${missing.size} UI string(s) have no English translation:\n`);
  for (const [literal, where] of missing) console.error(`  ${where}\n    ${literal}`);
  console.error("\nAdd them to src/lib/i18n-en.ts.\n");
  process.exit(1);
}
console.log(`i18n: ${keys.size} translations, every t("...") string covered`);
