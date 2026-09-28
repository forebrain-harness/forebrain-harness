#!/usr/bin/env node
// forebrain launcher
// 1. Resolve platform-specific Go binary from @forebrain-harness/forebrain-{platform}-{arch}
// 2. Spawn binary, inherit stdio, forward exit code & signals

import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const require = createRequire(import.meta.url);

const PLATFORM_PACKAGES = {
  "darwin-arm64": {
    pkg: "@forebrain-harness/forebrain-darwin-arm64",
    triple: "aarch64-apple-darwin",
    binary: "forebrain",
  },
  "darwin-x64": {
    pkg: "@forebrain-harness/forebrain-darwin-x64",
    triple: "x86_64-apple-darwin",
    binary: "forebrain",
  },
  "linux-x64": {
    pkg: "@forebrain-harness/forebrain-linux-x64",
    triple: "x86_64-unknown-linux-gnu",
    binary: "forebrain",
  },
  "linux-arm64": {
    pkg: "@forebrain-harness/forebrain-linux-arm64",
    triple: "aarch64-unknown-linux-gnu",
    binary: "forebrain",
  },
  "win32-x64": {
    pkg: "@forebrain-harness/forebrain-win32-x64",
    triple: "x86_64-pc-windows-gnu",
    binary: "forebrain",
  },
};

function die(msg, code = 1) {
  process.stderr.write(`forebrain: ${msg}\n`);
  process.exit(code);
}

function resolvePlatformPackage() {
  const key = `${process.platform}-${process.arch}`;
  const meta = PLATFORM_PACKAGES[key];
  if (!meta) {
    die(
      `unsupported platform/arch: ${key}. Supported: ${Object.keys(PLATFORM_PACKAGES).join(", ")}`,
    );
  }
  const launcherPkgRoot = path.dirname(__dirname);
  const repoRoot = path.dirname(launcherPkgRoot);

  // 1. installed platform package
  try {
    const pkgJson = require.resolve(`${meta.pkg}/package.json`);
    return { meta, root: path.dirname(pkgJson), source: "platform-package" };
  } catch {
    /* fall through */
  }

  // 2. local build output: npm/dist/@forebrain-harness/<short>/
  const shortName = meta.pkg.split("/").pop();
  const distRoot = path.join(launcherPkgRoot, "dist", "@forebrain-harness", shortName);
  if (existsSync(path.join(distRoot, "package.json"))) {
    return { meta, root: distRoot, source: "local-dist" };
  }

  // 3. repo build/bin/forebrain (Method 1)
  const repoBinExt = process.platform === "win32" ? ".exe" : "";
  const repoBinary = path.join(repoRoot, "build", "bin", `forebrain${repoBinExt}`);
  if (existsSync(repoBinary)) {
    return { meta, root: repoRoot, source: "repo-build", repoBinary };
  }

  die(
    `platform package ${meta.pkg} not installed and no local build found.\n` +
      `  Options:\n` +
      `    A) Install via npm: \`npm i -g @forebrain-harness/forebrain\` (ensures optionalDependencies install)\n` +
      `    B) Local dev: run \`./npm/scripts/build-platform-packages.sh\` (produces npm/dist/)\n` +
      `    C) Local dev: \`make build\` from repo root (binary plus its dictionary in build/bin)`,
  );
}

function binaryPath(ctx) {
  if (ctx.source === "repo-build") return ctx.repoBinary;
  const { meta, root } = ctx;
  const ext = process.platform === "win32" ? ".exe" : "";
  const p = path.join(root, "vendor", meta.triple, "bin", `${meta.binary}${ext}`);
  if (!existsSync(p)) {
    die(`forebrain binary missing at ${p}`);
  }
  return p;
}

function main() {
  const platform = resolvePlatformPackage();
  const binary = binaryPath(platform);

  const env = { ...process.env };

  const child = spawn(binary, process.argv.slice(2), {
    stdio: "inherit",
    env,
  });

  const forward = (sig) => {
    if (!child.killed) child.kill(sig);
  };
  process.on("SIGINT", () => forward("SIGINT"));
  process.on("SIGTERM", () => forward("SIGTERM"));

  child.on("error", (err) => die(`failed to spawn ${binary}: ${err.message}`));
  child.on("exit", (code, signal) => {
    if (signal) {
      process.kill(process.pid, signal);
      return;
    }
    process.exit(code ?? 0);
  });
}

main();
