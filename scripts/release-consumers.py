import base64
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from pathlib import PurePosixPath

PRODUCER = 'ploeg'
TAG = re.compile(r'^v(?P<version>(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?)$')
REPOSITORY = re.compile(r'^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$')
BRANCH = re.compile(r'^[A-Za-z0-9_./-]+$')
MERGE_STYLES = {'merge', 'rebase', 'rebase-merge', 'squash', 'fast-forward-only'}
SUPPORTED_FORGES = {'forgejo'}
DEFAULT_MESSAGE = 'chore(deps): update ' + PRODUCER + ' to {version}'
LOOPBACK_HOSTS = {'127.0.0.1', 'localhost', '::1'}
MERGE_ATTEMPTS = 6


class ConsumerError(Exception):
    pass


@dataclass(frozen=True)
class Consumer:
    forge: str
    url: str
    repo: str
    base: str
    recipe: str
    message: str
    merge_style: str

    @property
    def clone_url(self):
        return f'{self.url}/{self.repo}.git'


def parse_consumer(text):
    try:
        entry = json.loads(text)
    except json.JSONDecodeError as error:
        raise ConsumerError(f'the consumer entry is not JSON: {error}') from error
    if not isinstance(entry, dict):
        raise ConsumerError('the consumer entry must be a JSON object')
    missing = [field for field in ('forge', 'url', 'repo', 'base', 'recipe') if not isinstance(entry.get(field), str) or not entry[field]]
    if missing:
        raise ConsumerError(f'the consumer entry lacks {", ".join(missing)}')
    unknown = sorted(set(entry) - {'forge', 'url', 'repo', 'base', 'recipe', 'message', 'merge_style'})
    if unknown:
        raise ConsumerError(f'the consumer entry has unknown fields: {", ".join(unknown)}')
    if entry['forge'] not in SUPPORTED_FORGES:
        raise ConsumerError(f'forge {entry["forge"]!r} is not supported; supported: {", ".join(sorted(SUPPORTED_FORGES))}')
    url = urllib.parse.urlsplit(entry['url'].rstrip('/'))
    if url.scheme != 'https' and not (url.scheme == 'http' and url.hostname in LOOPBACK_HOSTS):
        raise ConsumerError(f'{entry["url"]} must be an https URL')
    if url.query or url.fragment or url.username or url.password:
        raise ConsumerError(f'{entry["url"]} must carry no credentials, query or fragment')
    if not REPOSITORY.match(entry['repo']):
        raise ConsumerError(f'{entry["repo"]} is not an owner/name repository')
    if not BRANCH.match(entry['base']) or '..' in entry['base']:
        raise ConsumerError(f'{entry["base"]} is not a branch name')
    recipe = PurePosixPath(entry['recipe'])
    if recipe.is_absolute() or '..' in recipe.parts:
        raise ConsumerError(f'{entry["recipe"]} must be a path inside the consumer repository')
    message = entry.get('message', DEFAULT_MESSAGE)
    if not isinstance(message, str) or '{version}' not in message or '\n' in message:
        raise ConsumerError('message must be one line that contains {version}')
    merge_style = entry.get('merge_style', 'merge')
    if merge_style not in MERGE_STYLES:
        raise ConsumerError(f'merge_style must be one of {", ".join(sorted(MERGE_STYLES))}')
    return Consumer(entry['forge'], urllib.parse.urlunsplit(url), entry['repo'], entry['base'], str(recipe), message, merge_style)


def release_version(tag):
    match = TAG.match(tag)
    if not match:
        raise ConsumerError(f'{tag} is not a release tag such as v0.2.0 or v0.2.0-rc.1')
    return match['version']


