-- ADR-0078: a Run's key can be minted inside a LiteLLM team and granted that
-- team's read-only MCP access groups. Both are fixed at reservation, like the
-- budget and the model scope, so a retried mint asks for the same grant.
-- The defaults are today's behaviour: no team, no MCP tools.
ALTER TABLE run_llm_accounts
    ADD COLUMN gateway_team_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN mcp_access_groups JSONB NOT NULL DEFAULT '[]'::jsonb;
