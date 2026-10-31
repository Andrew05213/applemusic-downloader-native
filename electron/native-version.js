'use strict';
const {version}=require('../package.json');
// A local stable version does not grant public redistribution permission.
if(!/^1\.0\.(?:0|[1-9][0-9]*)(?:-beta\.[1-9][0-9]*)?$/.test(version))throw new Error('native_version_invalid');
module.exports={VERSION:version};
