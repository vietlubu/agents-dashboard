import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { createServer } from 'node:http';
import { mkdir, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { basename, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { allocateTag, checksums, datePrefix, metadata, parseVersion, publish, reserve } from './release.mjs';

const REPOSITORY = 'vietlubu/agents-dashboard';
const SHA = 'a'.repeat(40);
const OTHER_SHA = 'b'.repeat(40);
const TAG = 'v26.10.01.001';
const NOW = new Date('2026-09-30T17:00:00Z');
const PAYLOADS = [
  'agents-dashboard-darwin-arm64.zip',
  'agents-dashboard-windows-amd64.exe',
  'agents-dashboard-windows-amd64-installer.exe',
  'agents-dashboard-linux-amd64.tar.gz',
  'agents-dashboard-linux-arm64.tar.gz',
  'agents-dashboard-server-darwin-arm64.tar.gz',
  'agents-dashboard-server-windows-amd64.zip',
  'agents-dashboard-server-linux-amd64.tar.gz',
  'agents-dashboard-server-linux-arm64.tar.gz',
].sort();
const ALL_ASSETS = [...PAYLOADS, 'SHA256SUMS'].sort();
const digest = (bytes) => createHash('sha256').update(bytes).digest('hex');
const marker = (sha = SHA) => `<!-- agents-dashboard-release sha=${sha} -->`;
const repositoryRoot = dirname(dirname(fileURLToPath(import.meta.url)));

async function temporaryDirectory(t) {
  const path = await mkdtemp(join(tmpdir(), 'agents-dashboard-release-'));
  t.after(() => rm(path, { recursive: true, force: true }));
  return path;
}

async function assetDirectory(t) {
  const directory = await temporaryDirectory(t);
  for (const name of PAYLOADS) await writeFile(join(directory, name), `payload:${name}\n`);
  await checksums(directory);
  return directory;
}

// The REST fixture owns refs/releases. The injected CLI mutates the same state,
// so publication checks consume uploaded bytes rather than mock return values.
async function githubFixture(t, options = {}) {
  const refs = new Map();
  const tagObjects = new Map();
  const releases = new Map();
  const requests = [];
  const ghCalls = [];
  const errors = [];
  let nextID = 1;
  const state = { latestTag: null, compareStatus: 'ahead', createRef: null, upload: null, assetDownloadStatus: 200 };
  let baseURL;

  function addTag(tag, sha = SHA, annotated = false) {
    let object = { type: 'commit', sha };
    if (annotated) {
      const inner = `annotated-inner-${tag}`;
      const outer = `annotated-outer-${tag}`;
      tagObjects.set(inner, { object });
      tagObjects.set(outer, { object: { type: 'tag', sha: inner } });
      object = { type: 'tag', sha: outer };
    }
    refs.set(tag, { ref: `refs/tags/${tag}`, object });
  }

  function addAsset(release, name, bytes, overrides = {}) {
    const asset = {
      id: nextID++, name, state: 'uploaded', size: bytes.length,
      digest: `sha256:${digest(bytes)}`, bytes: Buffer.from(bytes), ...overrides,
    };
    asset.url = `${baseURL}/repos/${REPOSITORY}/releases/assets/${asset.id}`;
    asset.browser_download_url = `${baseURL}/download/${asset.id}`;
    release.assets.push(asset);
    return asset;
  }

  function addRelease({ tag = TAG, draft = false, body = marker(), names = ALL_ASSETS } = {}) {
    const release = {
      id: nextID++, tag_name: tag, name: tag, draft, prerelease: false, body,
      target_commitish: SHA, assets: [], html_url: `https://github.com/${REPOSITORY}/releases/tag/${tag}`,
    };
    releases.set(tag, release);
    for (const name of names) addAsset(release, name, Buffer.from(`old:${name}`));
    return release;
  }

  const publicAsset = ({ bytes, ...asset }) => asset;
  const publicRelease = (release) => ({ ...release, assets: release.assets.map(publicAsset) });
  const server = createServer(async (req, res) => {
    try {
      const url = new URL(req.url, baseURL);
      const parts = [];
      for await (const chunk of req) parts.push(chunk);
      const body = parts.length ? JSON.parse(Buffer.concat(parts).toString()) : null;
      requests.push({ method: req.method, path: url.pathname, search: url.search, body, accept: req.headers.accept, authorization: req.headers.authorization });
      const send = (status, value) => {
        res.writeHead(status, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify(value));
      };
      const endpoint = url.pathname.replace(`/repos/${REPOSITORY}`, '');
      const page = (values, size = options.pageSize ?? 100) => {
        const number = Number(url.searchParams.get('page') ?? 1);
        if (number * size < values.length) {
          const next = new URL(url);
          next.searchParams.set('page', String(number + 1));
          res.setHeader('Link', `<${next}>; rel="next"`);
        }
        return values.slice((number - 1) * size, number * size);
      };
      if (req.method === 'GET' && endpoint === '/git/matching-refs/tags/v') {
        return send(200, page([...refs.values()].filter((ref) => ref.ref.startsWith('refs/tags/v'))));
      }
      if (req.method === 'GET' && endpoint.startsWith('/git/ref/tags/')) {
        const ref = refs.get(decodeURIComponent(endpoint.slice('/git/ref/tags/'.length)));
        return send(ref ? 200 : 404, ref ?? { message: 'Not Found' });
      }
      if (req.method === 'GET' && endpoint.startsWith('/git/tags/')) {
        const object = tagObjects.get(endpoint.slice('/git/tags/'.length));
        return send(object ? 200 : 404, object ?? { message: 'Not Found' });
      }
      if (req.method === 'POST' && endpoint === '/git/refs') {
        const tag = body.ref.replace('refs/tags/', '');
        const failure = state.createRef?.(tag, body);
        if (failure) return send(failure.status, { message: failure.message });
        if (refs.has(tag)) return send(422, { message: 'Reference already exists' });
        addTag(tag, body.sha);
        return send(201, refs.get(tag));
      }
      if (req.method === 'GET' && endpoint === '/releases/latest') {
        const release = releases.get(state.latestTag);
        return send(release ? 200 : 404, release ? publicRelease(release) : { message: 'Not Found' });
      }
      if (req.method === 'GET' && endpoint.startsWith('/releases/tags/')) {
        const release = releases.get(decodeURIComponent(endpoint.slice('/releases/tags/'.length)));
        return send(release ? 200 : 404, release ? publicRelease(release) : { message: 'Not Found' });
      }
      const assetMatch = endpoint.match(/^\/releases\/assets\/(\d+)$/) ?? url.pathname.match(/^\/download\/(\d+)$/);
      if (req.method === 'GET' && assetMatch) {
        const asset = [...releases.values()].flatMap((release) => release.assets).find((item) => item.id === Number(assetMatch[1]));
        if (!asset) return send(404, { message: 'Not Found' });
        if (state.assetDownloadStatus !== 200) return send(state.assetDownloadStatus, { message: 'Download failed' });
        res.writeHead(200, { 'Content-Type': 'application/octet-stream' });
        return res.end(asset.bytes);
      }
      const releaseMatch = endpoint.match(/^\/releases\/(\d+)(\/assets)?$/);
      if (releaseMatch) {
        const release = [...releases.values()].find((item) => item.id === Number(releaseMatch[1]));
        if (!release) return send(404, { message: 'Not Found' });
        if (req.method === 'GET' && releaseMatch[2]) return send(200, page(release.assets.map(publicAsset)));
        if (req.method === 'GET') return send(200, publicRelease(release));
        if (req.method === 'PATCH' && !releaseMatch[2]) {
          Object.assign(release, body);
          if (body.make_latest === 'true') state.latestTag = release.tag_name;
          return send(200, publicRelease(release));
        }
      }
      if (req.method === 'GET' && endpoint.startsWith('/compare/')) return send(200, { status: state.compareStatus });
      throw new Error(`Unhandled fixture request: ${req.method} ${url.pathname}`);
    } catch (error) {
      errors.push(error.message);
      res.writeHead(500, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ message: error.message }));
    }
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  baseURL = `http://127.0.0.1:${server.address().port}`;
  t.after(async () => {
    await new Promise((resolve, reject) => {
      server.close((error) => error ? reject(error) : resolve());
      server.closeAllConnections();
    });
    assert.deepEqual(errors, [], 'all HTTP requests must be handled by the fixture');
  });

  async function gh(args) {
    ghCalls.push([...args]);
    assert.equal(args[0], 'release');
    const [command, tag] = args.slice(1, 3);
    if (command === 'create') {
      assert.ok(args.includes('--draft'), 'create must not expose a partial release');
      assert.ok(args.includes('--verify-tag'), 'create must reuse the reserved ref');
      assert.equal(args[args.indexOf('--target') + 1], SHA);
      assert.ok(refs.has(tag));
      assert.ok(!releases.has(tag));
      const notesIndex = args.indexOf('--notes');
      const notes = notesIndex === -1 ? '' : args[notesIndex + 1];
      addRelease({ tag, draft: true, body: `${notes}\nGenerated release notes`, names: [] });
      return '';
    }
    if (command === 'delete') {
      assert.ok(!args.includes('--cleanup-tag'), 'draft cleanup must preserve the reserved ref');
      assert.equal(releases.get(tag)?.draft, true, 'only drafts can be removed');
      releases.delete(tag);
      return '';
    }
    if (command === 'upload') {
      assert.ok(!args.includes('--clobber'), 'release assets must never be overwritten');
      const release = releases.get(tag);
      assert.equal(release?.draft, true, 'all uploads must happen while private');
      for (let index = 3; index < args.length; index++) {
        if (args[index] === '--repo' || args[index] === '-R') { index++; continue; }
        assert.ok(!args[index].startsWith('-'), `unexpected upload option ${args[index]}`);
        const path = args[index];
        const name = basename(path);
        assert.ok(!release.assets.some((asset) => asset.name === name), 'duplicate assets cannot replace earlier bytes');
        const asset = addAsset(release, name, await readFile(path));
        state.upload?.(asset);
      }
      return '';
    }
    throw new Error(`Unhandled gh command: ${args.join(' ')}`);
  }

  const common = { repository: REPOSITORY, sha: SHA, token: 'fixture-token', baseURL };
  return {
    refs, releases, requests, ghCalls, state, addTag, addRelease, addAsset,
    reserve: (extra = {}) => reserve({ ...common, clock: () => NOW, ...extra }),
    publish: (assetDir, extra = {}) => publish({ ...common, tag: TAG, runId: '1234', assetDir, gh, ...extra }),
  };
}

function publishingRequests(fixture) {
  return fixture.requests.filter((request) => request.method === 'PATCH' && request.body?.draft === false);
}

test('release grammar preserves the full tag and derives native numeric versions', () => {
  assert.deepEqual(parseVersion(TAG), {
    tag: TAG, date: '26.10.01', year: 26, month: 10, day: 1, serial: 1,
    numeric: '26.10.1.1', short: '26.10.1', bundle: '26.1001.1',
  });
  assert.equal(parseVersion('v24.02.29.999').numeric, '24.2.29.999');
  assert.deepEqual(parseVersion(TAG.slice(1)), parseVersion(TAG));
  for (const invalid of ['dev', '', 'v26.02.29.001', 'v26.04.31.001', 'v26.13.01.001', 'v26.00.01.001', 'v26.10.00.001', 'v26.10.01.000', 'v26.10.01.1000', 'v26.1.01.001', 'v2026.10.01.001', 'vv26.10.01.001', 'v26.10.01.001\n']) {
    assert.throws(() => parseVersion(invalid), undefined, invalid);
  }
});

test('Vietnam midnight changes the date exactly at 17:00 UTC', () => {
  assert.equal(datePrefix(new Date('2026-09-30T16:59:59.999Z')), '26.09.30');
  assert.equal(datePrefix(NOW), '26.10.01');
  assert.equal(allocateTag([], NOW), TAG);
  assert.equal(allocateTag(['v26.10.01.001', 'v26.10.01.009', 'v26.09.30.999', 'v26.10.02.100'], NOW), 'v26.10.01.010');
  assert.throws(() => allocateTag(['v26.10.01.999'], NOW));
});

test('reserve creates a lightweight ref at the exact workflow SHA', async (t) => {
  const fixture = await githubFixture(t);
  const result = await fixture.reserve();
  assert.deepEqual(result, { tag: TAG, published: false });
  assert.deepEqual(fixture.refs.get(TAG).object, { type: 'commit', sha: SHA });
  assert.equal(fixture.releases.size, 0);
});

test('reserve scans all ref pages and resolves annotated tags to the commit', async (t) => {
  const fixture = await githubFixture(t, { pageSize: 1 });
  fixture.addTag('v26.10.01.001', OTHER_SHA);
  fixture.addTag('v1.2.3', OTHER_SHA);
  fixture.addTag('v26.10.01.009', OTHER_SHA, true);
  assert.equal((await fixture.reserve()).tag, 'v26.10.01.010');
  assert.ok(fixture.requests.some((request) => request.search.includes('page=3')));
  const annotated = await githubFixture(t, { pageSize: 1 });
  annotated.addTag('v26.09.29.001', SHA, true);
  assert.deepEqual(await annotated.reserve(), { tag: 'v26.09.29.001', published: false });
  assert.equal(annotated.requests.filter((request) => request.method === 'POST').length, 0);
});

test('same-SHA rerun reuses its old tag across Vietnam dates', async (t) => {
  const fixture = await githubFixture(t);
  fixture.addTag('v26.09.30.007');
  assert.deepEqual(await fixture.reserve({ clock: () => new Date('2026-10-02T18:00:00Z') }), { tag: 'v26.09.30.007', published: false });
  assert.equal(fixture.requests.filter((request) => request.method === 'POST').length, 0);
});

test('multiple release tags for the same SHA are ambiguous and never mutated', async (t) => {
  const fixture = await githubFixture(t);
  fixture.addTag(TAG);
  fixture.addTag('v26.10.01.002');
  await assert.rejects(fixture.reserve());
  assert.equal(fixture.requests.filter((request) => request.method !== 'GET').length, 0);
});

for (const tag of ['v26.02.29.001', 'v26.10.01.000']) {
  test(`reserve rejects malformed calver ref ${tag}`, async (t) => {
    const fixture = await githubFixture(t);
    fixture.addTag(tag, OTHER_SHA);
    await assert.rejects(fixture.reserve());
    assert.equal(fixture.requests.filter((request) => request.method !== 'GET').length, 0);
  });
}

test('daily serial exhaustion cannot create a wider version', async (t) => {
  const fixture = await githubFixture(t);
  fixture.addTag('v26.10.01.999', OTHER_SHA);
  await assert.rejects(fixture.reserve());
  assert.equal(fixture.requests.filter((request) => request.method === 'POST').length, 0);
});

for (const status of [409, 422]) {
  test(`a real ${status} ref collision retries allocation without moving refs or changing date`, async (t) => {
    const fixture = await githubFixture(t);
    let attempts = 0;
    let clockCalls = 0;
    fixture.state.createRef = (tag) => {
      if (attempts++ === 0) {
        fixture.addTag(tag, OTHER_SHA);
        return { status, message: 'Reference already exists' };
      }
    };
    const result = await fixture.reserve({ clock: () => ++clockCalls === 1 ? NOW : new Date('2026-10-01T17:00:00Z') });
    assert.deepEqual(result, { tag: 'v26.10.01.002', published: false });
    assert.equal(fixture.refs.get(TAG).object.sha, OTHER_SHA);
    assert.equal(fixture.refs.get(result.tag).object.sha, SHA);
    assert.equal(attempts, 2);
    assert.equal(clockCalls, 1);
  });
}

for (const status of [403, 422]) {
  test(`permission/validation error ${status} without a collision is not retried`, async (t) => {
    const fixture = await githubFixture(t);
    fixture.state.createRef = () => ({ status, message: 'Repository rules prohibit this ref' });
    await assert.rejects(fixture.reserve());
    assert.equal(fixture.requests.filter((request) => request.method === 'POST').length, 1);
    assert.equal(fixture.refs.size, 0);
  });
}

async function metadataRoot(t) {
  const root = await temporaryDirectory(t);
  const templates = new Map();
  for (const relative of ['build/darwin/Info.plist', 'build/windows/info.json', 'build/windows/wails.exe.manifest']) {
    const contents = await readFile(join(repositoryRoot, relative), 'utf8');
    templates.set(relative, contents);
    await mkdir(dirname(join(root, relative)), { recursive: true });
    await writeFile(join(root, relative), contents);
  }
  return { root, templates, output: join(root, 'generated') };
}

test('metadata stamps copies while preserving identities, dependencies and non-version keys', async (t) => {
  const { root, templates, output } = await metadataRoot(t);
  await metadata(TAG, output, { root });
  const plist = await readFile(join(output, 'darwin/Info.plist'), 'utf8');
  let expectedPlist = templates.get('build/darwin/Info.plist');
  for (const [key, value] of [['CFBundleShortVersionString', '26.10.1'], ['CFBundleVersion', '26.1001.1']]) {
    expectedPlist = expectedPlist.replace(new RegExp(`(<key>${key}</key>\\s*<string>)[^<]*(</string>)`), (_, before, after) => `${before}${value}${after}`);
  }
  assert.equal(plist, expectedPlist);
  const info = JSON.parse(await readFile(join(output, 'windows/info.json'), 'utf8'));
  const expectedInfo = JSON.parse(templates.get('build/windows/info.json'));
  expectedInfo.fixed.file_version = '26.10.1.1';
  expectedInfo.info['0000'].ProductVersion = TAG;
  expectedInfo.info['0000'].FileVersion = TAG;
  assert.deepEqual(info, expectedInfo);
  const manifest = await readFile(join(output, 'windows/wails.exe.manifest'), 'utf8');
  assert.equal(manifest, templates.get('build/windows/wails.exe.manifest').replace('version="0.1.0"', 'version="26.10.1.1"'));
  for (const [relative, contents] of templates) assert.equal(await readFile(join(root, relative), 'utf8'), contents);
});

test('dev metadata is a no-op and malformed release versions cannot stamp files', async (t) => {
  const root = await temporaryDirectory(t);
  const output = join(root, 'generated');
  await metadata('dev', output, { root });
  assert.deepEqual(await readdir(root), []);
  await assert.rejects(metadata('v26.02.29.001', output, { root }));
  assert.deepEqual(await readdir(root), []);
});

const anchorMutations = [
  ['missing short plist version', 'build/darwin/Info.plist', (text) => text.replace('<key>CFBundleShortVersionString</key>', '<key>OtherVersion</key>')],
  ['duplicate plist version', 'build/darwin/Info.plist', (text) => text.replace('</dict>', '<key>CFBundleVersion</key><string>1</string></dict>')],
  ['missing bundle plist version', 'build/darwin/Info.plist', (text) => text.replace('<key>CFBundleVersion</key>', '<key>OtherVersion</key>')],
  ['duplicate short plist version', 'build/darwin/Info.plist', (text) => text.replace('</dict>', '<key>CFBundleShortVersionString</key><string>1</string></dict>')],
  ['missing numeric Windows version', 'build/windows/info.json', (text) => { const value = JSON.parse(text); delete value.fixed.file_version; return JSON.stringify(value); }],
  ['missing Windows product version', 'build/windows/info.json', (text) => { const value = JSON.parse(text); delete value.info['0000'].ProductVersion; return JSON.stringify(value); }],
  ['duplicate Windows product version', 'build/windows/info.json', (text) => text.replace('"ProductVersion":', '"ProductVersion": "1", "ProductVersion":')],
  ['duplicate numeric Windows version', 'build/windows/info.json', (text) => text.replace('"file_version":', '"file_version": "1", "file_version":')],
  ['missing outer manifest identity', 'build/windows/wails.exe.manifest', (text) => text.replace(/^\s*<assemblyIdentity[^\n]+\n/m, '')],
  ['duplicate outer manifest identity', 'build/windows/wails.exe.manifest', (text) => text.replace(/(\s*<assemblyIdentity[^\n]+\n)/, '$1$1')],
  ['missing outer manifest version', 'build/windows/wails.exe.manifest', (text) => text.replace(' version="0.1.0"', '')],
];
for (const [name, relative, mutate] of anchorMutations) {
  test(`metadata fails with ${name} instead of silently shipping an unstamped build`, async (t) => {
    const { root, templates, output } = await metadataRoot(t);
    await writeFile(join(root, relative), mutate(templates.get(relative)));
    await assert.rejects(metadata(TAG, output, { root }));
  });
}

test('checksums hashes exactly the sorted payloads and never hashes its own prior output', async (t) => {
  const directory = await assetDirectory(t);
  const expected = (await Promise.all(PAYLOADS.map(async (name) => `${digest(await readFile(join(directory, name)))}  ${name}\n`))).join('');
  assert.equal(await readFile(join(directory, 'SHA256SUMS'), 'utf8'), expected);
  await writeFile(join(directory, 'SHA256SUMS'), 'obsolete checksum output');
  await checksums(directory);
  assert.equal(await readFile(join(directory, 'SHA256SUMS'), 'utf8'), expected);
});

for (const problem of ['missing', 'extra', 'directory']) {
  test(`checksums rejects an ${problem} payload manifest`, async (t) => {
    const directory = await assetDirectory(t);
    if (problem === 'missing') await rm(join(directory, PAYLOADS[0]));
    if (problem === 'extra') await writeFile(join(directory, 'unexpected.zip'), 'extra');
    if (problem === 'directory') { await rm(join(directory, PAYLOADS[0])); await mkdir(join(directory, PAYLOADS[0])); }
    await assert.rejects(checksums(directory));
  });
}

for (const problem of ['missing', 'extra', 'changed bytes', 'invalid checksum', 'duplicate checksum', 'missing checksum']) {
  test(`publish rejects ${problem} before making a release public`, async (t) => {
    const fixture = await githubFixture(t);
    fixture.addTag(TAG);
    const directory = await assetDirectory(t);
    if (problem === 'missing') await rm(join(directory, PAYLOADS[0]));
    if (problem === 'extra') await writeFile(join(directory, 'extra.zip'), 'extra');
    if (problem === 'changed bytes') await writeFile(join(directory, PAYLOADS[0]), 'tampered after checksums');
    if (problem === 'invalid checksum') await writeFile(join(directory, 'SHA256SUMS'), 'not a checksum\n');
    if (problem === 'duplicate checksum') {
      const contents = await readFile(join(directory, 'SHA256SUMS'), 'utf8');
      await writeFile(join(directory, 'SHA256SUMS'), contents + contents.split('\n')[0] + '\n');
    }
    if (problem === 'missing checksum') await rm(join(directory, 'SHA256SUMS'));
    await assert.rejects(fixture.publish(directory));
    assert.deepEqual(publishingRequests(fixture), []);
    assert.deepEqual(fixture.ghCalls, [], 'local artifacts must be validated before remote mutation');
  });
}

test('publish rejects a reserved ref pointing at another SHA without changing it', async (t) => {
  const fixture = await githubFixture(t);
  fixture.addTag(TAG, OTHER_SHA);
  await assert.rejects(fixture.publish(await assetDirectory(t)));
  assert.deepEqual(fixture.ghCalls, []);
  assert.equal(fixture.refs.get(TAG).object.sha, OTHER_SHA);
});

test('complete published rerun returns success without CLI mutation or latest promotion', async (t) => {
  const fixture = await githubFixture(t, { pageSize: 3 });
  fixture.addTag(TAG, SHA, true);
  fixture.addRelease();
  assert.deepEqual(await fixture.reserve(), { tag: TAG, published: true });
  await fixture.publish(await assetDirectory(t));
  assert.deepEqual(fixture.ghCalls, []);
  assert.deepEqual(fixture.requests.filter((request) => request.method !== 'GET'), []);
  assert.ok(fixture.requests.some((request) => request.path.endsWith('/assets') && request.search.includes('page=4')));
});

for (const missing of [PAYLOADS[0], 'SHA256SUMS']) {
  test(`published release missing ${missing} fails reserve and publish without repair`, async (t) => {
    const fixture = await githubFixture(t);
    fixture.addTag(TAG);
    const release = fixture.addRelease({ names: ALL_ASSETS.filter((name) => name !== missing) });
    await assert.rejects(fixture.reserve());
    await assert.rejects(fixture.publish(await assetDirectory(t)));
    assert.deepEqual(fixture.ghCalls, []);
    assert.equal(release.draft, false);
    assert.deepEqual(fixture.requests.filter((request) => request.method !== 'GET'), []);
  });
}

test('new release stays draft through byte verification and then becomes latest atomically', async (t) => {
  const fixture = await githubFixture(t, { pageSize: 3 });
  fixture.addTag(TAG);
  const directory = await assetDirectory(t);
  await fixture.publish(directory);
  const release = fixture.releases.get(TAG);
  assert.equal(release.draft, false);
  assert.equal(release.prerelease, false);
  assert.equal(fixture.state.latestTag, TAG);
  assert.deepEqual(release.assets.map((asset) => asset.name).sort(), ALL_ASSETS);
  for (const asset of release.assets) assert.deepEqual(asset.bytes, await readFile(join(directory, asset.name)));
  assert.ok(release.body.includes('Generated release notes'));
  assert.ok(release.body.includes(marker()));
  assert.ok(release.body.includes(`https://github.com/${REPOSITORY}/actions/runs/1234`));
  assert.equal(publishingRequests(fixture).length, 1);
});

test('incomplete owned draft is replaced wholly by the current build, preserving its tag', async (t) => {
  const fixture = await githubFixture(t);
  fixture.addTag(TAG);
  const stale = fixture.addRelease({ draft: true, body: `${marker()}\nhttps://github.com/${REPOSITORY}/actions/runs/999`, names: [PAYLOADS[0], 'SHA256SUMS'] });
  const directory = await assetDirectory(t);
  await fixture.publish(directory);
  const rebuilt = fixture.releases.get(TAG);
  assert.notEqual(rebuilt.id, stale.id);
  assert.deepEqual(fixture.ghCalls.map((args) => args[1]), ['delete', 'create', 'upload']);
  assert.equal(fixture.refs.get(TAG).object.sha, SHA);
  assert.equal(rebuilt.draft, false);
  assert.ok(rebuilt.body.includes('/actions/runs/1234'));
  for (const asset of rebuilt.assets) assert.deepEqual(asset.bytes, await readFile(join(directory, asset.name)));
});

for (const body of ['A user-managed draft', marker(OTHER_SHA), `<!-- agents-dashboard-release sha=${SHA} extra=yes -->`, `${marker()}\n${marker()}`]) {
  test(`an unowned draft cannot be deleted or published (${body})`, async (t) => {
    const fixture = await githubFixture(t);
    fixture.addTag(TAG);
    const release = fixture.addRelease({ draft: true, body, names: [PAYLOADS[0]] });
    await assert.rejects(fixture.publish(await assetDirectory(t)));
    assert.deepEqual(fixture.ghCalls, []);
    assert.equal(fixture.releases.get(TAG).id, release.id);
    assert.equal(release.draft, true);
    assert.deepEqual(publishingRequests(fixture), []);
  });
}

for (const problem of ['wrong digest', 'null digest corrupted bytes', 'download failure', 'not uploaded', 'missing asset', 'extra asset', 'duplicate asset']) {
  test(`uploaded ${problem} leaves the release private`, async (t) => {
    const fixture = await githubFixture(t);
    fixture.addTag(TAG);
    fixture.state.upload = (asset) => {
      if (asset.name !== PAYLOADS[0]) return;
      if (problem === 'wrong digest') asset.digest = `sha256:${'0'.repeat(64)}`;
      if (problem === 'null digest corrupted bytes') { asset.digest = null; asset.bytes = Buffer.from('corrupt remote bytes'); }
      if (problem === 'download failure') { asset.digest = null; fixture.state.assetDownloadStatus = 403; }
      if (problem === 'not uploaded') asset.state = 'starter';
      if (problem === 'missing asset') fixture.releases.get(TAG).assets.splice(0, 1);
      if (problem === 'extra asset') fixture.addAsset(fixture.releases.get(TAG), 'unexpected.zip', Buffer.from('extra'));
      if (problem === 'duplicate asset') fixture.addAsset(fixture.releases.get(TAG), asset.name, asset.bytes);
    };
    await assert.rejects(fixture.publish(await assetDirectory(t)));
    assert.equal(fixture.releases.get(TAG).draft, true);
    assert.deepEqual(publishingRequests(fixture), []);
    assert.equal(fixture.state.latestTag, null);
  });
}

test('nullable GitHub digests are verified by authenticated downloads of every asset', async (t) => {
  const fixture = await githubFixture(t);
  fixture.addTag(TAG);
  fixture.state.upload = (asset) => { asset.digest = null; };
  await fixture.publish(await assetDirectory(t));
  assert.equal(fixture.releases.get(TAG).draft, false);
  const downloads = fixture.requests.filter((request) => request.path.includes('/releases/assets/'));
  assert.equal(downloads.length, ALL_ASSETS.length);
  assert.ok(downloads.every((request) => request.accept === 'application/octet-stream'));
  assert.ok(downloads.every((request) => request.authorization === 'Bearer fixture-token'));
});

for (const [ancestry, latest] of [['ahead', true], ['identical', true], ['behind', false], ['diverged', false]]) {
  test(`${ancestry} ancestry ${latest ? 'can advance' : 'cannot roll back'} the latest channel`, async (t) => {
    const fixture = await githubFixture(t);
    const latestTag = 'v26.09.30.099';
    fixture.addTag(latestTag, OTHER_SHA, true);
    fixture.addRelease({ tag: latestTag });
    fixture.state.latestTag = latestTag;
    fixture.state.compareStatus = ancestry;
    fixture.addTag(TAG);
    await fixture.publish(await assetDirectory(t));
    assert.equal(fixture.releases.get(TAG).draft, false);
    assert.equal(publishingRequests(fixture)[0].body.make_latest, String(latest));
    assert.equal(fixture.state.latestTag, latest ? TAG : latestTag);
    assert.ok(fixture.requests.some((request) => request.path.endsWith(`/compare/${OTHER_SHA}...${SHA}`)));
    const callsBefore = fixture.ghCalls.length;
    const mutationsBefore = fixture.requests.filter((request) => request.method !== 'GET').length;
    fixture.state.compareStatus = 'ahead';
    await fixture.publish(await assetDirectory(t));
    assert.equal(fixture.ghCalls.length, callsBefore);
    assert.equal(fixture.requests.filter((request) => request.method !== 'GET').length, mutationsBefore);
    assert.equal(fixture.state.latestTag, latest ? TAG : latestTag, 'rerun cannot promote a historical release');
  });
}

test('an interrupted CLI upload leaves an owned draft that a rerun can safely rebuild', async (t) => {
  const fixture = await githubFixture(t);
  fixture.addTag(TAG);
  const directory = await assetDirectory(t);
  fixture.state.upload = () => { throw new Error('Upload interrupted'); };
  await assert.rejects(fixture.publish(directory), /Upload interrupted/);
  const interrupted = fixture.releases.get(TAG);
  assert.equal(interrupted.draft, true);
  assert.ok(interrupted.body.includes(marker()));
  assert.deepEqual(publishingRequests(fixture), []);
  fixture.state.upload = null;
  await fixture.publish(directory, { runId: '5678' });
  const rebuilt = fixture.releases.get(TAG);
  assert.notEqual(rebuilt.id, interrupted.id);
  assert.equal(rebuilt.draft, false);
  assert.ok(rebuilt.body.includes('/actions/runs/5678'));
  assert.equal(fixture.refs.get(TAG).object.sha, SHA);
  assert.deepEqual(rebuilt.assets.map((asset) => asset.name).sort(), ALL_ASSETS);
  for (const asset of rebuilt.assets) assert.deepEqual(asset.bytes, await readFile(join(directory, asset.name)));
});

test('release metadata cannot overwrite its source templates', async (t) => {
  const { root, templates } = await metadataRoot(t);
  await assert.rejects(metadata(TAG, join(root, 'build'), { root }));
  await assert.rejects(metadata(TAG.slice(1), join(root, 'generated'), { root }));
  for (const [relative, contents] of templates) assert.equal(await readFile(join(root, relative), 'utf8'), contents);
});
