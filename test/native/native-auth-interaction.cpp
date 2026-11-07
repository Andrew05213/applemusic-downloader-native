// Offline downloader adapter fixture. No SDK, account or network is used.
#include "lite.h"
#include "import.h"
#include "native_login.h"
#include <cassert>
bool g_native_login=true;
static int submitted=0,cancelled=0;
AuthInteractionSubmitResult submit_auth_interaction_response(AuthInteractionResponse a){
 assert(a.protocol_version==1&&a.attempt_id==UINT64_MAX&&a.request_id==9007199254740993ULL);
 assert(a.button_id==2&&a.values.size()==1&&a.values[0].field_id==3&&a.values[0].value=="123456");++submitted;
 return AuthInteractionSubmitResult::Accepted;
}
uint64_t current_auth_interaction_attempt(){return UINT64_MAX;}
bool cancel_auth_interaction(uint64_t id){assert(id==UINT64_MAX);++cancelled;return true;}
int main(){
 AuthInteractionRequest q;q.attempt_id=UINT64_MAX;q.request_id=9007199254740993ULL;q.expires_after_ms=120000;q.kind=AuthInteractionKind::VerificationCode;
 q.title="PRIVATE_TITLE";q.message="PRIVATE_MESSAGE";
 AuthInteractionButton b;b.id=2;b.label="Verify";b.role=AuthInteractionButtonRole::Submit;q.buttons.push_back(b);
 AuthInteractionField f;f.id=3;f.type=AuthInteractionFieldType::VerificationCode;f.label="Code";f.min_bytes=f.max_bytes=6;f.digits_only=true;q.fields.push_back(f);
 native_display_interaction(q);
 native_interaction_reader();
 assert(submitted==1&&cancelled==1);
 AuthEvent intermediate;intermediate.type=AuthEventType::Response;
 intermediate.diagnostics.response_type.set(0);intermediate.diagnostics.store_error.set(10);intermediate.diagnostics.failure_type.set(-5000);
 native_auth_event(intermediate,nullptr);
 AuthEvent event;event.type=AuthEventType::Completed;event.outcome_present=true;event.outcome=AuthOutcome::InteractionTimedOut;
 native_auth_event(event,nullptr);
}
