import { spawn } from 'node:child_process';
import { constants as fsConstants } from 'node:fs';
import { access } from 'node:fs/promises';
import path from 'node:path';
import { redact, type OwnedGateway } from './token';

export const GATEWAY_START_TIMEOUT_MS = 20_000;

/** How long a gateway that prints no `Token` line gets, before it counts as configured. */
const CONFIGURED_TOKEN_WINDOW_MS = 300;
/** How long SIGTERM gets before the child is killed outright. */
const STOP_TIMEOUT_MS = 5_000;

export interface Banner { origin?: string; token?: string }

/** Reads the lines `forebrain gateway start` prints (pkg/gateway/serve_run.go writeStartupBanner). */
export function parseGatewayBanner(text: string): Banner {
  const out: Banner = {};
  for (const line of text.split(/\r?\n/)) {
    let m = /^Listening on (http:\/\/\S+?)\/?\s*$/.exec(line);
    if (m) out.origin = m[1];
    m = /^Token\s+(\S+)\s*$/.exec(line);
    if (m) out.token = m[1];
  }
  return out;
}

/**
 * The binary to start: the setting when it names an executable file, else the
 * first `forebrain` on PATH that is executable. Undefined when neither is.
 */
export async function findForebrainBinary(binaryPath: string, env: NodeJS.ProcessEnv = process.env): Promise<string | undefined> {
  const candidates = binaryPath ? [binaryPath] : (env.PATH ?? '').split(path.delimiter).filter(Boolean).map((dir) => path.join(dir, 'forebrain'));
  for (const candidate of candidates) {
    try {
      await access(candidate, fsConstants.X_OK);
      return candidate;
    } catch {
      // try the next one
    }
  }
  return undefined;
}

/** A gateway this extension started: its pid, its token when the banner printed one. */
export interface OwnedGatewayProcess extends OwnedGateway {
  readonly pid: number;
  readonly token: string | null;
}

// The banner's sign-in line carries the token in its fragment, and the `Token`
// line is the token: neither may reach the output channel, whatever else does.
const SIGN_IN_LINE = '#token=';
const TOKEN_LINE = /^Token\s/;

/**
 * Starts `command` and waits for the gateway's banner. The token never reaches
 * `log`: stdout is held until the banner settles, then forwarded line by line
 * with the token redacted and the two lines that carry it dropped.
 */
export async function startGateway(
  command: string[],
  log: (line: string) => void,
  timeoutMs = GATEWAY_START_TIMEOUT_MS,
): Promise<OwnedGatewayProcess> {
  const child = spawn(command[0], command.slice(1), { stdio: ['ignore', 'pipe', 'pipe'] });
  const held: string[] = [];
  let token: string | null = null;
  let settled = false;
  let stopping: Promise<void> | undefined;
  let stderr = '';
  let stdout = '';
  let handled = 0;

  const forward = (line: string): void => {
    if (line.includes(SIGN_IN_LINE) || TOKEN_LINE.test(line)) return;
    log(token === null ? line : redact(line, token));
  };
  const flushHeld = (): void => {
    for (const line of held.splice(0)) forward(line);
  };

  // SIGTERM leaves this function synchronously, before the first await: on
  // shutdown the host may not live for another turn of the event loop, and an
  // orphaned gateway is the one thing a person would notice.
  const stop = (): Promise<void> => {
    stopping ??= new Promise<void>((resolve) => {
      if (child.exitCode !== null || child.signalCode !== null) {
        resolve();
        return;
      }
      const kill = setTimeout(() => child.kill('SIGKILL'), STOP_TIMEOUT_MS);
      child.once('exit', () => {
        clearTimeout(kill);
        resolve();
      });
      child.kill('SIGTERM');
    });
    return stopping;
  };

  child.stdout?.setEncoding('utf8');
  child.stderr?.setEncoding('utf8');
  child.stderr?.on('data', (chunk: string) => {
    stderr = (stderr + chunk).slice(-2_000);
    if (!settled) return;
    for (const line of chunk.split(/\r?\n/)) if (line) forward(line);
  });

  return await new Promise<OwnedGatewayProcess>((resolve, reject) => {
    let configuredWindow: NodeJS.Timeout | undefined;

    const finish = (banner: Banner): void => {
      if (settled || banner.origin === undefined) return;
      settled = true;
      token = banner.token ?? null;
      clearTimeout(configuredWindow);
      clearTimeout(deadline);
      flushHeld();
      resolve({ origin: banner.origin, token, pid: child.pid ?? -1, stop });
    };

    const deadline = setTimeout(() => {
      if (settled) return;
      settled = true;
      clearTimeout(configuredWindow);
      void stop().then(() => reject(new Error(`gateway did not print "Listening on" within ${timeoutMs / 1000}s`)));
    }, timeoutMs);

    child.stdout?.on('data', (chunk: string) => {
      stdout += chunk;
      const lines = stdout.split(/\r?\n/);
      const complete = lines.length - 1;
      for (; handled < complete; handled++) {
        if (settled) forward(lines[handled]);
        else held.push(lines[handled]);
      }
      if (settled) return;
      const banner = parseGatewayBanner(stdout);
      if (banner.origin === undefined) return;
      if (banner.token !== undefined) {
        finish(banner);
        return;
      }
      // No `Token` line yet. A gateway configured with a token prints none, so
      // the banner gets a moment to finish before settling for "configured".
      configuredWindow ??= setTimeout(() => finish(parseGatewayBanner(stdout)), CONFIGURED_TOKEN_WINDOW_MS);
    });

    child.on('error', (err: Error) => {
      if (settled) return;
      settled = true;
      clearTimeout(configuredWindow);
      clearTimeout(deadline);
      reject(new Error(`gateway could not be started (${command[0]}): ${err.message}`));
    });
    // `close`, not `exit`: by then the streams are drained, so a port clash's
    // stderr line is complete when the message below quotes it.
    child.once('close', (code, signal) => {
      if (settled) return;
      settled = true;
      clearTimeout(configuredWindow);
      clearTimeout(deadline);
      const tail = stderr.trim().slice(-400);
      reject(new Error(`gateway exited before "Listening on" (code ${code ?? 'null'}, signal ${signal ?? 'none'}): ${tail}`));
    });
  });
}