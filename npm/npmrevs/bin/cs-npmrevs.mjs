#!/usr/bin/env node
// The command `npx cs-npmrevs` runs. It finds the binary for this machine and
// becomes it: same arguments, same streams, same exit status.
//
// Nothing here interprets the run. cs-npmrevs separates four exits: 0 done, 1
// failed, 2 called wrongly, and 3 nothing found to act on. A script reads all
// four, so a launcher that collapsed them would report one as another. The
// status is passed through untouched, and the launcher's own failures exit 1,
// the code that already means the command did not do its work.
//
// A server run through here, `npx cs-npmrevs serve`, stops when this process is
// signalled: the child shares its process group, so Ctrl-C reaches both.

import { spawnSync } from "node:child_process";
import { constants } from "node:os";
import { binaryPath } from "../index.mjs";

// The exit code cs-npmrevs uses for a run that failed. A launcher that cannot find
// its binary is one of those.
const FAILED = 1;

let bin;
try {
  bin = binaryPath();
} catch (err) {
  console.error(`cs-npmrevs: ${err.message}`);
  process.exit(FAILED);
}

const result = spawnSync(bin, process.argv.slice(2), {
  // The streams are the parent's, so a server's log and a command's output
  // arrive as they are written rather than when the run ends.
  stdio: "inherit",
});

if (result.error) {
  const { code } = result.error;
  const hint =
    code === "ENOENT"
      ? "the file is missing; reinstall with `rm -rf node_modules && npm install`"
      : code === "EACCES"
        ? "the file is not executable; some archive tools drop the permission bit"
        : result.error.message;
  console.error(`cs-npmrevs: cannot run ${bin}: ${hint}`);
  process.exit(FAILED);
}

// A binary killed by a signal has no exit code. Reporting the shell's
// 128 + signal keeps a Ctrl-C from reading as a clean run, which is what a
// bare `process.exit(result.status)` would produce, since status is null here.
if (result.signal) {
  process.exit(128 + (constants.signals[result.signal] ?? 0));
}

process.exit(result.status ?? FAILED);
