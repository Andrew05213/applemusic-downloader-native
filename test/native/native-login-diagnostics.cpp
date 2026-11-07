// Standalone, offline producer fixture; no Store library or account is loaded.
// Compile with -I native-runtime -I /src/cjson in the pinned toolchain.
#include "lite.h"
#include "import.h"
#include "native_login.h"
bool g_native_login = true;
int main() {
    int store = 10;
    long external = -20101;
    native_login_failure("store_response_failed", 0, &store, &external);
    external = 0;
    native_login_failure("store_response_failed", 0, &store, &external);
    external = 9007199254740992L;
    native_login_failure("store_response_failed", 0, &store, &external);
    native_login_event("dialog_other");
    g_native_login = false;
    native_login_event("dialog_unhandled"); // Legacy mode must not emit events.
}
