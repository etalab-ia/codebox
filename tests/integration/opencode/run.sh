#!/bin/sh
# Characterize the released v2 API, not the removed v1 merged debug output.
# Network qualification is opt-in: OPENCODE_INTEGRATION_NETWORK=1 + ALBERT_API_KEY.
set -eu
HERE="$(cd "$(dirname "$0")" && pwd)"
LAB="$(mktemp -d /tmp/opencode-v2-integration-XXXXXX)"
trap 'rm -rf "$LAB"' EXIT
# Resolve caches before isolating HOME (Go's module cache contains read-only files).
export GOMODCACHE="${GOMODCACHE:-$(go env GOMODCACHE)}"
export GOCACHE="${GOCACHE:-$(go env GOCACHE)}"
export HOME="$LAB/home"
export XDG_CONFIG_HOME="$HOME/.config" XDG_DATA_HOME="$HOME/.local/share"
export XDG_CACHE_HOME="$HOME/.cache" XDG_STATE_HOME="$HOME/.local/state"
mkdir -p "$HOME" "$LAB/managed-configs"
PIN="$(node -e 'const p=require(process.argv[1]); console.log(p.npm_package+"@"+p.version)' "$HERE/pin.json")"
if [ -n "${OPENCODE_INTEGRATION_BINARY:-}" ]; then
  printf 'use preinstalled binary %s (version checked below)\n' "$OPENCODE_INTEGRATION_BINARY"
else
  printf 'install %s (isolated under %s)\n' "$PIN" "$LAB"
  npm install --prefix "$LAB/runner" --no-audit --no-fund "$PIN" >"$LAB/npm.log" 2>&1 || { cat "$LAB/npm.log"; exit 1; }
