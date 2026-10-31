'use strict';

// Transport availability, cached account data, verified authentication and
// media capabilities are deliberately separate. Only the owning supervisor
// may produce these observations; Wrapper /status alone proves none of them.
function nativeCapabilities(input = {}) {
  const state = input && typeof input === 'object' ? input : {};
  const runtime = state.runtime === 'native-lite'
    && state.backendReady === true
    && state.legacyWslRequired === false
    && state.embeddedWebRequired === false;
  const account = runtime && state.wrapperReady === true
    && state.sessionVerified === true;
  return Object.freeze({
    browse: runtime && state.catalogVerified === true,
    library: account && state.libraryVerified === true,
    download: account && state.downloadVerified === true,
    fullPlayback: account && state.playbackVerified === true,
  });
}

module.exports = { nativeCapabilities };
