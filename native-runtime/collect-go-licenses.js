'use strict';
// Inventory *actual binaries*, not every module in go.mod. Candidate texts are
// not a legal conclusion; per-file attribution obligations still need review.
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const {spawnSync} = require('node:child_process');
const root = path.resolve(__dirname, '..');
function run(args) {
  const env={...process.env,GOMODCACHE:process.env.GOMODCACHE||path.join(root,'.tmp-v2-native','development','go-mod-cache')};
  const result = spawnSync('go', args, {cwd:path.join(root,'apple-music-downloader'), env, encoding:'utf8', shell:false, windowsHide:true, maxBuffer:16*1024*1024});
  if (result.status !== 0 || result.error) throw Error('go_license_command_failed');
  return result.stdout;
}
const hash = file => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
function candidates(directory) {
  const found=[]; let count=0;
  function visit(relative) {
    for (const entry of fs.readdirSync(path.join(directory,relative),{withFileTypes:true})) {
      if (++count>100000) throw Error('go_license_module_too_large');
      const child=path.join(relative,entry.name);
      if(entry.isSymbolicLink()) continue;
      if(entry.isDirectory()) { if(entry.name!=='.git') visit(child); }
      else if(entry.isFile() && /^(license|licence|copying|notice)(\..*)?$/i.test(entry.name)) found.push(child);
    }
  }
  visit(''); return found.sort();
}
function collect(output, binaries) {
  const destination = path.resolve(output), relative = path.relative(path.join(root,'.artifacts'),destination);
  if (!relative || relative.startsWith('..') || path.isAbsolute(relative) || fs.existsSync(destination) || !binaries.length) throw Error('go_license_output_invalid');
  const modules = new Map(), inputs = [];
  for (const input of binaries) {
    const binary = path.resolve(input), build = run(['version','-m',binary]);
    inputs.push({name:path.basename(binary),sha256:hash(binary),goVersion:build.split(/\r?\n/)[0].split(': ').at(-1)});
    for (const line of build.split(/\r?\n/)) {
      const parts = line.trim().split(/\s+/);
      if (parts[0] !== 'dep') continue;
      if (parts.length !== 4 || !parts[3].startsWith('h1:')) throw Error('go_license_dependency_invalid');
      const identity = parts[1]+'@'+parts[2];
      if (modules.has(identity) && modules.get(identity).sum !== parts[3]) throw Error('go_license_dependency_mismatch');
      modules.set(identity, {module:parts[1],version:parts[2],sum:parts[3]});
    }
  }
  fs.mkdirSync(destination,{recursive:true});
  const records=[];
  for (const [identity, record] of [...modules].sort((a,b)=>a[0].localeCompare(b[0]))) {
    const data=JSON.parse(run(['mod','download','-json',identity]));
    if (data.Error || data.Sum !== record.sum || !data.Dir) throw Error('go_license_module_identity_mismatch');
    const files=candidates(data.Dir);
    const sub=crypto.createHash('sha256').update(identity).digest('hex').slice(0,16);
    fs.mkdirSync(path.join(destination,sub));
    record.licenseCandidates=[];
    for(const name of files){const source=path.join(data.Dir,name);const target=path.join(destination,sub,name);fs.mkdirSync(path.dirname(target),{recursive:true});fs.copyFileSync(source,target,fs.constants.COPYFILE_EXCL);record.licenseCandidates.push({path:sub+'/'+name.replaceAll('\\','/'),sha256:hash(target)});}
    record.reviewRequired=true;
    records.push(record);
  }
  const goRoot=run(['env','GOROOT']).trim(), goLicense=path.join(goRoot,'LICENSE');
  fs.copyFileSync(goLicense,path.join(destination,'Go-LICENSE'),fs.constants.COPYFILE_EXCL);
  const result={schemaVersion:1,inputs,standardLibraryLicense:{path:'Go-LICENSE',sha256:hash(goLicense)},modules:records,
    incomplete:true,note:'License candidates only; per-file and attribution review still required.'};
  fs.writeFileSync(path.join(destination,'inventory.json'),JSON.stringify(result,null,2)+'\n',{flag:'wx'});
  return {output:destination,modules:records.length,missing:records.filter(m=>!m.licenseCandidates.length).map(m=>m.module),incomplete:true};
}
if(require.main===module){try {const [out,...files]=process.argv.slice(2);console.log(JSON.stringify(collect(out,files)));}catch(error){console.error(error.message);process.exitCode=1;}}
module.exports={collect};
