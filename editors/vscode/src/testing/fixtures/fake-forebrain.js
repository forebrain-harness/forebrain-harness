// A stand-in for the `forebrain` binary: prints one of the gateway banners
// `forebrain gateway start` prints (pkg/gateway/serve_run.go writeStartupBanner),
// then stays alive until SIGTERM, like the real gateway.
const mode = process.argv[2] ?? 'banner-with-token';

if (mode === 'exit-with-error') {
  process.stderr.write('listen tcp 127.0.0.1:6060: bind: address already in use\n');
  process.exit(1);
}

const banner = mode === 'banner-configured'
  ? [
      'Web UI       http://127.0.0.1:6060/',
      'Auth         token',
      'Listening on http://127.0.0.1:6060',
    ]
  : [
      'Web UI       http://127.0.0.1:6060/',
      'Sign in      http://127.0.0.1:6060/login#token=abc123',
      'Token        abc123',
      'Listening on http://127.0.0.1:6060',
    ];

for (const line of banner) process.stdout.write(`${line}\n`);

setInterval(() => {}, 1_000);
process.on('SIGTERM', () => process.exit(0));