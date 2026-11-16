# Milestone 0: credentials test results

Date: 2026-11-15. Claude Code 2.1.325 in `node:22-slim`, logged in once with a subscription account (not an API key). The login lives in a Docker volume mounted at `/home/node`.

## Tests
1. Two containers started at the same moment on the same volume, each running a ~150-word generation: both succeeded (8s and 14s).
2. Four containers at once on the same volume: all four succeeded.
3. A fresh container afterwards: still logged in.

## Conclusion
On one machine, a single subscription login copied into a shared volume works for several concurrent containers, and the concurrent runs did not log each other out.

## Test 2: separate credential copies (same day)
Copied the login volume into three volumes (one per simulated session).
- Three sessions at once, each on its own copy: all succeeded. The access token lasts about 8 hours (`expiresAt`), so no refresh happened.
- Then the access token in two copies was set to expired, so both had to refresh, and they were run at the same time. Result: one refreshed and worked; the other failed with "OAuth session expired and could not be refreshed" (its stored credentials ended up zeroed).
- Afterwards the original login volume and the untouched copy also failed with the same error, even though their access tokens had not expired. The most likely explanation is that the refresh token is rotated on use, and presenting the old one a second time makes the server revoke the whole login. I did not confirm that mechanism directly.
- The login had to be thrown away. A new `/login` is required before further tests.

**Conclusion:** copying the credential files to several sessions works only until the first token refresh (about 8 hours). After that, concurrent refreshes from copies of one login can kill the login for every session at once. Do not hand each session its own copy of the refresh token.

## Test 3: one shared volume across a forced refresh (after a fresh login)
The access token in the shared volume was set to expired, then containers were started at the same moment so they all had to refresh.
- Round 1: three containers, all succeeded, the token expiry moved forward, and a follow-up container was still logged in.
- Round 2: expired again, five containers at once: all succeeded, and a follow-up was still logged in.

**Conclusion:** many containers mounting the same credentials volume survive a token refresh. Unlike separate copies, they share one file, so there is only one refresh token. Two rounds is a small sample, and the containers may not have refreshed at exactly the same instant, so a longer soak test is still worth running.

## Not covered
- **Long-lived token:** Claude Code has a `claude setup-token` command that is meant to issue a long-lived token for non-interactive use, passed through the `CLAUDE_CODE_OAUTH_TOKEN` environment variable. If sessions use that instead of the interactive login, there is no refresh to race. Untested; check Anthropic's current docs for terms and expiry before relying on it.
- **Provider limits:** the subscription's usage limits and any concurrent-session limits apply across all sessions, and weren't stressed.
- **Gemini CLI and Codex:** untested.

## Implication for the design
Use Docker sandboxes that all mount one shared credentials volume per agent login (Test 3). Do not copy a refresh-token login into each sandbox (Test 2). Remote workers, where a shared volume is not possible, still need one of these, to be tested: (1) a single long-lived token per agent injected as an environment variable, with no refresh in the sandbox; (2) a shared credentials volume with only one process allowed to refresh; (3) a controller-side broker that holds the refresh token and gives sessions short-lived access tokens only. The same question needs answering for Gemini CLI and Codex.
