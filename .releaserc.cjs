'use strict';

const { makeConfig } = require('@webgrip/semantic-release-config');

const ANALYZER = '@semantic-release/commit-analyzer';

const shared = makeConfig({
  forge: 'github',
  draftRelease: true,
  manifest: 'helm',
  chartPath: 'ops/helm/ploeg',
  changelogTitle: '# Changelog',
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
const analyzer = plugins.find((plugin) => Array.isArray(plugin) && plugin[0] === ANALYZER);
if (analyzer === undefined || analyzer[1].releaseRules.some((rule) => rule.release === 'major')) {
  throw new Error('Ploeg releases automatically only 0.x candidates: no analyzer rule may cut a major.');
}

module.exports = { ...shared, plugins };
