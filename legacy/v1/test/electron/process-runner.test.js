const test = require('node:test');
const assert = require('node:assert/strict');
const { EventEmitter } = require('node:events');
const { PassThrough } = require('node:stream');

const {
  ProcessRunner,
  decodeProcessOutput,
  redactSecrets,
} = require('../../electron/process-runner');

test('decodeProcessOutput handles redirected WSL UTF-16LE output and NULs', () => {
  const output = Buffer.concat([Buffer.from([0xff, 0xfe]), Buffer.from('AppleMusicDownloader\r\n', 'utf16le')]);
  assert.equal(decodeProcessOutput(output), 'AppleMusicDownloader\r\n');
  assert.equal(decodeProcessOutput('A\u0000B\u0000'), 'AB');
});

test('redaction removes overlapping secrets longest-first without leaking a suffix', () => {
  assert.equal(
    redactSecrets('credential=short-long-tail', ['short', 'short-long-tail', 'short']),
    'credential=[REDACTED]',
  );
});

test('exec always uses an argument array with shell disabled', async () => {
  const calls = [];
  const runner = new ProcessRunner({
    execFile: async (command, args, options) => {
      calls.push({ command, args, options });
      return { stdout: Buffer.from('ok'), stderr: Buffer.alloc(0) };
    },
  });

  assert.deepEqual(await runner.exec('wsl.exe', ['--list', '--verbose']), { stdout: 'ok', stderr: '' });
  assert.equal(calls[0].command, 'wsl.exe');
  assert.deepEqual(calls[0].args, ['--list', '--verbose']);
  assert.equal(calls[0].options.shell, false);
  await assert.rejects(runner.exec('wsl.exe', '--list'), /array of strings/);
});

test('secret payload is sent only through stdin and reflected output is redacted', async () => {
  const sentinel = 'SENTINEL-secret-credential';
  let invocation;
  let received = Buffer.alloc(0);
  const runner = new ProcessRunner({
    spawn: (command, args, options) => {
      invocation = { command, args, options };
      const child = new EventEmitter();
      child.stdin = new PassThrough();
      child.stdout = new PassThrough();
      child.stderr = new PassThrough();
      child.kill = () => {};
      child.stdin.on('data', (chunk) => { received = Buffer.concat([received, chunk]); });
      child.stdin.on('finish', () => {
        child.stderr.end(`server rejected ${sentinel}`);
        child.stdout.end();
        setImmediate(() => child.emit('close', 1, null));
      });
      return child;
    },
  });

  await assert.rejects(
    runner.input('wsl.exe', ['--distribution', 'AppleMusicDownloader', '--exec', '/control', 'credential-stdin'], sentinel, { secrets: [sentinel] }),
    (error) => {
      assert.equal(error.message.includes(sentinel), false);
      assert.match(error.message, /\[REDACTED\]/);
      return true;
    },
  );
  assert.equal(invocation.options.shell, false);
  assert.equal(invocation.args.join(' ').includes(sentinel), false);
  assert.equal(Object.values(invocation.options.env).join('\n').includes(sentinel), false);
  assert.equal(received.toString('utf8'), sentinel);
});
