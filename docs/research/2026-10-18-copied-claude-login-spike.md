# Copied Claude login spike, 2026-10-18

The question, from [ADR-0112](../adrs/0112-a-claude-environment-run-uses-the-claude-login-of-the-person-it-acts-for.md): does a claude.ai login copied into a fresh configuration directory dispatch a self-hosted environment session and refresh itself, without a browser?

## Setup

- Claude Code 2.1.296, the version the runner image pins.
- A WebGrip Team member's login.
- The self-hosted environment `ccpool_017SvGpGv1jLfduyvyWMRh3F`.
- The script printed only SHA-256 prefixes of tokens, never a token.

## Steps and results

1. Copied only `.credentials.json` and `.claude.json` into a new directory, set as `CLAUDE_CONFIG_DIR`.
   - `claude auth status` reported logged in, organisation WebGrip, subscription `team`.
   - The credential file holds `accessToken`, `expiresAt`, `rateLimitTier`, `refreshToken`, `refreshTokenExpiresAt`, `scopes` and `subscriptionType`.
   - The scopes are `user:file_upload`, `user:inference`, `user:mcp_servers`, `user:plugins`, `user:profile` and `user:sessions:claude_code`.
2. Set the copy's `expiresAt` to the past, then ran `claude -p … --environment <id> --ref development --output-format json` from a checkout.
   - It exited 0 and created a session.
   - The session stopped at the runner's checkout hook ("no running Run recorded this session"), before Claude Code started, so it used no model.
3. The CLI wrote a refreshed file into the copy:
   - **both** the access token and the refresh token changed;
   - `expiresAt` moved forward;
   - `refreshTokenExpiresAt` stayed at its original date, 30 days after sign-in.
4. Copied the refreshed file back to the source directory. `claude auth status` there still reported logged in.

## Findings

- A copied login dispatches and refreshes without a browser.
- The refresh token rotates on every refresh, so a copy taken before a refresh is dead afterwards. Many OAuth servers treat reuse of a rotated refresh token as theft and revoke the whole login, so any design that copies a login must write the refreshed file back, and must never run two refreshes of one login at once.
- Refreshing does not extend the 30-day limit. A person signs in again at least every 30 days.

## What this decided

These findings apply only to the stored-login option, which ADR-0112 parked on 2026-10-18 pending Anthropic's answer on its terms. The chosen design copies no login.
