import { createHash } from 'node:crypto';
import { createReadStream } from 'node:fs';
import { appendFile, mkdir, readFile, readdir, writeFile } from 'node:fs/promises';
import { spawn } from 'node:child_process';
import { resolve, join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const EXPECTED_ASSETS = Object.freeze([
  'agents-dashboard-macos-arm64.zip',
  'agents-dashboard-windows-amd64.exe',
  'agents-dashboard-windows-amd64-installer.exe',
  'agents-dashboard-linux-amd64.tar.gz',
  'agents-dashboard-linux-arm64.tar.gz',
  'agents-dashboard-server-macos-arm64.tar.gz',
  'agents-dashboard-server-windows-amd64.zip',
  'agents-dashboard-server-linux-amd64.tar.gz',
  'agents-dashboard-server-linux-arm64.tar.gz',
].sort());
const RELEASE_ASSETS = [...EXPECTED_ASSETS, 'SHA256SUMS'].sort();
const VERSION_SHAPE = /^v?\d{2}\.\d{2}\.\d{2}\.\d{3}(?![\s\S])/;

export function parseVersion(value) {
  if (typeof value !== 'string' || !VERSION_SHAPE.test(value)) {
    throw new Error(`Invalid release version: ${value}`);
  }
  const date = value.replace(/^v/, '').slice(0, 8);
  const [year, month, day, serial] = value.replace(/^v/, '').split('.').map(Number);
  const actual = new Date(Date.UTC(2000 + year, month - 1, day));
  if (actual.getUTCFullYear() !== 2000 + year || actual.getUTCMonth() !== month - 1 ||
      actual.getUTCDate() !== day || serial < 1 || serial > 999) {
    throw new Error(`Invalid release version: ${value}`);
  }
  return {
    tag: `v${value.replace(/^v/, '')}`, date, year, month, day, serial,
    numeric: `${year}.${month}.${day}.${serial}`,
    short: `${year}.${month}.${day}`,
    bundle: `${year}.${month * 100 + day}.${serial}`,
  };
}

export function datePrefix(date) {
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone: 'Asia/Ho_Chi_Minh', year: 'numeric', month: '2-digit', day: '2-digit',
  }).formatToParts(date);
  const part = (type) => parts.find((entry) => entry.type === type).value;
  const year = Number(part('year'));
  if (year < 2000 || year > 2099) throw new Error('Release date must be in 2000..2099');
  return `${part('year').slice(-2)}.${part('month')}.${part('day')}`;
}

function calverTag(tag) {
  if (!tag.startsWith('v') || !VERSION_SHAPE.test(tag)) return null;
  return parseVersion(tag);
}

export function allocateTag(tags, date) {
  const prefix = datePrefix(date);
  let maximum = 0;
  for (const tag of tags) {
    const parsed = calverTag(tag);
    if (parsed?.date === prefix) maximum = Math.max(maximum, parsed.serial);
  }
  if (maximum === 999) throw new Error(`Release serial exhausted for ${prefix} (999)`);
  return `v${prefix}.${String(maximum + 1).padStart(3, '0')}`;
}

