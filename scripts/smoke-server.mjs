import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtemp, mkdir, rm } from 'node:fs/promises';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { randomUUID } from 'node:crypto';
import { setTimeout as delay } from 'node:timers/promises';
import { parseVersion } from './release.mjs';

const [binary, expectedVersion, portArg = '18080', ...extra] = process.argv.slice(2);
assert(binary && expectedVersion && extra.length === 0, 'Usage: smoke-server.mjs <binary> <version> [port]');
if (expectedVersion !== 'dev') assert.equal(parseVersion(expectedVersion).tag, expectedVersion);
const port = Number(portArg);
assert(Number.isInteger(port) && port > 0 && port <= 65535, 'Invalid smoke port');
// Refuse to accidentally prove another process's server when this port is occupied.
const probe = createServer();
await new Promise((accept, reject) => { probe.once('error', reject); probe.listen(port, '127.0.0.1', accept); });
await new Promise((accept, reject) => probe.close((error) => error ? reject(error) : accept()));

const home = await mkdtemp(join(tmpdir(), 'agents-server-smoke-'));
const data = join(home, 'data');
await mkdir(data);
let child;
let exited;
let finished = false;
let spawnError;
let output = '';
const base = `http://127.0.0.1:${port}`;
function record(chunk) { output = (output + chunk).slice(-8192); }
async function request(path, options = {}) {
  const response = await fetch(base + path, { ...options, signal: AbortSignal.timeout(2000) });
  assert(response.ok, `${path}: HTTP ${response.status}`);
  return response;
}
async function binding(method) {
  return request('/wails/runtime', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ object: 0, method: 0, args: {
      'call-id': randomUUID(), methodName: `github.com/vietlubu/agents-dashboard/internal/service.AppService.${method}`, args: [],
    } }),
  });
}
try {
  child = spawn(resolve(binary), [], { env: {
    ...process.env, HOME: home, USERPROFILE: home,
    XDG_CONFIG_HOME: join(home, '.config'), XDG_DATA_HOME: join(home, '.local', 'share'),
    APPDATA: join(home, 'AppData', 'Roaming'), LOCALAPPDATA: join(home, 'AppData', 'Local'),
    AGENTS_DASHBOARD_HOME: data, AGENTS_DASHBOARD_AUTO_SYNC_PRICES: 'false',
    AGENTS_DASHBOARD_SERVER_HOST: '127.0.0.1', AGENTS_DASHBOARD_SERVER_PORT: String(port),
  }, stdio: ['ignore', 'pipe', 'pipe'] });
  child.stdout.on('data', record);
  child.stderr.on('data', record);
  exited = new Promise((accept) => {
    child.once('exit', () => { finished = true; accept(); });
    child.once('error', (error) => { spawnError = error; finished = true; accept(); });
  });
  const deadline = Date.now() + 30000;
  let health;
  while (Date.now() < deadline) {
    if (finished) throw spawnError ?? new Error(`Server exited before readiness: ${output}`);
    try { health = await (await request('/health')).json(); } catch { /* bounded startup wait */ }
    if (health?.status === 'ok') break;
    await delay(100);
  }
  assert.equal(health?.status, 'ok', `Server health deadline exceeded: ${output}`);
  const index = await (await request('/')).text();
  assert(index.includes('id="app"') && index.includes('/assets/'), 'Server did not serve the built frontend');
  const version = await (await binding('Version')).text();
  assert.equal(version, expectedVersion, 'Runtime version does not contain the full expected stamp');
  const status = await (await binding('UpdateStatus')).json();
  assert.equal(status.currentVersion, expectedVersion);
  assert.equal(status.enabled, false);
  assert.equal(status.canInstall, false);
  assert.equal(status.state, 'disabled');
  assert.equal(status.reason, 'server');
  assert.equal(finished, false, `Server exited during smoke: ${output}`);
  console.log(`Server smoke passed: /health=ok, embedded frontend, Version=${version}, updater=disabled/server`);
} finally {
  if (child && !finished) {
    child.kill();
    let timer;
    await Promise.race([exited, new Promise((accept) => {
      timer = setTimeout(() => { child.kill('SIGKILL'); accept(); }, 10000);
    })]);
    clearTimeout(timer);
    await exited;
  }
  await rm(home, { recursive: true, force: true });
}
