'use strict';

// Private, credential-free diagnostics only. Never accepts an existing data
// image: a fresh template clone and isolated network are mandatory.
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const { spawn, spawnSync } = require('node:child_process');
const root = path.resolve(__dirname, '../..');
const args = process.argv.slice(2);
if ((args.length !== 2 && !(args.length === 3 && ['--relative-data', '--relative-all'].includes(args[2]))) || args[0] !== '--bundle') throw new Error('native_diagnostic_arguments_invalid');
const relativeAll = args[2] === '--relative-all';
const relativeData = relativeAll || args[2] === '--relative-data';
const bundle = path.resolve(root, args[1]);
const manifest = path.join(bundle, 'bundle.json');
const document = JSON.parse(fs.readFileSync(manifest, 'utf8'));
if (document.isolated !== true) throw new Error('native_diagnostic_requires_isolation');
const check = spawnSync(path.join(root, 'electron/bin/amdl-desktop.exe'), ['--check-only', '--manifest', manifest], { encoding: 'utf8', shell: false, windowsHide: true });
if (check.status !== 0) throw new Error('native_diagnostic_bundle_invalid');
const directory = path.join(root, '.tmp-v2-native', 'diagnostic-' + crypto.randomUUID());
fs.mkdirSync(directory, { recursive: true });
const image = path.join(directory, 'data.img');
fs.copyFileSync(path.join(bundle, document.dataTemplate), image, fs.constants.COPYFILE_EXCL);
const absolute = (name) => path.join(bundle, name);
if (relativeAll) {
  fs.copyFileSync(absolute(document.kernel), path.join(directory, 'vmlinuz'));
  fs.copyFileSync(absolute(document.initramfs), path.join(directory, 'initramfs'));
  fs.mkdirSync(path.join(directory, 'firmware'));
  for (const name of fs.readdirSync(absolute(document.firmware))) {
    const source = path.join(absolute(document.firmware), name);
    if (!fs.lstatSync(source).isFile()) throw new Error('native_diagnostic_firmware_invalid');
    fs.copyFileSync(source, path.join(directory, 'firmware', name), fs.constants.COPYFILE_EXCL);
  }
}
const guestArgs = '--base-dir\n/data\n--host\n0.0.0.0\n--port\n12340\n';
const command = ['-L', relativeAll ? 'firmware' : absolute(document.firmware), '-accel', 'tcg', '-cpu', 'max', '-m', '512', '-smp', '2',
  '-kernel', relativeAll ? 'vmlinuz' : absolute(document.kernel), '-initrd', relativeAll ? 'initramfs' : absolute(document.initramfs),
  '-append', 'console=ttyS0 net.ifnames=0 biosdevname=0 lite_args_b64=' + Buffer.from(guestArgs).toString('base64'),
  '-display', 'none', '-serial', 'file:' + (relativeData ? 'serial.log' : path.join(directory, 'serial.log')), '-monitor', 'none', '-no-reboot',
  '-nic', 'user,model=e1000,restrict=on', '-drive', 'file=' + (relativeData ? 'data.img' : image.replaceAll(',', ',,')) + ',format=raw,if=virtio'];
if(document.controlProtocol===1){
  fs.writeFileSync(path.join(directory,'control.secret'),crypto.randomBytes(32).toString('hex'),{mode:0o600,flag:'wx'});
  command.push('-fw_cfg','name=opt/amdl/control,file='+(relativeData?'control.secret':path.join(directory,'control.secret')));
}
const child = spawn(absolute(document.qemu), command, { shell: false, windowsHide: true, cwd: relativeData ? directory : bundle, stdio: ['ignore', 'ignore', 'pipe'] });
let stderr = '';
child.stderr.setEncoding('utf8');
child.stderr.on('data', (chunk) => { stderr = (stderr + chunk).slice(-64 * 1024); });
const timer = setTimeout(() => child.kill(), 30000);
child.once('error', () => { clearTimeout(timer); process.exitCode = 1; });
child.once('close', (code, signal) => {
  clearTimeout(timer);
  fs.writeFileSync(path.join(directory, 'stderr.log'), stderr);
  process.stdout.write(JSON.stringify({ code, signal, directory }) + '\n');
});