function apiClient({ repository, token, fetch, baseURL }) {
  if (!/^[\w.-]+\/[\w.-]+$/.test(repository ?? '')) throw new Error('Invalid GITHUB_REPOSITORY');
  if (!token) throw new Error('GH_TOKEN is required');
  const base = `${baseURL.replace(/\/$/, '')}/`;
  const origin = new URL(base).origin;
  const root = `repos/${repository}/`;
  const headers = {
    Accept: 'application/vnd.github+json', Authorization: `Bearer ${token}`,
    'X-GitHub-Api-Version': '2026-03-10',
  };
  async function request(path, { method = 'GET', body, allow404 = false, accept } = {}) {
    const url = new URL(path.startsWith('http') ? path : `${root}${path}`, base);
    if (url.origin !== origin) throw new Error('Refusing authenticated request outside API origin');
    const response = await fetch(url.href, {
      method, headers: { ...headers, ...(accept ? { Accept: accept } : {}),
        ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
    if (allow404 && response.status === 404) return { data: null, response };
    if (!response.ok) {
      const error = new Error(`GitHub API ${method} ${url.pathname} failed (HTTP ${response.status})`);
      error.status = response.status;
      throw error;
    }
    if (accept === 'application/octet-stream') return { response };
    const text = await response.text();
    return { data: text ? JSON.parse(text) : null, response };
  }
  async function list(path) {
    const result = [];
    const visited = new Set();
    while (path) {
      if (visited.has(path)) throw new Error('GitHub API pagination cycle');
      visited.add(path);
      const { data, response } = await request(path);
      if (!Array.isArray(data)) throw new Error('GitHub API list response is not an array');
      result.push(...data);
      path = response.headers.get('link')?.match(/<([^>]+)>;\s*rel="next"/)?.[1];
    }
    return result;
  }
  async function commit(object) {
    const visited = new Set();
    while (object?.type === 'tag') {
      if (visited.has(object.sha)) throw new Error('Annotated tag cycle');
      visited.add(object.sha);
      object = (await request(`git/tags/${encodeURIComponent(object.sha)}`)).data?.object;
    }
    if (object?.type !== 'commit' || typeof object.sha !== 'string' || !object.sha) {
      throw new Error('Tag does not resolve to a commit');
    }
    return object.sha;
  }
  async function tagCommit(tag) {
    const { data } = await request(`git/ref/tags/${encodeURIComponent(tag)}`);
    return commit(data?.object);
  }
  async function release(tag) {
    const { data } = await request(`releases/tags/${encodeURIComponent(tag)}`, { allow404: true });
    if (data) return data;
    // GitHub's by-tag endpoint returns 404 for drafts, even when authenticated.
    const matches = (await list('releases?per_page=100')).filter((release) => release.tag_name === tag);
    if (matches.length > 1) throw new Error(`Multiple releases match reserved tag ${tag}`);
    return matches[0] ?? null;
  }
  async function assets(release) {
    return list(`releases/${release.id}/assets?per_page=100`);
  }
  return { request, list, commit, tagCommit, release, assets };
}

function validateUploadedAssets(assets) {
  const names = assets.map((asset) => asset.name).sort();
  if (names.length !== RELEASE_ASSETS.length || names.some((name, index) => name !== RELEASE_ASSETS[index])) {
    throw new Error('Release assets must contain exactly the 9 expected payloads and SHA256SUMS');
  }
  if (assets.some((asset) => asset.state !== 'uploaded')) throw new Error('Release has assets not fully uploaded');
}

async function publishedRelease(api, tag) {
  const release = await api.release(tag);
  if (!release || release.draft) return false;
  if (release.prerelease) throw new Error(`Reserved tag ${tag} has a prerelease instead of a stable release`);
  validateUploadedAssets(await api.assets(release));
  return true;
}

export async function reserve({ repository, sha, token, fetch = globalThis.fetch,
  clock = () => new Date(), baseURL = 'https://api.github.com' }) {
  if (!sha) throw new Error('GITHUB_SHA is required');
  const api = apiClient({ repository, token, fetch, baseURL });
  let date;
  async function refs() {
    const entries = await api.list('git/matching-refs/tags/v?per_page=100');
    const versions = [];
    for (const ref of entries) {
      const tag = ref.ref?.replace(/^refs\/tags\//, '');
      if (typeof tag !== 'string' || !calverTag(tag)) continue;
      versions.push({ tag, sha: await api.commit(ref.object) });
    }
    return versions;
  }
  let versions = await refs();
  for (;;) {
    const matches = versions.filter((entry) => entry.sha === sha);
    if (matches.length > 1) throw new Error('Multiple release tags point to GITHUB_SHA');
    if (matches.length === 1) {
      const tag = matches[0].tag;
      return { tag, published: await publishedRelease(api, tag) };
    }
    // Capture the Vietnam date once; a collision retry must not cross midnight.
    if (!date) date = new Date(clock());
    const tag = allocateTag(versions.map((entry) => entry.tag), date);
    try {
      await api.request('git/refs', { method: 'POST', body: { ref: `refs/tags/${tag}`, sha } });
      return { tag, published: false };
    } catch (error) {
      if (error.status !== 409 && error.status !== 422) throw error;
      const fresh = await refs();
      if (!fresh.some((entry) => entry.tag === tag)) throw error;
      versions = fresh;
    }
  }
}

function replaceUnique(text, pattern, replacement, anchor) {
  if ([...text.matchAll(pattern)].length !== 1) throw new Error(`Metadata anchor missing or not unique: ${anchor}`);
  return text.replace(pattern, replacement);
}

export async function metadata(tag, outputDir, { root = process.cwd() } = {}) {
  if (tag === 'dev') return;
  const version = parseVersion(tag);
  if (tag !== version.tag) throw new Error('Metadata release tag must include its v prefix');
  const inputs = ['darwin/Info.plist', 'windows/info.json', 'windows/wails.exe.manifest'];
  const source = inputs.map((file) => join(root, 'build', file));
  const destination = inputs.map((file) => resolve(outputDir, file));
  if (destination.some((file, index) => file === resolve(source[index]))) {
    throw new Error('Release metadata output must not overwrite committed templates');
  }
  let [plist, json, manifest] = await Promise.all(source.map((file) => readFile(file, 'utf8')));
  for (const [key, value] of [['CFBundleShortVersionString', version.short], ['CFBundleVersion', version.bundle]]) {
    if ([...plist.matchAll(new RegExp(`<key>\\s*${key}\\s*</key>`, 'g'))].length !== 1) {
      throw new Error(`Metadata anchor missing or not unique: ${key}`);
    }
    plist = replaceUnique(plist, new RegExp(`(<key>\\s*${key}\\s*</key>\\s*<string>)[^<]*(</string>)`, 'g'),
      (_, before, after) => `${before}${value}${after}`, key);
  }
  const info = JSON.parse(json);
  for (const key of ['fixed', 'file_version', 'info', '0000', 'ProductVersion']) {
    if ([...json.matchAll(new RegExp(`"${key}"\\s*:`, 'g'))].length !== 1) {
      throw new Error(`Metadata anchor missing or not unique: ${key}`);
    }
  }
  if (typeof info.fixed?.file_version !== 'string' || typeof info.info?.['0000']?.ProductVersion !== 'string') {
    throw new Error('Metadata version anchors must be strings');
  }
  if ([...json.matchAll(/"FileVersion"\s*:/g)].length > 1) throw new Error('Metadata anchor not unique: FileVersion');
  info.fixed.file_version = version.numeric;
  info.info['0000'].ProductVersion = version.tag;
  info.info['0000'].FileVersion = version.tag;
  const outer = manifest.replace(/<dependentAssembly\b[^>]*>[\s\S]*?<\/dependentAssembly\s*>/g, '');
  if ([...outer.matchAll(/<assemblyIdentity\b[^>]*\/?\s*>/g)].length !== 1) {
    throw new Error('Metadata anchor missing or not unique: outer assemblyIdentity');
  }
  const identity = outer.match(/<assemblyIdentity\b[^>]*\/?\s*>/)[0];
  const stamped = replaceUnique(identity, /(\bversion\s*=\s*)(["'])[^"']*\2/g,
    (_, before, quote) => `${before}${quote}${version.numeric}${quote}`, 'outer assemblyIdentity version');
  manifest = manifest.replace(identity, stamped);
  const contents = [plist, `${JSON.stringify(info, null, 2)}\n`, manifest];
  await Promise.all(destination.map(async (file, index) => {
    await mkdir(resolve(file, '..'), { recursive: true });
    await writeFile(file, contents[index]);
  }));
}

async function localManifest(assetDir, requireChecksums) {
  const entries = await readdir(assetDir, { withFileTypes: true });
  if (entries.some((entry) => !entry.isFile())) throw new Error('Release assets must be regular files, not directories or symlinks');
  const names = entries.map((entry) => entry.name).sort();
  const expected = requireChecksums || names.includes('SHA256SUMS') ? RELEASE_ASSETS : EXPECTED_ASSETS;
  if (names.length !== expected.length || names.some((name, index) => name !== expected[index])) {
    throw new Error('Asset directory must contain exactly the 9 expected payloads and optional SHA256SUMS');
  }
}

async function hashFile(path) {
  const hash = createHash('sha256');
  for await (const chunk of createReadStream(path)) hash.update(chunk);
  return hash.digest('hex');
}

async function payloadHashes(assetDir) {
  const hashes = new Map();
  for (const name of EXPECTED_ASSETS) hashes.set(name, await hashFile(join(assetDir, name)));
  return hashes;
}

function checksumText(hashes) {
  return [...hashes].map(([name, digest]) => `${digest}  ${name}\n`).join('');
}

export async function checksums(assetDir) {
  await localManifest(assetDir, false);
  const output = join(assetDir, 'SHA256SUMS');
  await writeFile(output, checksumText(await payloadHashes(assetDir)));
  return output;
}

async function runGh(args, token) {
  await new Promise((resolvePromise, reject) => {
    const child = spawn('gh', args, { stdio: ['ignore', 'ignore', 'pipe'], env: { ...process.env, GH_TOKEN: token } });
    let stderr = '';
    child.stderr.on('data', (chunk) => { stderr += chunk; });
    child.on('error', reject);
    child.on('close', (code) => code === 0 ? resolvePromise() : reject(new Error(`gh failed (${code}): ${stderr.trim().replaceAll(token, '[REDACTED]')}`)));
  });
}

async function verifyUploaded(api, release, hashes) {
  const assets = await api.assets(release);
  validateUploadedAssets(assets);
  for (const asset of assets) {
    const expected = hashes.get(asset.name);
    if (asset.digest != null) {
      if (asset.digest !== `sha256:${expected}`) throw new Error(`Uploaded asset checksum mismatch: ${asset.name}`);
      continue;
    }
    const { response } = await api.request(`releases/assets/${asset.id}`, { accept: 'application/octet-stream' });
    const hash = createHash('sha256');
    if (!response.body) throw new Error(`Uploaded asset download has no body: ${asset.name}`);
    for await (const chunk of response.body) hash.update(chunk);
    if (hash.digest('hex') !== expected) throw new Error(`Uploaded asset checksum mismatch: ${asset.name}`);
  }
}

async function makeLatest(api, sha) {
  const { data: latest } = await api.request('releases/latest', { allow404: true });
  if (!latest) return 'true';
  parseVersion(latest.tag_name);
  const latestSHA = await api.tagCommit(latest.tag_name);
  const { data: comparison } = await api.request(`compare/${encodeURIComponent(latestSHA)}...${encodeURIComponent(sha)}`);
  if (comparison?.status === 'ahead' || comparison?.status === 'identical') return 'true';
  if (comparison?.status === 'behind') return 'false';
  if (comparison?.status === 'diverged') {
    console.warn('::warning::Release commit diverges from latest; publishing without latest promotion');
    return 'false';
  }
  throw new Error('GitHub comparison returned an unknown ancestry status');
}

export async function publish({ repository, sha, token, tag, runId, assetDir,
  fetch = globalThis.fetch, baseURL = 'https://api.github.com', gh }) {
  const version = parseVersion(tag);
  if (tag !== version.tag) throw new Error('RELEASE_TAG must include its v prefix');
  if (!sha || !runId) throw new Error('GITHUB_SHA and GITHUB_RUN_ID are required');
  const api = apiClient({ repository, token, fetch, baseURL });
  const invokeGh = gh ?? ((args) => runGh(args, token));
  await localManifest(assetDir, true);
  const hashes = await payloadHashes(assetDir);
  if (await readFile(join(assetDir, 'SHA256SUMS'), 'utf8') !== checksumText(hashes)) {
    throw new Error('SHA256SUMS does not match release payload bytes');
  }
  hashes.set('SHA256SUMS', await hashFile(join(assetDir, 'SHA256SUMS')));
  if (await api.tagCommit(tag) !== sha) throw new Error('Reserved release tag does not point to GITHUB_SHA');
  const marker = `<!-- agents-dashboard-release sha=${sha} -->`;
  let release = await api.release(tag);
  if (release && !release.draft) {
    if (release.prerelease) throw new Error('Existing release is not a stable release');
    validateUploadedAssets(await api.assets(release));
    return { tag, published: true };
  }
  if (release) {
    const markers = release.body?.match(/<!-- agents-dashboard-release sha=[^\r\n]*? -->/g) ?? [];
    if (markers.length !== 1 || markers[0] !== marker) throw new Error('Refusing to replace a draft not owned by this source SHA');
    await invokeGh(['release', 'delete', tag, '--yes', '--repo', repository]);
  }
  const instructions = [
    marker,
    `Source commit: ${sha}`,
    `Workflow: https://github.com/${repository}/actions/runs/${runId}`,
    '',
    'Installation:',
    '- macOS: extract the ZIP and copy Agents Dashboard.app to a writable local folder before running.',
    '- Windows: the installer installs Agents Dashboard.exe per-user and bootstraps WebView2; the standalone EXE is also available.',
    '- Linux: extract the portable tarball and run Agents Dashboard; GTK 4.14+ and WebKitGTK 6.0 runtime libraries are required.',
    '- These builds have no paid signing certificates: macOS is ad-hoc signed, not notarized; Windows is not Authenticode signed. Gatekeeper or SmartScreen may require first-install approval.',
    '- SHA256SUMS verifies download integrity; it is not an independent code signature.',
  ].join('\n');
  // Keep the creation response: pending-tag/list indexes need not expose a new draft.
  release = (await api.request('releases', {
    method: 'POST', body: {
      tag_name: tag, target_commitish: sha, name: tag, body: instructions,
      draft: true, prerelease: false, generate_release_notes: true,
    },
  })).data;
  if (!Number.isSafeInteger(release?.id) || release.id <= 0 || !release.draft || release.tag_name !== tag || !release.body?.includes(marker)) {
    throw new Error('GitHub did not return the expected owned draft release');
  }
  const draftId = release.id;
  await invokeGh(['release', 'upload', tag, ...RELEASE_ASSETS.map((name) => resolve(assetDir, name)), '--repo', repository]);
  release = (await api.request(`releases/${draftId}`)).data;
  if (!release?.draft || release.id !== draftId || release.tag_name !== tag || !release.body?.includes(marker)) {
    throw new Error('Owned release must remain the same draft during upload verification');
  }
  await verifyUploaded(api, release, hashes);
  const latest = await makeLatest(api, sha);
  // Recheck the immutable ref immediately before the single public transition.
  if (await api.tagCommit(tag) !== sha) throw new Error('Reserved release tag changed before publication');
  const { data: published } = await api.request(`releases/${release.id}`, {
    method: 'PATCH', body: { draft: false, prerelease: false, make_latest: latest },
  });
  if (!published || published.draft || published.prerelease) throw new Error('GitHub did not publish a stable release');
  return { tag, published: true };
}

function requiredEnv(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}

async function cli() {
  const [command, ...args] = process.argv.slice(2);
  if (command === 'reserve' && args.length === 0) {
    const output = requiredEnv('GITHUB_OUTPUT');
    const result = await reserve({ repository: requiredEnv('GITHUB_REPOSITORY'), sha: requiredEnv('GITHUB_SHA'), token: requiredEnv('GH_TOKEN') });
    await appendFile(output, `tag=${result.tag}\npublished=${result.published}\n`);
  } else if (command === 'metadata' && args.length === 2) {
    await metadata(args[0], args[1]);
  } else if (command === 'checksums' && args.length === 1) {
    await checksums(args[0]);
  } else if (command === 'publish' && args.length === 1) {
    await publish({ repository: requiredEnv('GITHUB_REPOSITORY'), sha: requiredEnv('GITHUB_SHA'),
      token: requiredEnv('GH_TOKEN'), tag: requiredEnv('RELEASE_TAG'), runId: requiredEnv('GITHUB_RUN_ID'), assetDir: args[0] });
  } else {
    throw new Error('Usage: release.mjs reserve | metadata <tag> <output-dir> | checksums <asset-dir> | publish <asset-dir>');
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  cli().catch((error) => {
    const message = String(error.message);
    console.error(process.env.GH_TOKEN ? message.replaceAll(process.env.GH_TOKEN, '[REDACTED]') : message);
    process.exitCode = 1;
  });
}
