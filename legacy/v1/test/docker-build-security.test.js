'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');

const repositoryRoot = path.resolve(__dirname, '..');

test('Docker image uses the reviewed non-secret config example', () => {
  const dockerfile = fs.readFileSync(path.join(repositoryRoot, 'Dockerfile'), 'utf8');
  assert.match(
    dockerfile,
    /^COPY apple-music-downloader\/config\.example\.yaml \/app\/working\/config\.yaml$/m,
  );
  assert.doesNotMatch(
    dockerfile,
    /^COPY apple-music-downloader\/config\.yaml(?:\s|$)/m,
  );
});

test('Docker context excludes the ignored local YAML configuration', () => {
  const patterns = fs.readFileSync(path.join(repositoryRoot, '.dockerignore'), 'utf8')
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);
  assert.ok(patterns.includes('apple-music-downloader/config.yaml'));
});

test('Docker advertises login-fd only for its verified source-built Wrapper', () => {
  const dockerfile = fs.readFileSync(path.join(repositoryRoot, 'Dockerfile'), 'utf8');
  const compose = fs.readFileSync(path.join(repositoryRoot, 'docker-compose.yml'), 'utf8');
  assert.match(dockerfile, /AS wrapper-build\b/);
  assert.match(dockerfile, /grep -a -q -- '--login-fd' \/src\/wrapper-source\/wrapper/);
  assert.match(dockerfile, /grep -a -q -- 'AMDL_WRAPPER_LOGIN_FD' \/src\/wrapper-source\/rootfs\/system\/bin\/main/);
  assert.match(dockerfile, /^COPY --from=wrapper-build \/out\/wrapper \/app\/wrapper-linux\/wrapper$/m);
  assert.match(dockerfile, /^COPY --from=wrapper-build \/out\/android-main \/app\/rootfs\/system\/bin\/main$/m);
  assert.match(dockerfile, /^\s*AMDL_WRAPPER_SUPPORTS_LOGIN_FD=1 \\$/m);
  assert.doesNotMatch(dockerfile, /^COPY wrapper-linux\/wrapper \/app\/wrapper-linux\/wrapper$/m);
  assert.doesNotMatch(compose, /AMDL_WRAPPER_SUPPORTS_LOGIN_FD/);
});

