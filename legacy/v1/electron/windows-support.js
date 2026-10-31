'use strict';

// The Appliance uses the independently serviced WSL (including systemd), not
// the legacy inbox WSL. Windows 10 21H2 is the minimum desktop baseline.
// Windows 11 also reports the NT 10.0 kernel version.
const MIN_WINDOWS_BUILD = 19044;
const WINDOWS_SUPPORT_MESSAGE = '支持 Windows 10 21H2/22H2（19044+）或 Windows 11 x64。';
const WINDOWS_SUPPORT_ACTION = '请更新至 Windows 10 22H2 或 Windows 11 x64，并安装系统更新和新版 WSL2。';

function supportedWindowsBuild(build) {
  return Number.isSafeInteger(build) && build >= MIN_WINDOWS_BUILD;
}

function windowsBuildFromRelease(release) {
  if (typeof release !== 'string') return null;
  const match = /^10\.0\.(\d+)(?:\.\d+)?$/.exec(release);
  if (!match) return null;
  const build = Number(match[1]);
  return Number.isSafeInteger(build) ? build : null;
}

module.exports = { MIN_WINDOWS_BUILD, WINDOWS_SUPPORT_MESSAGE, WINDOWS_SUPPORT_ACTION, supportedWindowsBuild, windowsBuildFromRelease };
