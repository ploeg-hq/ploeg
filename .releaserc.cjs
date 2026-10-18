'use strict';

const { makeConfig } = require('@webgrip/semantic-release-config');

const ANALYZER = '@semantic-release/commit-analyzer';

const shared = makeConfig({
  forge: 'github',
  draftRelease: true,
  changelog: false,
  branches: ['main', { name: 'development', prerelease: 'rc', channel: false }],
  verifyReleaseCmd: 'bash scripts/release.sh check ${nextRelease.gitTag}',
});

function breakingChangesBumpMinor(plugin) {
  if (!Array.isArray(plugin) || plugin[0] !== ANALYZER) return plugin;
  const [name, options] = plugin;
  const releaseRules = options.releaseRules.map((rule) =>
    rule.breaking === true ? { ...rule, release: 'minor' } : rule
  );
  return [name, { ...options, releaseRules }];
}

const plugins = shared.plugins.map(breakingChangesBumpMinor);
if (plugins.some((plugin) => (Array.isArray(plugin) ? plugin[0] : plugin) === '@semantic-release/git')) {
  throw new Error('Ploeg candidates are tags only: the Protect development ruleset refuses a release commit.');
}
const analyzer = plugins.find((plugin) => Array.isArray(plugin) && plugin[0] === ANALYZER);
if (analyzer === undefined || analyzer[1].releaseRules.some((rule) => rule.release === 'major')) {
  throw new Error('Ploeg releases automatically only 0.x candidates: no analyzer rule may cut a major.');
}

module.exports = { ...shared, plugins };
