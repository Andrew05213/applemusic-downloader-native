#!/usr/bin/env python3
"""Deterministic source contract for noninteractive Wrapper restoration."""
import pathlib
import sys

path = pathlib.Path(sys.argv[1]) if len(sys.argv) > 1 else pathlib.Path(__file__).resolve().parents[1] / 'patches/wrapper-login-fd.patch'
patch = path.read_text(encoding='utf-8')
def view(name):
    section = patch.split(f'diff --git a/{name} b/{name}', 1)[1].split('\ndiff --git ', 1)[0]
    return '\n'.join(line[1:] for line in section.splitlines() if line.startswith(('+', ' ')) and not line.startswith('+++'))
main, launcher = view('main.c'), view('wrapper.c')
def require(value, message):
    if not value: raise SystemExit(message)
require('"--resume-session"' in launcher, 'launcher does not accept explicit session restore mode')
require('setenv("AMDL_WRAPPER_RESUME_SESSION", "1", 1)' in launcher, 'restore mode not handed to Android main')
require('if (amdl_resume_session)' in main, 'native credentials callback lacks a noninteractive restore guard')
guard = main[main.index('static void credentialHandler'):main.index('setenv("ANDROID_DNS_MODE"')]
require(guard.index('session_login_required') < guard.index('requiresHSA2VerificationCodeEv'), 'restore prompts for 2FA before opting out')
require('exit(78);' in guard, 'restore rejection does not have a controlled exit code')
require('if (!amdl_resume_session && (args_info.login_given || login_from_fd > 0))' in main, 'restore still runs password AuthenticateFlow')
start = main[main.index('int main(int argc'):]
require(start.index('g_music_token = get_music_user_token') < start.index('[wrapper-native] session_verified'), 'restored session marked verified before fresh account tokens')
require(start.index('[wrapper-native] session_verified') > start.index('if (g_music_token == NULL)'), 'failed token acquisition can verify a session')
print('Wrapper session restoration invariants passed')
