'use strict';

const { createDRMBuildConfig } = require('./electron/drm-build-config');

// Inherited configs export the build object itself, not package.json's wrapper.
module.exports = createDRMBuildConfig({
  build: require('./electron/legacy-desktop-package.json').build,
  repositoryRoot: __dirname,
});
