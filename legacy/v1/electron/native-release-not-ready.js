'use strict';
// A development runtime is not a migrated, audited 2.0 installer. Keep old
// packaging commands from accidentally producing a WSL-based 1.4 artifact.
process.stderr.write('native_packaging_not_ready: use the native development entry; legacy WSL installers are not 2.0 builds.\n');
process.exitCode=1;