test('Docker locks the exact reviewed Wrapper login-fd patch', () => {
  const dockerfile = fs.readFileSync(path.join(repositoryRoot, 'Dockerfile'), 'utf8');
  const applianceDockerfile = fs.readFileSync(path.join(repositoryRoot, 'appliance', 'Dockerfile'), 'utf8');
  const urlRequestAbiGate = fs.readFileSync(
    path.join(repositoryRoot, 'appliance', 'scripts', 'verify-urlrequest-abi.sh'),
    'utf8',
  );
  const applianceVerify = fs.readFileSync(
    path.join(repositoryRoot, 'appliance', 'scripts', 'verify.sh'),
    'utf8',
  );
  const patch = fs.readFileSync(path.join(repositoryRoot, 'appliance', 'patches', 'wrapper-login-fd.patch'));
  const versionEntries = Object.fromEntries(
    fs.readFileSync(path.join(repositoryRoot, 'appliance', 'versions.lock'), 'utf8')
      .split(/\r?\n/)
      .filter((line) => /^[A-Z0-9_]+=/.test(line))
      .map((line) => line.split(/=(.*)/s).slice(0, 2)),
  );
  const digest = crypto.createHash('sha256').update(patch).digest('hex');
  const match = dockerfile.match(/^ARG WRAPPER_PATCH_SHA256=([0-9a-f]{64})$/m);
  assert.ok(match, 'Dockerfile must lock the Wrapper patch with SHA-256');
  assert.equal(match[1], digest);
  assert.match(applianceVerify, /test-wrapper-http-message-abi\.py/);
  for (const source of [dockerfile, applianceDockerfile]) {
    assert.match(source, /MEDIAPLATFORM_BINARY_SHA256/);
    assert.match(source, /STORESERVICESCORE_BINARY_SHA256/);
    assert.match(source, /ANDROIDAPPMUSIC_BINARY_SHA256/);
    assert.match(source, /URLREQUEST_ALLOCATION_BYTES/);
    assert.match(source, /URLREQUEST_OBJECT_OFFSET/);
    assert.match(source, /URLREQUEST_OBJECT_BYTES/);
    assert.match(source, /URLREQUEST_OBJECT_ALIGNMENT/);
    assert.match(source, /STOREERRORCODE_OBJECT_OFFSET/);
    assert.match(source, /STOREERRORCODE_VALUE_BYTES/);
    assert.match(source, /verify-urlrequest-abi\.sh/);
    assert.match(source, /sha256sum -c -/);
    assert.match(source, /nm -D -S --defined-only/);
    assert.match(source, /data_length_size/);
    assert.match(source, /0000000000000028/);
    assert.match(source, /_ZNK13mediaplatform4Data6lengthEv/);
    assert.match(source, /objdump -d --start-address/);
    assert.ok(source.includes("grep -Ec 'mov[[:space:]]+0x8\\(%rdi\\),%rax'"));
    assert.match(source, /grep -a -q -- 'authentication_succeeded'/);
    assert.match(source, /grep -a -q -- 'dev_token complete'/);
  }
  for (const invariant of [
    'MEDIAPLATFORM_LIBRARY MEDIAPLATFORM_SHA256',
    'ANDROIDAPPMUSIC_LIBRARY ANDROIDAPPMUSIC_SHA256',
    'mediaplatform_symbols',
    'storeservices_symbols',
    '_ZN13mediaplatform11HTTPMessageC1',
    '_ZN13mediaplatform11HTTPMessageC2',
    '_ZN13mediaplatform11HTTPMessageD2Ev',
    '_ZN13mediaplatform11HTTPMessage9setHeader',
    '_ZN13mediaplatform11HTTPMessage11setBodyData',
    '_ZN17storeservicescore10URLRequestC1',
    '_ZN17storeservicescore10URLRequestC2',
    '_ZN17storeservicescore10URLRequestD2Ev',
    '_ZN17storeservicescore10URLRequest19setRequestParameter',
    '_ZN17storeservicescore10URLRequest3runEv',
    '_ZNK17storeservicescore10URLRequest5errorEv',
    '_ZNK17storeservicescore10URLRequest8responseEv',
    '_ZNK17storeservicescore11URLResponse18underlyingResponseEv',
    '_ZNK17storeservicescore19StoreErrorCondition9errorCodeEv',
    '_ZN17storeservicescore19StoreErrorConditionC1ERKNS_9ErrorCode',
    '00000000001959e0',
    '000000000000002b',
    '00000000001953b0',
    '000000000000011f',
    'store-error-accessor.disassembly',
    'store-error-constructor.disassembly',
    'store_error_provider_count',
    'STOREERRORCODE_OBJECT_OFFSET',
    'STOREERRORCODE_VALUE_BYTES',
    '0000000000101580',
    '00000000000d8200',
    '00000000000dbde0',
    '0000000000150940',
    '0000000000147b00',
    '0000000000146450',
    '000000000000053e',
    '00000000000dc180',
    '00000000000003e9',
    '0x220',
    '0x224',
    '0x208',
    '0x250',
    '0x20',
    '0xa0',
    '0xa8',
    '0x140',
    '0x28',
    '0x30',
    '0x38',
    '0x1,%ecx',
  ]) {
    assert.ok(urlRequestAbiGate.includes(invariant), `URLRequest ABI gate must lock ${invariant}`);
  }
  for (const source of [dockerfile, applianceDockerfile]) {
    assert.match(source, /"\$data_library" "\$\{MEDIAPLATFORM_BINARY_SHA256\}"/);
  }
  for (const name of [
    'STORESERVICESCORE_BINARY_SHA256',
    'ANDROIDAPPMUSIC_BINARY_SHA256',
    'URLREQUEST_ALLOCATION_BYTES',
    'URLREQUEST_OBJECT_OFFSET',
    'URLREQUEST_OBJECT_BYTES',
    'URLREQUEST_OBJECT_ALIGNMENT',
    'STOREERRORCODE_OBJECT_OFFSET',
    'STOREERRORCODE_VALUE_BYTES',
  ]) {
    const buildMatch = dockerfile.match(new RegExp(`^ARG ${name}=([^\\s]+)$`, 'm'));
    assert.ok(buildMatch, `Dockerfile must lock ${name}`);
    assert.equal(buildMatch[1], versionEntries[name], `${name} must match appliance/versions.lock`);
  }
  for (const name of ['WRAPPER_BUILT_SHA256', 'ANDROID_MAIN_BUILT_SHA256']) {
    const buildMatch = dockerfile.match(new RegExp(`^ARG ${name}=([0-9a-f]{64})$`, 'm'));
    assert.ok(buildMatch, `Dockerfile must lock ${name}`);
    assert.equal(buildMatch[1], versionEntries[name], `${name} must match appliance/versions.lock`);
    const applianceMatches = [...applianceDockerfile.matchAll(new RegExp(`^ARG ${name}=([0-9a-f]{64})$`, 'gm'))];
    assert.equal(applianceMatches.length, 2, `appliance Dockerfile must pin ${name} in both stages`);
    for (const applianceMatch of applianceMatches) {
      assert.equal(applianceMatch[1], versionEntries[name], `appliance ${name} must match appliance/versions.lock`);
    }
  }
});

