import fs from "node:fs";
import path from "node:path";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
// Candidate identity is the sorted tracked/non-ignored file path and content
// fingerprint shared by the runner and run-mode compiler.

const DEPENDENCY_DIRECTORIES = new Set([".git", ".pnpm", "node_modules", "vendor", "bower_components", "Pods"]);
const IGNORED_OUTPUT_PREFIXES = [
  ".git/",
  ".tmp/",
  "tmp/",
  ".worktrees/",
  "web/coverage/",
  "web/dist/",
  "web/test-results/",
];
const LOCKFILE_NAMES = new Set([
  "Cargo.lock",
  "Gemfile.lock",
  "Pipfile.lock",
  "composer.lock",
  "go.sum",
  "npm-shrinkwrap.json",
  "package-lock.json",
  "pnpm-lock.yaml",
  "yarn.lock",
  "bun.lock",
  "bun.lockb",
]);
const SHA256_PATTERN = /^[0-9a-f]{64}$/;

function pathInside(parent, candidate) {
  const relative = path.relative(parent, candidate);
  return relative === "" || (relative !== ".." && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative));
}

function runGit(repositoryRoot, args) {
  try {
    return execFileSync("git", args, {
      cwd: repositoryRoot,
      encoding: args.includes("-z") ? "buffer" : "utf8",
      maxBuffer: 128 * 1024 * 1024,
      stdio: ["ignore", "pipe", "pipe"],
    });
  } catch (error) {
    const detail = Buffer.isBuffer(error?.stderr)
      ? error.stderr.toString("utf8").trim()
      : String(error?.stderr ?? error?.message ?? "git failed").trim();
    throw new Error(`git ${args.join(" ")} failed${detail ? `: ${detail}` : ""}`);
  }
}

function ignoredCandidatePath(relativePath) {
  const normalized = relativePath.replaceAll(path.sep, "/");
  if (IGNORED_OUTPUT_PREFIXES.some((prefix) => normalized === prefix.slice(0, -1) || normalized.startsWith(prefix))) return true;
  return normalized.split("/").some((part) => DEPENDENCY_DIRECTORIES.has(part));
}

function digestBytes(bytes) {
  return createHash("sha256").update(bytes).digest("hex");
}

function hashCandidateFile(absolutePath, repositoryRoot) {
  const realPath = fs.realpathSync(absolutePath);
  if (!pathInside(repositoryRoot, realPath)) throw new Error(`candidate symlink escapes the checkout: ${absolutePath}`);
  if (!fs.statSync(realPath).isFile()) throw new Error(`candidate path is not a regular file: ${absolutePath}`);
  return digestBytes(fs.readFileSync(realPath));
}

function candidateDigest(files) {
  const canonical = files.map(({ path: relativePath, sha256 }) => `${relativePath}\0${sha256}\n`).join("");
  return digestBytes(Buffer.from(canonical, "utf8"));
}

function isLockfile(relativePath) {
  return LOCKFILE_NAMES.has(path.posix.basename(relativePath));
}

export function captureCandidate(repositoryRoot) {
  const listed = runGit(repositoryRoot, ["ls-files", "--cached", "--others", "--exclude-standard", "-z"]);
  const paths = listed.toString("utf8").split("\0").filter(Boolean)
    .filter((relativePath) => !ignoredCandidatePath(relativePath))
    .sort((left, right) => (left < right ? -1 : left > right ? 1 : 0));
  const files = [];
  const seen = new Set();
  for (const relativePath of paths) {
    if (seen.has(relativePath)) continue;
    seen.add(relativePath);
    const absolutePath = path.resolve(repositoryRoot, relativePath);
    if (!pathInside(repositoryRoot, absolutePath)) throw new Error(`candidate path escapes the checkout: ${relativePath}`);
    files.push({ path: relativePath, sha256: hashCandidateFile(absolutePath, repositoryRoot) });
  }
  return { files, sha256: candidateDigest(files) };
}

export function lockfileSummary(candidate) {
  return candidate.files.filter(({ path: relativePath }) => isLockfile(relativePath));
}

export function validateCandidateSnapshot(value, label) {
  if (!value || typeof value !== "object" || !Array.isArray(value.files) || typeof value.sha256 !== "string") {
    throw new Error(`${label} is malformed`);
  }
  if (!SHA256_PATTERN.test(value.sha256)) throw new Error(`${label}.sha256 is not a SHA-256 digest`);
  let previousPath = "";
  const seen = new Set();
  for (const file of value.files) {
    if (!file || typeof file.path !== "string" || typeof file.sha256 !== "string" || !SHA256_PATTERN.test(file.sha256)) {
      throw new Error(`${label} contains an invalid file fingerprint`);
    }
    if (!file.path || file.path.startsWith("/") || file.path.includes("\\") || file.path.includes("\0") || file.path.split("/").includes("..")) {
      throw new Error(`${label} contains an unsafe path: ${file.path}`);
    }
    if (seen.has(file.path) || (previousPath && file.path <= previousPath)) throw new Error(`${label}.files must be sorted and unique`);
    seen.add(file.path);
    previousPath = file.path;
  }
  if (candidateDigest(value.files) !== value.sha256) throw new Error(`${label}.sha256 does not match its file list`);
  return value;
}
