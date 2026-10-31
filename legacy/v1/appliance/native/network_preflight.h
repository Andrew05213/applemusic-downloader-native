#pragma once

// Returns 1 when AMDL_NETWORK_RESULT_FD selected preflight mode and stores the
// process exit code in exit_code. The result-FD document is schema v2 and only
// contains bounded curl/TLS values plus the allowlisted stage and CA source.
// Returns 0 for an ordinary Wrapper launch.
int amdl_network_preflight_if_requested(const char *proxy, int *exit_code);