test('Docker mounts ignored credentials read-only instead of persisting secrets in Config.Env', () => {
  const compose = fs.readFileSync(path.join(repositoryRoot, 'docker-compose.yml'), 'utf8');
  assert.doesNotMatch(compose, /^\s*env_file:/m);
  assert.match(compose, /^\s*AMDL_CREDENTIALS_FILE:\s*\/run\/secrets\/amdl-credentials\.env$/m);
  assert.match(compose, /^\s*source:\s*\$\{AMDL_DOCKER_CREDENTIALS_FILE:-\.\/\.env\.example\}$/m);
  assert.match(compose, /^\s*target:\s*\/run\/secrets\/amdl-credentials\.env$/m);
  assert.match(compose, /^\s*read_only:\s*true$/m);
  for (const secret of ['APPLE_EMAIL', 'APPLE_PASSWORD', 'MEDIA_USER_TOKEN', 'AUTHORIZATION_TOKEN', 'AMDL_API_TOKEN']) {
    assert.doesNotMatch(compose, new RegExp(`^\\s*${secret}:`, 'm'));
  }
});

test('Docker keeps security metadata in a private named volume', () => {
  const compose = fs.readFileSync(path.join(repositoryRoot, 'docker-compose.yml'), 'utf8');
  assert.match(compose, /^\s*- amdl-data:\/app\/data$/m);
  assert.match(compose, /^volumes:\s*\n\s+amdl-data:\s*$/m);
  assert.doesNotMatch(compose, /\$\{DATA_DIR/);
  assert.match(compose, /127\.0\.0\.1:\$\{AMDL_HOST_PORT:-8080\}:8080/);
  assert.match(compose, /^\s*AMDL_RUNTIME:\s*docker$/m);
});

// The Wrapper patch is compiled into both the launcher and Android main, so
// editing it necessarily changes both built outputs. versions.lock records which
// patch its locked built hashes came from; without checking that link the drift
// is only found part way through a Docker build, as a bare sha256sum mismatch
// with no indication of what to do about it.
test('versions.lock records which Wrapper patch its built hashes came from', () => {
  const versionsPath = path.join(repositoryRoot, 'appliance', 'versions.lock');
  const versionEntries = Object.fromEntries(
    fs.readFileSync(versionsPath, 'utf8')
      .split(/\r?\n/)
      .filter((line) => /^[A-Z0-9_]+=/.test(line))
      .map((line) => line.split(/=(.*)/s).slice(0, 2)),
  );
  for (const key of ['WRAPPER_BUILT_SHA256', 'ANDROID_MAIN_BUILT_SHA256', 'WRAPPER_BUILT_FROM_PATCH_SHA256']) {
    assert.match(versionEntries[key] ?? '', /^[0-9a-f]{64}$/, `versions.lock must pin ${key}`);
  }
  const patch = fs.readFileSync(path.join(repositoryRoot, 'appliance', 'patches', 'wrapper-login-fd.patch'));
  const patchDigest = crypto.createHash('sha256').update(patch).digest('hex');
  assert.equal(
    versionEntries.WRAPPER_BUILT_FROM_PATCH_SHA256,
    patchDigest,
    'the Wrapper patch changed since the locked built hashes were produced: run one reproducible '
      + 'build, then refresh WRAPPER_BUILT_SHA256, ANDROID_MAIN_BUILT_SHA256 and '
      + 'WRAPPER_BUILT_FROM_PATCH_SHA256 together',
  );
});

// The gate above is only meaningful if the build actually enforces every built
// native hash it protects.
test('the appliance build enforces all three locked built hashes', () => {
  const applianceDockerfile = fs.readFileSync(path.join(repositoryRoot, 'appliance', 'Dockerfile'), 'utf8');
  assert.match(applianceDockerfile, /\$\{WRAPPER_BUILT_SHA256\}/);
  assert.match(applianceDockerfile, /\$\{ANDROID_MAIN_BUILT_SHA256\}/);
  assert.match(applianceDockerfile, /\$\{ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256\}/);
  const verify = fs.readFileSync(path.join(repositoryRoot, 'appliance', 'scripts', 'verify.sh'), 'utf8');
  assert.match(verify, /assert_built_wrapper_pins_match_patch/);
  assert.match(verify, /manifest\["productVersion"\] != expected\["productVersion"\]/);
  assert.match(verify, /manifest\["sourceDateEpoch"\] != expected\["sourceDateEpoch"\]/);
  assert.match(verify, /manifest\["applianceBuildId"\] != expected\["buildId"\]/);
});

test('the appliance build verifies the locked native network preflight source', () => {
  const versions = Object.fromEntries(
    fs.readFileSync(path.join(repositoryRoot, 'appliance', 'versions.lock'), 'utf8')
      .split(/\r?\n/)
      .filter((line) => /^[A-Z0-9_]+=/.test(line))
      .map((line) => line.split(/=(.*)/s).slice(0, 2)),
  );
  const source = fs.readFileSync(
    path.join(repositoryRoot, 'appliance', 'native', 'network_preflight.c'),
  );
  const header = fs.readFileSync(
    path.join(repositoryRoot, 'appliance', 'native', 'network_preflight.h'),
  );
  const entrypoint = fs.readFileSync(
    path.join(repositoryRoot, 'appliance', 'native', 'network_preflight_main.c'),
  );
  const digest = crypto.createHash('sha256').update(Buffer.concat([
    Buffer.from('F\0network_preflight.c\0'),
    source,
    Buffer.from('\0F\0network_preflight.h\0'),
    header,
    Buffer.from('\0F\0network_preflight_main.c\0'),
    entrypoint,
  ])).digest('hex');
  assert.equal(versions.NETWORK_PREFLIGHT_SOURCE_SHA256, digest);

  const common = fs.readFileSync(path.join(repositoryRoot, 'appliance', 'scripts', 'common.sh'), 'utf8');
  const build = fs.readFileSync(path.join(repositoryRoot, 'appliance', 'scripts', 'build-image.sh'), 'utf8');
  const dockerfile = fs.readFileSync(path.join(repositoryRoot, 'appliance', 'Dockerfile'), 'utf8');
  const rootDockerfile = fs.readFileSync(path.join(repositoryRoot, 'Dockerfile'), 'utf8');
  const digestInput = "printf 'F\\0network_preflight.c\\0'; cat /tmp/network_preflight.c; printf '\\0F\\0network_preflight.h\\0'; cat /tmp/network_preflight.h; printf '\\0F\\0network_preflight_main.c\\0'; cat /tmp/network_preflight_main.c";
  assert.match(common, /native network preflight source differs from versions\.lock/);
  assert.match(build, /NETWORK_PREFLIGHT_SOURCE_SHA256=\$\{PREPARED_NETWORK_PREFLIGHT_SOURCE_SHA\}/);
  assert.match(dockerfile, /^ARG NETWORK_PREFLIGHT_SOURCE_SHA256$/m);
  assert.ok(dockerfile.includes(digestInput), 'Appliance Dockerfile must hash the NUL-delimited probe source set');
  assert.ok(rootDockerfile.includes(digestInput), 'root Dockerfile must hash the NUL-delimited probe source set');
  assert.match(dockerfile, /network_preflight_source_sha=.*network_preflight\.h/);
  assert.match(dockerfile, /test "\$network_preflight_source_sha" = "\$\{NETWORK_PREFLIGHT_SOURCE_SHA256\}";/);
});

test('native pin measurement is isolated from the gated runtime stage', () => {
  const applianceDockerfile = fs.readFileSync(path.join(repositoryRoot, 'appliance', 'Dockerfile'), 'utf8');
  assert.match(applianceDockerfile, /FROM build-base AS wrapper-compiled/);
  assert.match(applianceDockerfile, /FROM wrapper-compiled AS wrapper-builder/);
  assert.match(applianceDockerfile, /mkdir -p \/out;[\s\S]*> \/out\/native-pins\.env;/);
  assert.ok(applianceDockerfile.includes(
    `printf '%s  %s\\n' "\${WRAPPER_BUILT_SHA256}" /out/wrapper | sha256sum -c -;`,
  ));
  assert.ok(applianceDockerfile.includes(
    `printf '%s  %s\\n' "\${ANDROID_MAIN_BUILT_SHA256}" /out/android-main | sha256sum -c -`,
  ));
  assert.match(applianceDockerfile, /COPY --from=wrapper-builder \/out\/wrapper/);
  assert.doesNotMatch(applianceDockerfile, /COPY --from=wrapper-compiled\b/);

  const measurement = fs.readFileSync(
    path.join(repositoryRoot, 'appliance', 'scripts', 'measure-native-pins.sh'),
    'utf8',
  );
  assert.match(measurement, /--target wrapper-compiled/);
  assert.match(measurement, /--no-cache/);
  assert.match(measurement, /runs=.*2/);
  assert.match(measurement, /native build is not reproducible across measurement runs/);
  assert.match(measurement, /\(\[A-Z\]\[A-Z0-9_\]\*\)/);
  assert.match(measurement, /measurement output must not overwrite versions\.lock/);
  assert.match(measurement, /MSYS_NO_PATHCONV=1 docker cp/);
  assert.match(measurement, /cygpath -w/);
  assert.doesNotMatch(measurement, /WRAPPER_BUILT_SHA256.*versions\.lock.*>/s);
});

test('the appliance build executes the credential-free Android CA preflight harness offline', () => {
  const applianceDockerfile = fs.readFileSync(
    path.join(repositoryRoot, 'appliance', 'Dockerfile'),
    'utf8',
  );
  const common = fs.readFileSync(
    path.join(repositoryRoot, 'appliance', 'scripts', 'common.sh'),
    'utf8',
  );
  const harness = fs.readFileSync(
    path.join(repositoryRoot, 'appliance', 'scripts', 'run-network-preflight-harness.sh'),
    'utf8',
  );

  assert.match(common, /run-network-preflight-harness\.sh/);
  assert.match(common, /network-preflight-fixtures/);
  assert.match(applianceDockerfile, /^COPY run-network-preflight-harness\.sh \/tmp\/run-network-preflight-harness\.sh$/m);
  assert.match(applianceDockerfile, /^COPY network-preflight-fixtures\/ \/tmp\/network-preflight-fixtures\/$/m);
  assert.match(
    applianceDockerfile,
    /RUN --network=none bash \/tmp\/run-network-preflight-harness\.sh[\s\S]*\/src\/wrapper-source\/rootfs[\s\S]*\/src\/wrapper-build\/amdl_network_preflight[\s\S]*\/tmp\/network-preflight-fixtures[\s\S]*\/tmp\/network-preflight-fixture-server\.py/,
  );
  assert.match(harness, /env -i[\s\S]*AMDL_NETWORK_RESULT_FD=3[\s\S]*chroot/);
  assert.match(harness, /run_case trusted trusted server-correct\.crt true/);
  assert.match(harness, /run_case unknown_ca untrusted server-correct\.crt false tls/);
  assert.match(harness, /run_case wrong_hostname trusted server-wrong-host\.crt false tls/);
  assert.match(harness, /run_case missing_bundle missing server-correct\.crt false ca_bundle_missing/);
  assert.match(harness, /run_case malformed_bundle malformed server-correct\.crt false ca_bundle_invalid/);
  assert.match(harness, /run_case trusted trusted server-correct\.crt true '' '0' '0' true/);
  assert.match(harness, /run_case malformed_bundle malformed server-correct\.crt false ca_bundle_invalid '77' any ignore/);
  assert.match(applianceDockerfile, /^ARG ANDROID_CA_BUNDLE_PATH$/m);
  assert.match(applianceDockerfile, /AndroidCABundlePath=\$\{ANDROID_CA_BUNDLE_PATH\}/);
});

test('the Docker runtime supplies the same locked Android CA bundle and executes the offline harness', () => {
  const dockerfile = fs.readFileSync(path.join(repositoryRoot, 'Dockerfile'), 'utf8');
  const versions = Object.fromEntries(
    fs.readFileSync(path.join(repositoryRoot, 'appliance', 'versions.lock'), 'utf8')
      .split(/\r?\n/)
      .filter((line) => /^[A-Z0-9_]+=/.test(line))
      .map((line) => line.split(/=(.*)/s).slice(0, 2)),
  );

  assert.match(dockerfile, /AS android-ca-bundle\b/);
  assert.match(
    dockerfile,
    /^COPY --from=build \/etc\/ssl\/certs\/ca-certificates\.crt \/etc\/ssl\/certs\/ca-certificates\.crt$/m,
  );
  assert.match(
    dockerfile,
    new RegExp(`^ARG ANDROID_CA_BUNDLE_SHA256=${versions.ANDROID_CA_BUNDLE_SHA256}$`, 'm'),
  );
  assert.match(dockerfile, /^COPY --from=android-ca-bundle \/out\/android-ca-bundle\.pem \/tmp\/amdl-android-ca-bundle\.pem$/m);
  assert.match(
    dockerfile,
    /install -D -o root -g root -m 0644 \/tmp\/amdl-android-ca-bundle\.pem \/app\/rootfs\/usr\/local\/android\/cert\.pem/,
  );
  assert.match(
    dockerfile,
    /\$\{ANDROID_CA_BUNDLE_SHA256\}" \/app\/rootfs\/usr\/local\/android\/cert\.pem \| sha256sum -c -/,
  );
  assert.match(dockerfile, /^COPY appliance\/scripts\/run-network-preflight-harness\.sh \/tmp\/run-network-preflight-harness\.sh$/m);
  assert.match(dockerfile, /^COPY appliance\/tests\/network-preflight-fixtures\/ \/tmp\/network-preflight-fixtures\/$/m);
  assert.match(
    dockerfile,
    /RUN --network=none bash \/tmp\/run-network-preflight-harness\.sh[\s\S]*\/src\/wrapper-source\/rootfs[\s\S]*\/src\/wrapper-build\/amdl_network_preflight/,
  );
  assert.match(
    dockerfile,
    /^COPY --from=wrapper-build \/out\/android-network-preflight \/app\/rootfs\/system\/bin\/amdl-network-preflight$/m,
  );
  assert.match(dockerfile, /! grep -a -q -- 'curl_easy_setopt_hook' \/src\/wrapper-source\/rootfs\/system\/bin\/main/);
  assert.match(dockerfile, /! grep -a -q -- 'install_hooks' \/src\/wrapper-source\/rootfs\/system\/bin\/main/);
  assert.match(dockerfile, /curl_library="\$\(find \/src\/wrapper-source\/rootfs\/system\/lib64 -maxdepth 1 -type f -name 'libcurl\.so' -print -quit\)"/);
  assert.match(dockerfile, /tr '\\000' '\\n' < "\$curl_library" \| grep -Fx '\/usr\/local\/android\/cert\.pem'/);
});

test('the appliance build preserves the Linux CA path under Git Bash without disabling host path conversion', () => {
  const build = fs.readFileSync(
    path.join(repositoryRoot, 'appliance', 'scripts', 'build-image.sh'),
    'utf8',
  );

  assert.match(build, /MINGW\*\|MSYS\*\|CYGWIN\*/);
  assert.match(
    build,
    /docker_msys_arg_conv_excl="\$\{docker_msys_arg_conv_excl:\+\$\{docker_msys_arg_conv_excl\};\}ANDROID_CA_BUNDLE_PATH="/,
  );
  assert.match(build, /\$\{MSYS2_ARG_CONV_EXCL:-\}/);
  assert.match(build, /MSYS2_ARG_CONV_EXCL="\$docker_msys_arg_conv_excl" docker buildx build/);
  assert.doesNotMatch(build, /MSYS_NO_PATHCONV=1 docker buildx build/);
});
