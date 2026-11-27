import * as vscode from 'vscode';
import { ChatPanel } from './panel';
import { type PanelState } from './panelHtml';
import { Connection } from './connection';
import { readGatewayConfig } from './config';
import { ConfigError } from './gatewayOrigin';
import { GatewayClient } from './gateway';
import { findForebrainBinary, startGateway } from './gatewayProcess';
import { startRelay } from './relay';
import { redact } from './token';
import { EMPTY_SESSION_STATE, reduceSessionMessage, reconcilePending, type SessionState } from './sessionState';
import { SessionSocket, type ServerMessage } from './sessionSocket';
import { sessionFromRequestPath } from './sessionTarget';
import { ChecklistTree } from './checklistTree';
import { StatusBar } from './statusBar';
import { Approvals } from './approvals';
import { Deliverables } from './deliverables';
import { ForebrainCommands } from './commands';
import { WebviewRpc } from './webviewRpc';
import { devWebviewHtml } from './devWebview';

// Module level so that `deactivate` can reach them: the subscription below
// disposes them too, but a shutdown closes the socket rather than racing it.
let active: Connection | undefined;
let link: SessionSocket | undefined;
/** The host socket's link state: what the status bar's "not connected" reads. */
let linkUp = false;

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  const output = vscode.window.createOutputChannel('Forebrain');
  const log = (line: string) => output.appendLine(`${new Date().toISOString()} ${line}`);
  const panel = new ChatPanel();
  const checklist = new ChecklistTree();
  /** The conversation the relay last saw the Web UI read — the panel's current session. */
  let observed: string | undefined;
  const statusBar = new StatusBar(() => link?.boundSession ?? observed);
  // The fold of everything the host socket receives: one state, four surfaces.
  const state: { current: SessionState } = { current: EMPTY_SESSION_STATE };

  const reflect = (previous: SessionState, next: SessionState): void => {
    checklist.update(next);
    statusBar.update(next, linkUp);
    approvals.onState(previous, next, panel.visible);
    deliverables.onRunEnd(previous, next);
  };

  /** A different conversation: the old one's checklists and approvals do not belong to it. */
  const reset = (): void => {
    const previous = state.current;
    state.current = EMPTY_SESSION_STATE;
    reflect(previous, state.current);
  };

  const listener = (msg: ServerMessage): void => {
    const previous = state.current;
    state.current = reduceSessionMessage(state.current, msg);
    if (state.current !== previous) reflect(previous, state.current);
  };

  const client = () => new GatewayClient(active!.origin);
  const token = () => active?.currentToken() ?? '';

  const approvals = new Approvals({
    client,
    token,
    boundSession: () => link?.boundSession,
    notificationsEnabled: () => vscode.workspace.getConfiguration('forebrain').get<boolean>('approvalNotifications', true),
    focusPanel: () => void vscode.commands.executeCommand('forebrain.chat.focus'),
    reconciled: (pendingIds) => {
      const previous = state.current;
      state.current = reconcilePending(state.current, pendingIds);
      if (state.current !== previous) reflect(previous, state.current);
    },
    log,
  });

  const deliverables = new Deliverables({ client, token, boundSession: () => link?.boundSession, log });

  const commands = new ForebrainCommands({
    workspaceState: context.workspaceState,
    connected: () => Boolean(active?.currentToken()),
    token,
    client,
    observedSession: () => observed,
    showSession: async (id) => {
      if (!active) throw new Error(vscode.l10n.t('the panel is not connected'));
      await active.showSession(id);
    },
    startTurn: async (sessionId, content) => {
      if (!link) throw new Error(vscode.l10n.t('the gateway link is not up'));
      if (link.boundSession !== sessionId) await link.bind(sessionId, 0);
      return link.startRun(sessionId, content);
    },
    focusPanel: () => void vscode.commands.executeCommand('forebrain.chat.focus'),
    log,
  });

  const onConnectionState = (panelState: PanelState): void => {
    if (panelState.kind === 'ready' && !link && active) {
      const socket = new SessionSocket({
        origin: active.origin,
        getToken: () => active?.currentToken() ?? '',
        log,
        listener,
        onLink: (up) => {
          linkUp = up;
          statusBar.update(state.current, up);
        },
      });
      link = socket;
      // A new socket knows no session: the fallback bind is the only thing that
      // can put it on the conversation the panel is showing.
      void socket
        .open()
        .then(async () => {
          if (observed !== undefined && socket.boundSession !== observed) await socket.bind(observed, 0);
        })
        .catch((err: unknown) => log(`session socket: ${err instanceof Error ? err.message : String(err)}`));
      return;
    }
    if (panelState.kind !== 'ready' && link) {
      const closing = link;
      link = undefined;
      linkUp = false;
      reset();
      void closing.close();
    }
  };

  const setPanelState = (panelState: PanelState): void => {
    panel.setState(panelState);
    onConnectionState(panelState);
  };

  const build = (): Connection => {
    const config = readGatewayConfig(); // throws ConfigError: handled by the caller
    const probeClient = new GatewayClient(config.origin);
    return new Connection({
      config, env: process.env,
      // async 包一层：context.secrets.* 返回 vscode.Thenable（PromiseLike，不是 Promise），
      // 直接赋给 ConnectionDeps/SecretStore 的 Promise 签名会 typecheck 失败（TS2739/TS2322）。
      secrets: {
        get: async (k) => context.secrets.get(k),
        store: async (k, v) => { await context.secrets.store(k, v); },
        delete: async (k) => { await context.secrets.delete(k); },
      },
      probe: (t) => probeClient.probe(t), healthy: () => probeClient.health(),
      loadNonce: async () => context.secrets.get('forebrain.relayNonce'),
      saveNonce: async (n) => { await context.secrets.store('forebrain.relayNonce', n); },
      loadBoot: async () => context.secrets.get('forebrain.relayBoot'),
      saveBoot: async (b) => { await context.secrets.store('forebrain.relayBoot', b); },
      loadPort: async () => context.globalState.get<number>('forebrain.relayPort'),
      savePort: async (p) => { await context.globalState.update('forebrain.relayPort', p); },
      startGateway: async () => {
        const bin = await findForebrainBinary(config.binaryPath);
        if (!bin) {
          log('gateway: no forebrain binary found (set forebrain.binaryPath or install @forebrain-harness/forebrain)');
          return null;
        }
        log(`gateway: starting ${bin}`);
        try {
          const p = await startGateway([bin, 'gateway', 'start'], log);
          log(`gateway: started (pid ${p.pid}) at ${p.origin}`);
          return p;
        } catch (err) {
          // startGateway never throws the token (Step 5); a port already taken
          // or a failing binary lands here and is reported, not retried.
          log(`gateway: failed to start: ${err instanceof Error ? err.message : String(err)}`);
          return null;
        }
      },
      startRelay,
      log: (l) => log(redact(l, active?.currentToken() ?? '')),
      emit: setPanelState,
      // Mirror binding: a host socket that never binds a session is told
      // nothing, so the panel's own transcript read is what puts it on one.
      onRequest: (method, path) => {
        const seen = sessionFromRequestPath(method, path);
        if (seen === undefined || seen === observed) return;
        observed = seen;
        reset();
        const socket = link;
        if (socket && socket.boundSession !== seen) {
          socket.bind(seen, 0).catch((err: unknown) => {
            log(`session socket: bind failed: ${err instanceof Error ? err.message : String(err)}`);
          });
        }
      },
    });
  };

  const boot = async (): Promise<void> => {
    try {
      active ??= build();
      await active.start();
    } catch (err) {
      const detail = err instanceof ConfigError ? err.message : err instanceof Error ? err.message : String(err);
      log(`startup failed: ${detail}`);
      // Through the same door as every other state: an active connection left
      // behind here would keep a socket retrying with an empty token.
      setPanelState({ kind: 'unavailable', reason: 'error', detail });
    }
  };

  // Development loading entries (Steps 3–9): the native webview apps in
  // panels, riding the RPC surface. Step 10 removes these when the three
  // views of the alignment contract take over. Two views for now — the chat
  // workbench and the Sessions list — and the host steers between them: a
  // pick in Sessions opens that conversation in chat, and chat reports the
  // conversation on screen so Sessions can mark its row.
  type DevView = 'chat' | 'sessions';
  const devPanels = new Map<DevView, { panel: vscode.WebviewPanel; rpc: WebviewRpc; disposables: vscode.Disposable }>();
  /** A session a view asked for while the target view was still loading: the
   *  webview announces itself with a `ready` frame, and the pending pick goes
   *  out then. */
  const pendingSession: { chat?: string; sessions?: string } = {};
  const pushThemeTo = (panel: vscode.WebviewPanel): void => {
    const kind = vscode.window.activeColorTheme.kind;
    // `HighContrast` is the dark high-contrast kind in this API.
    void panel.webview.postMessage({
      type: 'theme',
      dark: kind === vscode.ColorThemeKind.Dark || kind === vscode.ColorThemeKind.HighContrast,
    });
  };
  const openDevWebview = (view: DevView): void => {
    const shown = devPanels.get(view);
    if (shown) {
      shown.panel.reveal();
      return;
    }
    // `webviewDistRoot` inlining stays here rather than in devWebview.ts: Uri
    // joining is vscode's, the loader itself stays pure for the unit tests.
    const distRoot = vscode.Uri.joinPath(context.extensionUri, 'webview', 'dist');
    const panel = vscode.window.createWebviewPanel(
      view === 'chat' ? 'forebrain.devChat' : 'forebrain.devSessions',
      view === 'chat' ? 'Forebrain Chat (dev)' : 'Forebrain Sessions (dev)',
      vscode.ViewColumn.Active,
      {
        enableScripts: true,
        retainContextWhenHidden: true,
        localResourceRoots: [distRoot],
      },
    );
    panel.webview.html = devWebviewHtml(panel.webview, distRoot, view === 'chat' ? 'index' : 'sessions');
    // One RPC dispatcher per panel; its sockets die with the panel.
    const rpc = new WebviewRpc({
      post: (message) => void panel.webview.postMessage(message),
      origin: active?.origin ?? '',
      token: () => active?.currentToken() ?? '',
      fetchImpl: (input, init) => fetch(input, init),
      log: (l) => log(redact(l, active?.currentToken() ?? '')),
      workspaceRoots: () => (vscode.workspace.workspaceFolders ?? []).map((folder) => folder.uri.fsPath),
      workspaceTrusted: () => vscode.workspace.isTrusted,
      trustSet: async (root, trusted) => {
        await new GatewayClient(active?.origin ?? '').postJson('/api/host/trust', active?.currentToken() ?? '', { path: root, trusted });
      },
      showWarningModal: async (message, detail, confirmLabel, cancelLabel) =>
        vscode.window.showWarningMessage(message, { modal: true, detail }, confirmLabel, cancelLabel),
      bindSession: async (sessionId) => {
        // The webview bound a conversation: follow it, the way the relay's
        // request log is followed today.
        observed = sessionId;
        reset();
        const socket = link;
        if (socket && socket.boundSession !== sessionId) await socket.bind(sessionId, 0);
      },
      openChatSession: async (sessionId) => {
        // An empty pick is the Sessions view's "new chat": open the chat view
        // with nothing to steer.
        if (sessionId) pendingSession.chat = sessionId;
        openDevWebview('chat');
      },
      reportActiveSession: async (sessionId) => {
        pendingSession.sessions = sessionId;
        openDevWebview('sessions');
      },
      openFileRange: (request) => deliverables.openRange(request),
    });
    panel.webview.onDidReceiveMessage((m: unknown) => {
      // The app announces itself once its transport is installed; anything a
      // view asked for while it loaded goes out now.
      if ((m as { type?: unknown })?.type === 'ready') {
        const sessionId = view === 'chat' ? pendingSession.chat : pendingSession.sessions;
        if (sessionId) {
          pendingSession[view] = undefined;
          void panel.webview.postMessage(
            view === 'chat' ? { type: 'openSession', sessionId } : { type: 'activeSession', sessionId },
          );
        }
      }
      rpc.onMessage(m);
    });
    const themeSubscription = vscode.window.onDidChangeActiveColorTheme(() => pushThemeTo(panel));
    panel.onDidDispose(() => {
      rpc.dispose();
      themeSubscription.dispose();
      devPanels.delete(view);
    });
    pushThemeTo(panel);
    devPanels.set(view, { panel, rpc, disposables: themeSubscription });
    context.subscriptions.push(themeSubscription);
  };

  context.subscriptions.push(
    output,
    checklist,
    statusBar,
    approvals,
    vscode.window.createTreeView('forebrain.checklist', { treeDataProvider: checklist, showCollapseAll: false }),
    vscode.window.registerWebviewViewProvider('forebrain.chat', panel, { webviewOptions: { retainContextWhenHidden: true } }),
    vscode.commands.registerCommand('forebrain.openPanel', () => vscode.commands.executeCommand('forebrain.chat.focus')),
    vscode.commands.registerCommand('forebrain.reconnect', () => void active?.reconnect()),
    vscode.commands.registerCommand('forebrain.askAboutSelection', () => commands.askAboutSelection()),
    vscode.commands.registerCommand('forebrain.askAboutFile', (uri?: vscode.Uri) => commands.askAboutFile(uri)),
    vscode.commands.registerCommand('forebrain.openDeliveredFile', () => deliverables.openDeliveredFile()),
    vscode.commands.registerCommand('forebrain.devOpenWebview', (view?: string) => {
      openDevWebview(view === 'sessions' ? 'sessions' : 'chat');
    }),
    vscode.commands.registerCommand('forebrain.enterGatewayToken', async () => {
      const entered = await vscode.window.showInputBox({ prompt: vscode.l10n.t('Forebrain gateway token'), password: true, ignoreFocusOut: true });
      if (entered === undefined) return;
      if (!active) {
        void vscode.window.showInformationMessage(vscode.l10n.t('Forebrain: not connected yet. Run the reconnect command first.'));
        return;
      }
      const verdict = await active.enterToken(entered);
      if (verdict === 'rejected') void vscode.window.showErrorMessage(vscode.l10n.t('Forebrain: the gateway rejected that token.'));
      else if (verdict === 'unreachable') void vscode.window.showWarningMessage(vscode.l10n.t('Forebrain: the gateway is not answering.'));
    }),
    vscode.commands.registerCommand('forebrain.openWebUiInBrowser', () => {
      if (!active) return;
      const current = active.currentToken();
      if (!current) { void vscode.window.showInformationMessage(vscode.l10n.t('Forebrain: connect the panel first.')); return; }
      // The sign-in link the gateway's own banner prints (the Web UI reads the
      // fragment once and wipes it). The token leaves the extension only into
      // the person's own browser, the way the banner already does.
      void vscode.env.openExternal(vscode.Uri.parse(`${active.origin}/login#token=${encodeURIComponent(current)}`));
    }),
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration('forebrain.gatewayUrl') || e.affectsConfiguration('forebrain.binaryPath') || e.affectsConfiguration('forebrain.autoStartGateway')) {
        // A new origin/binary needs a new Connection; the old one owns a relay
        // and possibly a gateway child, so it is disposed, not dropped.
        const previous = active;
        active = undefined;
        void (async () => {
          await previous?.dispose();
          await boot();
        })();
      }
    }),
    panel.onCommand((id) => void vscode.commands.executeCommand(id)),
    panel.onFileRange((range) => void deliverables.openRange(range)),
    {
      dispose: () => {
        // `Connection.dispose()` emits nothing, so nothing else would ever
        // close the host socket.
        void link?.close();
        link = undefined;
        linkUp = false;
        void active?.dispose();
      },
    },
  );
  // Before the first event arrives: the checklist view's welcome text needs the
  // context key set, and the status bar needs its first line.
  checklist.activate();
  statusBar.update(state.current, false);
  await boot();
}

export async function deactivate(): Promise<void> {
  await link?.close();
  link = undefined;
  linkUp = false;
  await active?.dispose();
  active = undefined;
}