fi
(cd "$HERE/../../.." && go run ./tests/integration/opencode/configcompose "$LAB/managed-configs")
# Node owns every spawned server and reaps it in finally, including assertion failures.
# CLI probes always use --standalone or --server, never the managed host service.
node - "$HERE" "$LAB" <<'NODE'
const fs = require('node:fs');
const path = require('node:path');
const net = require('node:net');
const {spawn, spawnSync} = require('node:child_process');
const assert = require('node:assert/strict');
const [here, lab] = process.argv.slice(2);
const oc = process.env.OPENCODE_INTEGRATION_BINARY || path.join(lab, 'runner/node_modules/.bin/opencode');
const version = JSON.parse(fs.readFileSync(path.join(here, 'pin.json'))).version;
const project = path.join(lab, 'project');
fs.mkdirSync(project, {recursive:true});
const globalDir = path.join(process.env.XDG_CONFIG_HOME, 'opencode');
fs.mkdirSync(globalDir, {recursive:true});
// Do not inherit a caller's server, config override, password, or service lease.
const env = {...process.env};
for (const key of Object.keys(env)) if (key.startsWith('OPENCODE_')) delete env[key];
const synthetic = 'integration-synthetic-password-not-a-credential';
let passes = 0, failures = 0;
const children = new Set();
const sleep = ms => new Promise(r => setTimeout(r, ms));
function write(file, data) { fs.mkdirSync(path.dirname(file), {recursive:true}); fs.writeFileSync(file, typeof data === 'string' ? data : JSON.stringify(data)); }
function cli(args, extra = {}, cwd = project) {
  assert.ok(!args.some(arg=>String(arg).includes(synthetic)),'password must not enter CLI argv');
  const r = spawnSync(oc, args, {cwd, env:{...env,...extra}, encoding:'utf8', timeout:120000, maxBuffer:32*1024*1024});
  if (r.error || r.status !== 0) throw new Error(`CLI ${args.join(' ')} failed (${r.status}): ${r.error || r.stderr}`);
  return r.stdout.trim();
}
function apiProbe(route, extra = {}, cwd = project) { return JSON.parse(cli(['api','--standalone','GET',route], extra, cwd)); }
async function test(name, fn) {
  try { await fn(); passes++; console.log(`  ok  ${name}`); }
  catch(e) { failures++; console.error(`FAIL  ${name}: ${e.stack || e}`); }
}
function docs(entries) {
  assert.ok(Array.isArray(entries), '/api/config must return ordered sources, not a merged object');
  for (const e of entries) assert.ok(!e.diagnostics?.length, `config diagnostics: ${JSON.stringify(e)}`);
  return entries.filter(e => e.type === 'document');
}
// The API exposes normalized ordered documents. Compose only for precedence
// assertions; actual effective provider/model behavior is checked separately.
function merge(a,b) {
  for (const [k,v] of Object.entries(b)) a[k] = v && typeof v === 'object' && !Array.isArray(v) ? merge(a[k] && typeof a[k] === 'object' ? a[k] : {},v) : v;
  return a;
}
function effective(entries) { return docs(entries).reduce((a,e) => merge(a,e.info),{}); }
function selection(m) { return typeof m === 'string' ? m : `${m?.providerID}/${m?.model}`; }
async function freePort() {
  const s = net.createServer(); await new Promise(r => s.listen(0,'127.0.0.1',r));
  const port = s.address().port; await new Promise(r => s.close(r)); return port;
}
async function stop(child) {
  if (child.exitCode !== null || child.signalCode !== null) { children.delete(child); return; }
  const done = new Promise(r => child.once('exit',r)); child.kill('SIGTERM');
  const timer = setTimeout(() => child.kill('SIGKILL'),5000);
  await done; clearTimeout(timer); children.delete(child);
}
for (const [signal,code] of [['SIGINT',130],['SIGTERM',143]]) {
  process.once(signal,async () => { await Promise.all([...children].map(stop)); process.exit(code); });
}
async function server(extra, fn) {
  const port = await freePort(), url = `http://127.0.0.1:${port}`;
  const child = spawn(oc,['serve','--print-logs','--hostname','127.0.0.1','--port',String(port)],{cwd:project, env:{...env,...extra,OPENCODE_PASSWORD:synthetic},stdio:['ignore','pipe','pipe']});
  children.add(child); let log = ''; child.stdout.on('data', b => log += b); child.stderr.on('data', b => log += b);
  const request = async (route, {method='GET',body,password=synthetic} = {}) => {
    const u = new URL(route,url); u.searchParams.set('location[directory]',project);
    const headers = {'content-type':'application/json'};
    if (password !== null) headers.authorization = `Basic ${Buffer.from(`opencode:${password}`).toString('base64')}`;
    return fetch(u,{method,headers,body:body === undefined ? undefined : JSON.stringify(body), signal:AbortSignal.timeout(30000)});
  };
  const json = async (route, options) => { const r = await request(route,options); assert.equal(r.status,200,`${route}: ${await (r.status === 200 ? Promise.resolve('') : r.text())}`); return r.json(); };
  try {
    let ready = false;
    for (let n=0;n<200;n++) {
      if (child.exitCode !== null) throw new Error(`server exited: ${log}`);
      try { if ((await request('/api/info')).status === 200) { ready=true; break; } } catch {}
      await sleep(100);
    }
    assert.ok(ready,`server startup timed out: ${log}`);
    await fn({url,request,json,child,logs:() => log});
  } finally { await stop(child); }
}
(async () => {
try {
  await test('pinned npm CLI version and supported backend/resume flags', () => {
    assert.ok([version,`opencode v${version}`].includes(cli(['--version'])),'unexpected pinned CLI version');
    const help = cli(['--help']);
    for (const flag of ['--server','--standalone','--continue','--session']) assert.ok(help.includes(flag),flag);
    assert.ok(cli(['session','list','--help']).includes('--server'));
  });
  for (const file of fs.readdirSync(path.join(lab,'managed-configs')).filter(n=>n.endsWith('.json')).sort()) {
    await test(`C0 managed config accepted without diagnostics, metadata retained: ${file}`, async () => {
      const raw = fs.readFileSync(path.join(lab,'managed-configs',file),'utf8');
      const input = JSON.parse(raw);
      assert.ok(input.providers, 'managed providers must use native v2 providers (legacy provider loses metadata)');
      await server({OPENCODE_CONFIG_CONTENT:raw,ALBERT_API_KEY:'synthetic-validation-key',OPENAI_COMPATIBLE_API_KEY:'synthetic-validation-key'}, async ({json,logs}) => {
        const entries = await json('/api/config'), info = effective(entries);
        assert.equal(selection(info.model),selection(input.model));
        if (input.small_model) assert.equal(selection(info.agents.title.model),selection(input.small_model));
        if (input.mcp) {
          const expectedServers = input.mcp.servers || input.mcp;
          for (const [name, expected] of Object.entries(expectedServers)) {
            const actual = info.mcp?.servers?.[name];
            assert.ok(actual, `managed MCP ${name} was dropped during v2 normalization`);
            for (const key of ['type','command','url']) if (expected[key] !== undefined) assert.deepEqual(actual[key], expected[key], `managed MCP ${name}.${key}`);
            if (expected.enabled === true) assert.notEqual(actual.disabled, true, `managed MCP ${name} disabled`);
          }
        }
        const providers = (await json('/api/provider')).data;
        const models = (await json('/api/model')).data;
        assert.ok(!logs().includes('configuration normalization diagnostic'),`configuration diagnostics: ${logs()}`);
        assert.ok(Array.isArray(providers)); assert.ok(Array.isArray(models));
        for (const [id,p] of Object.entries(input.providers)) {
          assert.ok(providers.some(x=>x.id===id),`missing provider ${id}`);
          for (const [mid,m] of Object.entries(p.models || {})) {
            const normalized = info.providers[id].models[mid];
            const actual = models.find(x=>x.providerID===id && x.id===mid);
            assert.ok(actual,`missing effective model ${id}/${mid}`);
            for (const key of ['modelID','capabilities','body','limit']) if (m[key] !== undefined) {
              // Effective models may add defaults, but must retain every managed field.
              const subset = (want,got) => { for (const [k,v] of Object.entries(want)) { if (v && typeof v === 'object' && !Array.isArray(v)) subset(v,got?.[k] || {}); else assert.deepEqual(got?.[k],v,`${id}/${mid}.${key}.${k}`); } };
              if (m[key] && typeof m[key] === 'object' && !Array.isArray(m[key])) { subset(m[key],normalized[key]); subset(m[key],actual[key]); }
              else { assert.deepEqual(normalized[key],m[key]); assert.deepEqual(actual[key],m[key]); }
            }
          }
        }
      });
    });
  }
  write(path.join(globalDir,'opencode.json'),{model:'global/model',small_model:'global/small',permission:{bash:{'dangerous.*':'deny'}}});
  write(path.join(project,'opencode.json'),{model:'project/model'});
  await test('C1-C3 inline > project > global, normalized small model and permission survive', () => {
    const entries = apiProbe('/api/config',{OPENCODE_CONFIG_CONTENT:'{"model":"inline/model"}'});
    const info = effective(entries);
    assert.equal(selection(info.model),'inline/model');
    assert.equal(selection(info.agents.title.model),'global/small');
    assert.ok(info.permissions.some(x=>x.action==='shell' && x.resource==='dangerous.*' && x.effect==='deny'));
    assert.equal(selection(effective(apiProbe('/api/config')).model),'project/model');
  });
  await test('C3 project small model overrides global and survives unrelated inline selection', () => {
    write(path.join(project,'opencode.json'),{model:'project/model',small_model:'project/small'});
    const info = effective(apiProbe('/api/config',{OPENCODE_CONFIG_CONTENT:'{"model":"inline/model"}'}));
    assert.equal(selection(info.model),'inline/model');
    assert.equal(selection(info.agents.title.model),'project/small');
  });
  await test('C4 explicit OPENCODE_CONFIG precedes project config', () => {
    const file = path.join(lab,'extra.json'); write(file,{model:'explicit/model'});
    assert.equal(selection(effective(apiProbe('/api/config',{OPENCODE_CONFIG:file})).model),'project/model');
  });
  fs.unlinkSync(path.join(project,'opencode.json'));
  const jsonc = '{\n // preserve this exact user comment\n "model": "project/jsonc-model",\n}\n';
  write(path.join(project,'opencode.jsonc'),jsonc);
  await test('C5-C6 JSONC discovered from subdirectory and byte-for-byte unchanged', () => {
    const sub = path.join(project,'sub'); fs.mkdirSync(sub);
    assert.equal(selection(effective(apiProbe('/api/config',{},sub)).model),'project/jsonc-model');
    assert.equal(fs.readFileSync(path.join(project,'opencode.jsonc'),'utf8'),jsonc);
  });
  await test('C7 unknown top-level key omitted without rejecting document', () => {
    const entries = apiProbe('/api/config',{OPENCODE_CONFIG_CONTENT:'{"totally_unknown_key_xyz":true,"model":"inline/unknown"}'});
    assert.equal(selection(effective(entries).model),'inline/unknown');
    assert.ok(!JSON.stringify(entries).includes('totally_unknown_key_xyz'));
  });
  const proof = path.join(lab,'plugin-proof');
  const plugin = `import {writeFileSync} from 'node:fs'; writeFileSync(${JSON.stringify(proof)},'imported'); export default {id:'integration.proof',setup:async()=>({})};`;
  write(path.join(project,'.opencode/plugin/proof.js'),plugin);
  for (const [name,extra] of [['C8 auto-discovery executes project code',{}],['C8 OPENCODE_PURE is not a v2 trust boundary',{OPENCODE_PURE:'1'}],['C8 disabling default plugins is not a trust boundary',{OPENCODE_DISABLE_DEFAULT_PLUGINS:'1'}]]) {
    await test(name, async () => {
      fs.rmSync(proof,{force:true});
      await server(extra,async ({json}) => {
        docs(await json('/api/config'));
        // Config is available before the asynchronous plugin activation settles.
        for (let n=0;n<100 && !fs.existsSync(proof);n++) await sleep(100);
        assert.equal(fs.readFileSync(proof,'utf8'),'imported');
      });
    });
  }
  fs.rmSync(path.join(project,'.opencode/plugin'),{recursive:true});
  await test('C9 declared plugin directories execute; legacy declared file fixture is unsupported', async () => {
    const dir = path.join(project,'.opencode/declared');
    write(path.join(dir,'package.json'),{name:'integration-declared',type:'module',exports:{'.':'./index.js','./server':'./index.js'}});
    write(path.join(dir,'index.js'),plugin);
    fs.rmSync(proof,{force:true});
    await server({OPENCODE_CONFIG_CONTENT:JSON.stringify({plugins:['./.opencode/declared']})},async ({json}) => {
      docs(await json('/api/config'));
      for (let n=0;n<100 && !fs.existsSync(proof);n++) await sleep(100);
      assert.equal(fs.readFileSync(proof,'utf8'),'imported');
    });
  });
  const skill = name => `---\nname: ${name}\ndescription: Integration discovery fixture.\n---\nIntegration skill body.\n`;
  write(path.join(project,'.opencode/skills/project-fixture/SKILL.md'),skill('project-fixture'));
  write(path.join(globalDir,'skills/global-fixture/SKILL.md'),skill('global-fixture'));
  if (env.JUST_CODE_TEST_SKILL_ARCHIVE || env.JUST_CODE_TEST_SKILL_NAME) {
    assert.ok(env.JUST_CODE_TEST_SKILL_ARCHIVE && /^[a-z0-9-]+$/.test(env.JUST_CODE_TEST_SKILL_NAME || ''),'P14 requires archive and valid skill name');
    const dir = path.join(globalDir,'skills',env.JUST_CODE_TEST_SKILL_NAME); fs.mkdirSync(dir,{recursive:true});
    const r = spawnSync('tar',['-xzf',env.JUST_CODE_TEST_SKILL_ARCHIVE,'-C',dir],{encoding:'utf8'}); assert.equal(r.status,0,r.stderr);
  }
  await test('C10 project and global skills via /api/skill (including optional P14 archive)', async () => {
    await server({}, async ({json}) => {
      const names = ['global-fixture','project-fixture',env.JUST_CODE_TEST_SKILL_NAME].filter(Boolean);
      let response;
      for (let n=0;n<100;n++) {
        response = await json('/api/skill');
        if (names.every(name=>response.data.some(s=>s.name===name))) break;
        await sleep(100);
      }
      for (const name of names) {
        const found = response.data.find(s=>s.name===name);
        assert.ok(found,`missing skill ${name}: ${JSON.stringify(response)}`);
        assert.equal(typeof found.content,'string'); assert.equal(typeof found.path,'string');
      }
    });
  });
  await test('server auth, health, location, sessions, CLI reconnect and backend selection', async () => {
    await server({}, async ({url,request,json,child}) => {
      assert.equal((await request('/api/info',{password:null})).status,401);
      assert.equal((await request('/api/info',{password:'wrong-password'})).status,401);
      const info = await json('/api/info');
      assert.equal(info.version,version); assert.equal(info.pid,child.pid);
      assert.equal(typeof info.hostname,'string'); assert.ok(Array.isArray(info.urls));
      assert.equal(typeof info.paths.tmp,'string'); assert.equal(typeof info.capabilities.persistentPty,'boolean');
      const location = await json('/api/location'); assert.equal(location.directory,project);
      assert.deepEqual((await json('/api/session')).data,[]);
      const auth = {OPENCODE_PASSWORD:synthetic};
      assert.deepEqual(JSON.parse(cli(['session','list','--server',url,'--format','json'],auth)),[]);
      const created = (await json('/api/session',{method:'POST',body:{title:'integration resume proof'}})).data;
      assert.ok(created.id);
      assert.equal((await json(`/api/session/${created.id}`)).data.id,created.id);
      for (let n=0;n<2;n++) {
        const listed = JSON.parse(cli(['session','list','--server',url,'--format','json'],auth));
        assert.ok(listed.some(s=>s.id===created.id),'new CLI invocation must rediscover resumable session');
      }
      // Password is in the environment only, never URL/argv.
      if (process.platform === 'linux') {
        const argv = fs.readFileSync(`/proc/${child.pid}/cmdline`,'utf8'); assert.ok(!argv.includes(synthetic));
      }
      assert.ok(!url.includes(synthetic));
    });
  });
  console.log('SKIP C11: v1 OPENCODE_DISABLE_PROJECT_CONFIG is not a supported v2 isolation contract.');
  console.log('SKIP legacy declared .mjs plugin function fixture: v2 configured paths must be plugin directories; C9 tests the native equivalent.');
  if (process.env.OPENCODE_INTEGRATION_NETWORK === '1') {
    await test('N1 live managed Albert inference and AGENTS.md', () => {
      assert.ok(process.env.ALBERT_API_KEY,'network requested but ALBERT_API_KEY missing');
      const raw = fs.readFileSync(path.join(lab,'managed-configs/01-default.json'),'utf8');
      assert.ok(JSON.parse(raw).providers?.albert,'network must use composed native managed config');
      write(path.join(project,'AGENTS.md'),'Always end your answer with the exact marker ZQX7-JK1-MARKER.\n');
      // The marker is absent from the prompt, so prompt echo cannot satisfy this assertion.
      const out = cli(['run','--standalone','--format','json','Reply with the single word ready.'],{OPENCODE_CONFIG_CONTENT:raw,ALBERT_API_KEY:process.env.ALBERT_API_KEY});
      const events = out.split('\n').filter(Boolean).map(line=>JSON.parse(line));
      const texts = events.filter(e=>e.type==='text').map(e=>e.part?.text || e.text || '').join('\n');
      assert.ok(texts.includes('ready'),'no model response'); assert.ok(texts.includes('ZQX7-JK1-MARKER'),'AGENTS.md marker absent');
      fs.unlinkSync(path.join(project,'AGENTS.md'));
    });
  } else console.log('SKIP network (set OPENCODE_INTEGRATION_NETWORK=1 and ALBERT_API_KEY).');
} finally {
  await Promise.all([...children].map(stop));
}
console.log(`\npasses: ${passes}, failures: ${failures}`);
process.exitCode = failures ? 1 : 0;
})().catch(async e => { console.error(e.stack || e); await Promise.all([...children].map(stop)); process.exitCode=1; });
NODE
