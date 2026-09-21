#!/usr/bin/env node
// The command `npx cs-npmrevs` runs. It finds the binary for this machine and
// becomes it: same arguments, same streams, same exit status, same signals.
//
// Nothing here interprets the run. cs-npmrevs separates four exits: 0 done, 1
// failed, 2 called wrongly, and 3 nothing found to act on. A script reads all
// four, so a launcher that collapsed them would report one as another. The
// status is passed through untouched, and the launcher's own failures exit 1,
// the code that already means the command did not do its work.
//
// A signal sent here is forwarded to the binary, and this process lives until
// the binary has exited. `npx cs-npmrevs serve` is therefore stopped the way a
// script stops a server: by signalling the process whose pid it has. A Ctrl-C
// would reach both without the forwarding, because the terminal signals the
// whole foreground group, but a `kill` of this pid alone would leave the
// server running and owned by nothing.

import { spawn } from "node:child_process";
import { constants } from "node:os";
import { binaryPath } from "../index.mjs";

// The exit code cs-npmrevs uses for a run that failed. A launcher that cannot find
// its binary is one of those.
const FAILED = 1;

// What a shell reports for a command a signal ended: 128 plus the number. It
// keeps a Ctrl-C from reading as a clean run, which is what passing on the exit
// code of a signalled process would produce, since there is none.
const signalled = (signal) => 128 + (constants.signals[signal] ?? 0);

let bin;
try {
  bin = binaryPath();
} catch (err) {
  console.error(`cs-npmrevs: ${err.message}`);
  process.exit(FAILED);
}

// The streams are this process's, so a server's log and a command's output
// arrive as they are written rather than when the run ends.
const child = spawn(bin, process.argv.slice(2), { stdio: "inherit" });

// Forwarded rather than acted on: the binary decides what each one means, and
// it already stops a serve on SIGINT and SIGTERM, finishing the requests in
// flight first. Handling them here is also what keeps this process alive to
// report how the binary ended, which is the status its caller waits for.
for (const signal of ["SIGINT", "SIGTERM", "SIGHUP", "SIGQUIT"]) {
  process.on(signal, () => {
    if (!child.killed) child.kill(signal);
  });
}

child.on("error", (err) => {
  const { code } = err;
  const hint =
    code === "ENOENT"
      ? "the file is missing; reinstall with `rm -rf node_modules && npm install`"
      : code === "EACCES"
        ? "the file is not executable; some archive tools drop the permission bit"
        : err.message;
  console.error(`cs-npmrevs: cannot run ${bin}: ${hint}`);
  process.exit(FAILED);
});

child.on("exit", (code, signal) => {
  process.exit(signal ? signalled(signal) : (code ?? FAILED));
});