class Forgejo:
    def __init__(self, consumer, token):
        self.api = f'{consumer.url}/api/v1'
        self.repo = consumer.repo
        self.token = token

    def call(self, method, path, body=None):
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(f'{self.api}{path}', data=data, method=method, headers={'Accept': 'application/json', 'Authorization': f'token {self.token}'})
        if data is not None:
            request.add_header('Content-Type', 'application/json')
        try:
            with urllib.request.urlopen(request, timeout=60) as response:
                payload = response.read()
                return response.status, json.loads(payload) if payload.strip() else None
        except urllib.error.HTTPError as error:
            detail = error.read().decode(errors='replace')[:500]
            error.close()
            return error.code, detail
        except urllib.error.URLError as error:
            raise ConsumerError(f'{method} {self.api}{path} is unreachable: {error.reason}') from error

    def expect(self, method, path, body=None, accepted=(200, 201)):
        status, payload = self.call(method, path, body)
        if status not in accepted:
            raise ConsumerError(f'{method} {path} answered HTTP {status}: {payload}')
        return payload

    def identity(self):
        user = self.expect('GET', '/user')
        login = user.get('login')
        if not login:
            raise ConsumerError('the token names no user')
        return login, user.get('email') or f'{login}@noreply.invalid'

    def open_pulls(self):
        pulls, page = [], 1
        while True:
            batch = self.expect('GET', f'/repos/{self.repo}/pulls?state=open&limit=50&page={page}') or []
            pulls.extend(batch)
            if len(batch) < 50:
                return pulls
            page += 1

    def upsert_pull(self, branch, base, title, body):
        existing = [pull for pull in self.open_pulls() if pull['head']['ref'] == branch and pull['base']['ref'] == base]
        if existing:
            number = existing[0]['number']
            self.expect('PATCH', f'/repos/{self.repo}/pulls/{number}', {'title': title, 'body': body})
            return number, False
        created = self.expect('POST', f'/repos/{self.repo}/pulls', {'base': base, 'head': branch, 'title': title, 'body': body})
        return created['number'], True

    def merge_when_checks_succeed(self, number, style, delay):
        body = {'Do': style, 'merge_when_checks_succeed': True, 'delete_branch_after_merge': True}
        for attempt in range(1, MERGE_ATTEMPTS + 1):
            status, payload = self.call('POST', f'/repos/{self.repo}/pulls/{number}/merge', body)
            if status in (200, 201):
                return 'merged' if status == 200 else 'scheduled'
            if status == 409:
                return 'already scheduled'
            if status != 405 or attempt == MERGE_ATTEMPTS:
                raise ConsumerError(f'asking to merge #{number} answered HTTP {status}: {payload}')
            time.sleep(delay)

    def close_superseded(self, prefix, branch, author, number):
        closed = []
        for pull in self.open_pulls():
            ref = pull['head']['ref']
            if ref.startswith(prefix) and ref != branch and pull['user']['login'] == author:
                self.expect('POST', f'/repos/{self.repo}/issues/{pull["number"]}/comments', {'body': f'Superseded by #{number}.'})
                self.expect('PATCH', f'/repos/{self.repo}/pulls/{pull["number"]}', {'state': 'closed'})
                closed.append(pull['number'])
        return closed


def git_environment(consumer, login, token):
    environment = dict(os.environ, GIT_TERMINAL_PROMPT='0')
    if token:
        count = int(environment.get('GIT_CONFIG_COUNT', '0'))
        credentials = base64.b64encode(f'{login}:{token}'.encode()).decode()
        environment[f'GIT_CONFIG_KEY_{count}'] = f'http.{consumer.url}/.extraHeader'
        environment[f'GIT_CONFIG_VALUE_{count}'] = f'Authorization: Basic {credentials}'
        environment['GIT_CONFIG_COUNT'] = str(count + 1)
    return environment


def git(checkout, *arguments, environment=None, capture=False):
    result = subprocess.run(['git', '-C', checkout, *arguments], env=environment, check=False, text=True, capture_output=capture)
    if result.returncode != 0:
        raise ConsumerError(f'git {arguments[0]} failed with exit {result.returncode}')
    return result.stdout if capture else None


def pull_request_body(consumer, tag, version, release_url):
    released = f'{PRODUCER} {tag} was released: {release_url}' if release_url else f'{PRODUCER} {tag} was released.'
    return (
        f'{released}\n\n'
        f'{PRODUCER}\'s release workflow ran `{consumer.recipe} {version}` on `{consumer.base}` and committed what it changed. '
        'A newer release supersedes this pull request with its own. The merge waits for the required checks of the base branch.'
    )


