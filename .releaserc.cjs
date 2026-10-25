'use strict';

const { makeConfig } = require('@webgrip/semantic-release-config');

module.exports = makeConfig({
  forge: 'github',
  draftRelease: true,
  branches: ['main', { name: 'development', prerelease: 'rc', channel: false }],
  verifyReleaseCmd: 'bash scripts/release.sh check ${nextRelease.gitTag}',
  waivedCommits: ['706e0030b94f20059cfb14fe67e047b04b8a6703', '502ec5f74894af41a10e7d7f8ae202f1c5fb0368'],
});
