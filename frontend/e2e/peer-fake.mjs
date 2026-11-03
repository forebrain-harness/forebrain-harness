// A stand-in for another forebrain process on this machine (plan 200 §3).
//
// A held peer message is the one peer fact no database row can stand in for: it
// lives in the receiving process's memory, and only a real frame from a real
// second process puts it there. So this script *is* that second process, as far
// as the protocol can tell:
//
//   1. it listens on a Unix socket of its own, in the gateway's socket
//      namespace, so the rendezvous record it writes names a live socket;
//   2. it writes a rendezvous record for its own pid, carrying the forebrain
//      extension block the FB-mode source gate requires;
//   3. it connects to the gateway's socket and sends one user frame holding an
//      envelope, claiming the mode the receiver does not run in — which is what
//      makes the gateway hold the message for the person.
//
// It prints `HELD` once the gateway's receipt says so, and exits. The record and
// socket it leaves behind are this run's own (FOREBRAIN_PEERS_DIR and
// FOREBRAIN_PEER_SOCKET_DIR point into the acceptance scratch tree), and the
// gateway's own dead-record sweep removes the record.
//
// Environment: FOREBRAIN_PEERS_DIR, FOREBRAIN_PEER_SOCKET_DIR, E2E_PEER_ROOT,
// E2E_PEER_NAME, E2E_PEER_MESSAGE, E2E_PEER_FROM_MODE.

import { execFileSync } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { createServer, connect } from 'node:net'

const registryDir = process.env.FOREBRAIN_PEERS_DIR
const socketDir = process.env.FOREBRAIN_PEER_SOCKET_DIR
const root = process.env.E2E_PEER_ROOT
const name = process.env.E2E_PEER_NAME || 'forebrain-e2e-fake'
const body = process.env.E2E_PEER_MESSAGE || 'are you still editing open.go?'
const fromMode = process.env.E2E_PEER_FROM_MODE || 'bypass'

for (const [key, value] of Object.entries({ FOREBRAIN_PEERS_DIR: registryDir, FOREBRAIN_PEER_SOCKET_DIR: socketDir, E2E_PEER_ROOT: root })) {
  if (!value) {
    console.error(`${key} is required`)
    process.exit(2)
  }
}

/** The gateway's own socket, from the record it wrote at startup. */
function gatewaySocket() {
  for (const entry of readdirSync(registryDir)) {
    if (!/^\d+\.json$/.test(entry)) continue
    try {
      const record = JSON.parse(readFileSync(join(registryDir, entry), 'utf8'))
      if (record.entrypoint === 'forebrain-gateway' && record.messagingSocketPath) {
        return record.messagingSocketPath
      }
    } catch {
      // A record a concurrent writer is still writing is not ours to read.
    }
  }
  return ''
}

/** This process's start time, spelled the way pkg/peer spells it. */
function procStart(pid) {
  return execFileSync('ps', ['-o', 'lstart=', '-p', String(pid)], {
    encoding: 'utf8',
    env: { ...process.env, LC_ALL: 'C', TZ: 'UTC' },
  }).trim()
}

/** The envelope the model would have read, with the attributes in wire order
 *  and the body on its own line — the exact shape `peer.Build` renders. */
function envelope(address) {
  const escaped = String(body).replace(/</g, '\\<')
  return `<cross-session-message from="${address}" from-name="${name}" from-mode="${fromMode}">\n${escaped}\n</cross-session-message>`
}

/** The wire spelling of a local address: `uds:` plus every byte outside the
 *  protocol's own character set as %XX. A scratch path under $TMPDIR carries
 *  underscores, which the address grammar does not allow. */
function encodeUDS(path) {
  let out = ''
  for (const byte of new TextEncoder().encode(path)) {
    const ch = String.fromCharCode(byte)
    out += /[A-Za-z0-9:_/.-]/.test(ch) ? ch : `%${byte.toString(16).toUpperCase().padStart(2, '0')}`
  }
  return `uds:${out}`
}

const pid = process.pid
const socketPath = join(socketDir, `fake-peer-${pid}.sock`)
const address = encodeUDS(socketPath)
const server = createServer((socket) => {
  // The gateway answers a frame by connecting back to the address the frame
  // claimed, which is this socket: the receipt arrives on its own connection.
  let received = ''
  socket.on('data', (chunk) => {
    received += chunk.toString('utf8')
    for (const line of received.split('\n')) {
      if (!line.trim()) continue
      let parsed
      try {
        parsed = JSON.parse(line)
      } catch {
        continue
      }
      if (parsed?.action !== 'peer_message_status') continue
      console.log(`RECEIPT ${parsed.status}`)
      if (parsed.status === 'held') {
        console.log('HELD')
        clearTimeout(timer)
        server.close()
        process.exit(0)
      }
    }
  })
})
server.listen(socketPath)

server.on('error', (error) => {
  console.error(`fake peer: listen ${socketPath}: ${error.message}`)
  process.exit(2)
})

// The record the source gate reads. Every field the reader knows is set; the
// extension block is what makes this process a forebrain rather than a stranger.
const now = Date.now()
const record = {
  pid,
  sessionId: 'e2e-fake-peer',
  cwd: root,
  startedAt: now,
  procStart: procStart(pid),
  version: 'forebrain e2e',
  peerProtocol: 1,
  peerFeatures: ['reply_across_default_dirs'],
  kind: 'interactive',
  entrypoint: 'forebrain-tui',
  pidDomain: 'darwin',
  name,
  nameSource: 'user',
  nameSince: now,
  status: 'idle',
  updatedAt: now,
  statusUpdatedAt: now,
  messagingSocketPath: socketPath,
  forebrain: {
    v: 1,
    surface: 'tui',
    agents: [{
      id: 'e2e-fake',
      name,
      kind: 'primary',
      root,
      branch: 'main',
      status: 'idle',
      task: '',
      mode: fromMode,
      since: now,
    }],
  },
}
writeFileSync(join(registryDir, `${pid}.json`), JSON.stringify(record))

const frame = JSON.stringify({
  msgV: 1,
  msg_id: randomUUID(),
  type: 'user',
  message: { role: 'user', content: envelope(address) },
  priority: 'next',
  from: address,
})

const settle = (message, code) => {
  console.error(`fake peer: ${message}`)
  server.close()
  process.exit(code)
}

const timer = setTimeout(() => settle('timed out waiting for the gateway receipt', 1), 15_000)

/** Sleep without yielding, for the few ticks the gateway needs to write its
 *  own record after it starts listening. */
function sleep(ms) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms)
}

let target = gatewaySocket()
for (let attempts = 0; !target && attempts < 100; attempts++) {
  sleep(100)
  target = gatewaySocket()
}
if (!target) settle('no forebrain-gateway record in the registry', 1)

const connection = connect(target)
connection.on('connect', () => connection.write(`${frame}\n`))
connection.on('error', (error) => settle(`connect ${target}: ${error.message}`, 1))
