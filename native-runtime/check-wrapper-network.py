#!/usr/bin/env python3
"""Verify patched production networking using loopback-only upstream fixtures."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

source = Path('/native-build/source')
deps = source / 'build/_deps'
deps.mkdir(parents=True, exist_ok=True)
for name in ('cjson', 'dobby'):
    (deps / (name + '-src')).symlink_to('/src/' + name, target_is_directory=True)

with tempfile.TemporaryDirectory(prefix='amdl-network-check-') as temporary:
    output = Path(temporary)
    subprocess.run(['c++', '-std=c++11', '-O1', '-g', '-fsanitize=address,undefined',
                    '-fno-omit-frame-pointer', str(source / 'tests/network_config.cpp'),
                    '-o', str(output / 'network-config')], check=True)
    subprocess.run([str(output / 'network-config')], check=True, timeout=120)
    # Debian runtime packages need not include the development-name symlink.
    candidates = list(Path('/usr/lib').glob('*/libcurl.so.4'))
    if not candidates:
        candidates = list(Path('/usr/lib').glob('*/libcurl-gnutls.so.4'))
    if not candidates:
        raise RuntimeError('native_network_fixture_curl_missing')
    (output / 'libcurl.so').symlink_to(candidates[0].resolve())
    os.environ['LD_LIBRARY_PATH'] = str(output)
    sys.path.insert(0, str(source / 'tests'))
    from test_curl_options import CurlOptionsTests

    class NativeCurlOptionsTests(CurlOptionsTests):
        # Native deliberately has a stricter policy than upstream's debug
        # opt-out. A debug flag must never bypass certificate/hostname checks.
        def test_debug_remains_explicit_opt_out(self):
            self.assertEqual(self.request(tls=True, debug=True), '0 0')
            self.assertEqual(self.request(tls=True, trusted=True, debug=True), '1 201 ok')

    result = unittest.TextTestRunner(verbosity=2).run(
        unittest.defaultTestLoader.loadTestsFromTestCase(NativeCurlOptionsTests))
    if not result.wasSuccessful():
        raise SystemExit(1)
print('native_wrapper_network_compatibility_passed')
