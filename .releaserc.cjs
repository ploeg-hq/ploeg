'use strict';

const { makeConfig } = require('@webgrip/semantic-release-config');

module.exports = makeConfig({
  forge: 'github',
  draftRelease: true,
  branches: ['main', { name: 'development', prerelease: 'rc', channel: false }],
  verifyReleaseCmd: 'bash scripts/release.sh check ${nextRelease.gitTag}',
});
