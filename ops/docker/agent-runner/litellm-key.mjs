#!/usr/bin/env node

const die = (msg) => {
  process.stderr.write(`litellm-key: ${msg}\n`);
  process.exit(1);
};

const env = (name) => {
  const value = process.env[name];
  if (!value) die(`${name} is not set`);
  return value;
};

async function admin(path, body, timeoutMs) {
  const base = env('LITELLM_ADMIN_URL').replace(/\/+$/, '');
  const headers = {
    authorization: `Bearer ${env('LITELLM_MASTER_KEY')}`,
    'content-type': 'application/json',
  };
  if (process.env.LITELLM_TRACE_ID) headers['x-litellm-trace-id'] = process.env.LITELLM_TRACE_ID;
  let response;
  try {
    response = await fetch(`${base}${path}`, {
      method: 'POST',
      headers,
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(timeoutMs),
    });
  } catch (err) {
    const cause = err.cause ? ` (${err.cause.code ?? err.cause.name}: ${err.cause.message})` : '';
    die(`POST ${base}${path} failed: ${err.name}: ${err.message}${cause}`);
  }
  const text = await response.text();
  if (!response.ok) die(`POST ${base}${path} returned HTTP ${response.status}: ${text}`);
  return text;
}

const command = process.argv[2];

if (command === 'mint') {
  const models = env('LITELLM_KEY_MODELS')
    .split(',')
    .map((m) => m.trim())
    .filter(Boolean);
  if (models.length === 0) die('LITELLM_KEY_MODELS resolved to an empty model list');

  const budget = Number(env('LITELLM_KEY_BUDGET'));
  if (Number.isNaN(budget)) die(`LITELLM_KEY_BUDGET is not a number: ${env('LITELLM_KEY_BUDGET')}`);

  const alias = env('LITELLM_KEY_ALIAS');
  const text = await admin(
    '/key/generate',
    {
      key_alias: alias,
      max_budget: budget,
      duration: env('LITELLM_KEY_DURATION'),
      models,
      metadata: {
        source: process.env.LITELLM_KEY_SOURCE || 'agent-runner',
        trace_id: alias,
      },
    },
    30_000,
  );

  let key;
  try {
    key = JSON.parse(text).key;
  } catch {
    die(`key mint returned a body that is not JSON: ${text}`);
  }
  if (!key) die(`key mint returned no key. Response: ${text}`);
  process.stdout.write(key);
} else if (command === 'revoke') {
  await admin('/key/delete', { keys: [env('LITELLM_REVOKE_KEY')] }, 15_000);
} else {
  die(`unknown command ${command ?? '(none)'}; expected "mint" or "revoke"`);
}