def deliver(consumer, tag, token, release_url, dry_run, retry_delay):
    version = release_version(tag)
    branch = f'{PRODUCER}/{version}'
    title = consumer.message.format(version=version)
    body = pull_request_body(consumer, tag, version, release_url)
    forge = Forgejo(consumer, token) if token else None
    login, email = forge.identity() if forge else (f'{PRODUCER}-release', f'{PRODUCER}-release@noreply.invalid')
    workspace = tempfile.mkdtemp(prefix=f'{PRODUCER}-consumer-')
    checkout = os.path.join(workspace, 'checkout')
    try:
        authenticated = git_environment(consumer, login, token)
        clone = subprocess.run(['git', 'clone', '--quiet', '--depth', '1', '--single-branch', '--branch', consumer.base, consumer.clone_url, checkout], env=authenticated, check=False)
        if clone.returncode != 0:
            raise ConsumerError(f'could not clone {consumer.clone_url} at {consumer.base}')
        git(checkout, 'checkout', '--quiet', '-B', branch)
        recipe = os.path.join(checkout, consumer.recipe)
        if not os.path.isfile(recipe) or not os.access(recipe, os.X_OK):
            raise ConsumerError(f'{consumer.repo} has no executable {consumer.recipe} on {consumer.base}')
        print(f'Running {consumer.recipe} {version} in {consumer.repo}', flush=True)
        ran = subprocess.run([recipe, version], cwd=checkout, env=dict(os.environ, GIT_TERMINAL_PROMPT='0'), check=False)
        if ran.returncode != 0:
            raise ConsumerError(f'{consumer.recipe} {version} failed with exit {ran.returncode}')
        if not git(checkout, 'status', '--porcelain', capture=True).strip():
            print(f'{consumer.repo} already consumes {tag}; nothing to propose')
            return 'unchanged'
        git(checkout, 'add', '--all')
        git(checkout, '-c', f'user.name={login}', '-c', f'user.email={email}', 'commit', '--quiet', '-m', title, '-m', body)
        print(git(checkout, 'show', '--stat', '--format=%an <%ae>%n%s', 'HEAD', capture=True), flush=True)
        if dry_run:
            print(f'Dry run: would push {branch}, open or update its pull request into {consumer.base} and ask {consumer.forge} to merge it ({consumer.merge_style}) when its checks succeed')
            return 'dry run'
        if forge is None:
            raise ConsumerError('RELEASE_CONSUMER_TOKEN is required to push and open the pull request')
        git(checkout, 'push', '--quiet', '--force', 'origin', f'HEAD:refs/heads/{branch}', environment=authenticated)
        number, created = forge.upsert_pull(branch, consumer.base, title, body)
        merge = forge.merge_when_checks_succeed(number, consumer.merge_style, retry_delay)
        closed = forge.close_superseded(f'{PRODUCER}/', branch, login, number)
        superseded = f', closed superseded {", ".join(f"#{n}" for n in closed)}' if closed else ''
        print(f'{consumer.repo}#{number} {"opened" if created else "updated"}, merge {merge}{superseded}')
        return 'opened' if created else 'updated'
    finally:
        shutil.rmtree(workspace, ignore_errors=True)


def main(argv):
    if len(argv) != 2:
        sys.exit('usage: release-consumers.py <release tag>, with one consumer entry in RELEASE_CONSUMER')
    token = os.environ.pop('RELEASE_CONSUMER_TOKEN', '')
    try:
        deliver(
            parse_consumer(os.environ.get('RELEASE_CONSUMER', '')),
            argv[1],
            token,
            os.environ.get('RELEASE_URL', ''),
            os.environ.get('RELEASE_CONSUMERS_DRY_RUN') == 'true',
            float(os.environ.get('RELEASE_CONSUMER_RETRY_DELAY', '10')),
        )
    except ConsumerError as error:
        sys.exit(f'release consumer: {error}')


if __name__ == '__main__':
    main(sys.argv)
