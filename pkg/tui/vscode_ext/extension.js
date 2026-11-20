const vscode = require('vscode');
const fs = require('fs');

function real(p) { try { return fs.realpathSync(p); } catch (e) { return p; } }

async function gitAPI() {
  const ext = vscode.extensions.getExtension('vscode.git');
  if (!ext) { return null; }
  const exports = await ext.activate();
  return exports.getAPI(1);
}

// The git extension exposes a repository object before `git status` populates
// its resource states, and git.openChange silently no-ops on a resource it
// cannot find — so wait for the file to appear in a change list first.
async function waitForChange(api, root, path, deadlineMs) {
  const deadline = Date.now() + deadlineMs;
  let matched = null;
  while (Date.now() < deadline) {
    for (const repo of api.repositories) {
      const rr = real(repo.rootUri.fsPath);
      if (rr !== root && !root.startsWith(rr + '/')) { continue; }
      matched = repo;
      const s = repo.state;
      const all = [...s.workingTreeChanges, ...s.indexChanges, ...s.mergeChanges, ...s.untrackedChanges];
      if (all.some(c => real(c.uri.fsPath) === path)) { return { repo, changed: true }; }
    }
    await new Promise(res => setTimeout(res, 250));
  }
  return { repo: matched, changed: false };
}

function activate(context) {
  context.subscriptions.push(vscode.window.registerUriHandler({
    async handleUri(uri) {
      const q = new URLSearchParams(uri.query);
      const path = real(q.get('path') || '');
      const root = real(q.get('root') || '');
      if (!path) { return; }
      try {
        const cfg = vscode.workspace.getConfiguration('diffEditor');
        if (cfg.get('hideUnchangedRegions.enabled') === false) {
          await cfg.update('hideUnchangedRegions.enabled', true, vscode.ConfigurationTarget.Global);
        }
        const api = await gitAPI();
        const found = api ? await waitForChange(api, root, path, 10000) : { repo: null, changed: false };
        if (found.changed) {
          await vscode.commands.executeCommand('git.openChange', vscode.Uri.file(path));
          await vscode.commands.executeCommand('workbench.scm.focus');
          return;
        }
        // Clean file, ignored file, or a window that does not hold the repo:
        // a plain editor is the graceful answer, never a silent no-op.
        await vscode.window.showTextDocument(vscode.Uri.file(path));
      } catch (e) {
        vscode.window.showWarningMessage('Forebrain: could not open the Source Control diff: ' + e);
      }
    }
  }));
}

module.exports = { activate };
