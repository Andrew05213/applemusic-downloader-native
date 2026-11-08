'use strict';

// Offline audit of ordinary inline Markdown links, not a full Markdown parser.
// All project-owned documentation is current and checked strictly.
const fs = require('node:fs');
const path = require('node:path');
const ROOT = path.resolve(__dirname, '..');

function withoutFences(text) {
  return text.replace(/^```[^\n]*\n[\s\S]*?^```[^\n]*$/gm, '');
}

function plain(text) {
  return text.replace(/!?(?:\[([^\]]*)\])\([^)]*\)/g, '$1')
    .replace(/<[^>]*>/g, '').replace(/[`*_~]/g, '');
}

function anchors(text) {
  const seen = new Map();
  const result = new Set();
  for (const line of withoutFences(text).split(/\r?\n/)) {
    const match = line.match(/^#{1,6}\s+(.+?)\s*#*$/);
    if (!match) continue;
    const slug = plain(match[1]).toLowerCase()
      .replace(/[^\p{L}\p{N}\p{M}_\-\s]/gu, '').replace(/\s/g, '-');
    const count = seen.get(slug) || 0;
    seen.set(slug, count + 1);
    result.add(slug + (count ? '-' + count : ''));
  }
  return result;
}

function files(root) {
  const result = [
    'README.md', 'CHANGELOG.md', 'CONTRIBUTING.md', 'NATIVE-LOCAL-NOTICE.md',
    'appliance/README.md', 'legacy/README.md', 'src/renderer/README.md',
    'wrapper-linux/README.md', 'web/README.md',
  ];
  function visit(directory) {
    for (const entry of fs.readdirSync(path.join(root, directory), {withFileTypes: true})) {
      if (entry.isSymbolicLink()) continue;
      const name = directory + '/' + entry.name;
      if (entry.isDirectory()) visit(name);
      else if (entry.name.endsWith('.md')) result.push(name);
    }
  }
  visit('docs');
  return result;
}

function audit(root = ROOT) {
  const errors = [];
  const warnings = [];
  const documents = files(root);
  let localLinks = 0;
  let externalLinksNotChecked = 0;
  for (const file of documents) {
    const content = withoutFences(fs.readFileSync(path.join(root, file), 'utf8'));
    const links = /!?\[[^\]\n]*\]\((<[^>]+>|[^\s)]+)(?:\s+"[^"]*")?\)/g;
    for (const match of content.matchAll(links)) {
      const target = match[1].replace(/^<|>$/g, '');
      if (/^(https?:|mailto:|data:)/i.test(target)) {
        externalLinksNotChecked++;
        continue;
      }
      if (/^[a-z]+:/i.test(target)) continue;
      localLinks++;
      const [raw, fragment] = target.split('#');
      let local;
      try { local = decodeURIComponent(raw); } catch { local = raw; }
      const absolute = path.resolve(path.dirname(path.join(root, file)), local || path.basename(file));
      let reason = '';
      if (!fs.existsSync(absolute)) reason = 'missing target';
      else if (fragment && absolute.endsWith('.md')) {
        let slug;
        try { slug = decodeURIComponent(fragment); } catch { slug = fragment; }
        if (!anchors(fs.readFileSync(absolute, 'utf8')).has(slug)) reason = 'missing heading';
      }
      if (reason) errors.push({file, target, reason});
    }
  }
  return {documents: documents.length, localLinks, externalLinksNotChecked, errors, warnings};
}

if (require.main === module) {
  const result = audit();
  console.log(JSON.stringify(result, null, 2));
  if (result.errors.length || (process.argv.includes('--all') && result.warnings.length)) process.exitCode = 1;
}
module.exports = {audit, anchors};
