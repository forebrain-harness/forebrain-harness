export interface GatewayConfig {
  /** A bare http(s) origin: `http://127.0.0.1:6060`. A trailing slash is dropped. */
  readonly origin: string;
  readonly binaryPath: string;
  readonly autoStartGateway: boolean;
  readonly relayLogRequests: boolean;
}

export class ConfigError extends Error {}

/**
 * What an error message may say about the value the person typed. A sign-in
 * link pasted into forebrain.gatewayUrl by mistake carries the token in its
 * fragment, and the message reaches both the output channel and the panel.
 */
function echo(raw: string): string {
  return raw.replace(/token=[^\s&#]*/gi, 'token=‹redacted›');
}

/**
 * Validates the origin. The origin becomes the upstream of a relay that adds
 * the token to every request, so it may not carry a path, a query, a fragment
 * or credentials: anything after the host would be a request the relay could
 * be talked into making.
 */
export function parseGatewayOrigin(raw: string): string {
  let url: URL;
  try {
    url = new URL(raw.trim());
  } catch {
    throw new ConfigError(`forebrain.gatewayUrl is not a URL: "${echo(raw.trim())}"`);
  }
  const trimmed = raw.trim().replace(/\/+$/, '');
  if (url.username || url.password) {
    throw new ConfigError('forebrain.gatewayUrl must not carry credentials');
  }
  if (url.origin !== trimmed || (url.protocol !== 'http:' && url.protocol !== 'https:')) {
    throw new ConfigError(`forebrain.gatewayUrl must be a bare http(s) origin such as http://127.0.0.1:6060 (no path or query): "${echo(trimmed)}"`);
  }
  return trimmed;
}