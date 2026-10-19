#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
fixture=$(mktemp -d)
server_pid=
cleanup() {
  [[ -z "$server_pid" ]] || kill "$server_pid" 2>/dev/null || true
  rm -rf "$fixture"
}
trap cleanup EXIT
fail() { echo "release consumers: $*" >&2; exit 1; }

cat > "$fixture/forge.py" <<'PY'
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

TOKEN = 'token consumer-secret'
BOT = 'release-bot'
log_path = sys.argv[1]
pulls = {
    1: {'number': 1, 'state': 'open', 'title': 'old', 'head': {'ref': 'ploeg/0.1.0-rc.1'}, 'base': {'ref': 'development'}, 'user': {'login': BOT}},
    2: {'number': 2, 'state': 'open', 'title': 'a person', 'head': {'ref': 'ploeg/by-hand'}, 'base': {'ref': 'development'}, 'user': {'login': 'someone'}},
}
merges = {}
comments = []


class Forge(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def reply(self, status, body=None):
        payload = b'' if body is None else json.dumps(body).encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def handle_any(self, method):
        length = int(self.headers.get('Content-Length') or 0)
        body = json.loads(self.rfile.read(length)) if length else None
        path = urlsplit(self.path).path
        if path == '/state':
            return self.reply(200, {'pulls': pulls, 'merges': merges, 'comments': comments})
        with open(log_path, 'a') as log:
            log.write(json.dumps({'method': method, 'path': path, 'body': body}) + '\n')
        if self.headers.get('User-Agent', '').startswith('Python-urllib'):
            return self.reply(403, {'message': 'error code: 1010'})
        if self.headers.get('Authorization') != TOKEN:
            return self.reply(401, {'message': 'unauthorized'})
        parts = path.removeprefix('/api/v1/').split('/')
        if parts == ['user']:
            return self.reply(200, {'login': BOT, 'email': 'release-bot@example.invalid'})
        if parts[:3] != ['repos', 'acme', 'app']:
            return self.reply(404, {'message': 'not found'})
        rest = parts[3:]
        if rest == ['pulls'] and method == 'GET':
            return self.reply(200, [pull for pull in pulls.values() if pull['state'] == 'open'])
        if rest == ['pulls'] and method == 'POST':
            number = max(pulls) + 1
            pulls[number] = {'number': number, 'state': 'open', 'title': body['title'], 'body': body['body'], 'head': {'ref': body['head']}, 'base': {'ref': body['base']}, 'user': {'login': BOT}}
            return self.reply(201, pulls[number])
        if len(rest) == 2 and rest[0] == 'pulls' and method == 'PATCH':
            pulls[int(rest[1])].update(body)
            return self.reply(201, pulls[int(rest[1])])
        if len(rest) == 3 and rest[0] == 'pulls' and rest[2] == 'merge':
            number = rest[1]
            merges.setdefault(number, []).append(body)
            calls = len(merges[number])
            return self.reply({1: 405, 2: 201}.get(calls, 409), None if calls == 2 else {'message': 'try again later' if calls == 1 else 'already scheduled'})
        if len(rest) == 3 and rest[0] == 'issues' and rest[2] == 'comments':
            comments.append({'number': int(rest[1]), 'body': body['body']})
            return self.reply(201, {'id': len(comments)})
        return self.reply(404, {'message': 'not found'})

    def do_GET(self):
        self.handle_any('GET')

    def do_POST(self):
        self.handle_any('POST')

    def do_PATCH(self):
        self.handle_any('PATCH')


server = ThreadingHTTPServer(('127.0.0.1', 0), Forge)
print(server.server_address[1], flush=True)
server.serve_forever()
PY

python3 -u "$fixture/forge.py" "$fixture/requests.log" > "$fixture/port" &
server_pid=$!
for _ in $(seq 50); do [[ -s "$fixture/port" ]] && break; sleep 0.1; done
port=$(head -n 1 "$fixture/port")
[[ -n "$port" ]] || fail 'the fake forge did not start'
forge="http://127.0.0.1:$port"

git init --quiet --bare -b development "$fixture/app.git"
git clone --quiet "$fixture/app.git" "$fixture/seed" 2>/dev/null
cd "$fixture/seed"
git config user.name 'Consumer seed'
git config user.email seed@example.invalid
mkdir -p scripts
cat > scripts/consume.sh <<'SH'
#!/bin/sh
set -eu
[ "$1" != 0.1.0-rc.9 ] || exit 4
env > "$RECIPE_ENVIRONMENT_DUMP"
printf '%s\n' "$1" > pin.txt
SH
chmod +x scripts/consume.sh
printf '0.1.0-rc.1\n' > pin.txt
git add scripts/consume.sh pin.txt
git commit --quiet -m seed
git push --quiet origin development

export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0="url.file://$fixture/app.git.insteadOf" GIT_CONFIG_VALUE_0="$forge/acme/app.git"
export RECIPE_ENVIRONMENT_DUMP="$fixture/recipe.env" RELEASE_CONSUMER_RETRY_DELAY=0 RELEASE_URL=https://example.invalid/releases/v0.1.0-rc.2
consumer() { printf '{"forge":"forgejo","url":"%s","repo":"acme/app","base":"development","recipe":"scripts/consume.sh","message":"fix(pin): move to {version}"%s}' "$forge" "${1:-}"; }
run() { RELEASE_CONSUMER="$1" RELEASE_CONSUMER_TOKEN=consumer-secret python3 "$root/scripts/release-consumers.py" "$2"; }
state() { python3 -c 'import json,sys,urllib.request; print(json.dumps(eval(sys.argv[2], {}, {"s": json.load(urllib.request.urlopen(sys.argv[1] + "/state"))})))' "$forge" "$1"; }
requests() { python3 -c 'import json,sys; print(sum(1 for line in open(sys.argv[1]) if (r := json.loads(line)) and eval(sys.argv[2], {}, {"r": r})))' "$fixture/requests.log" "$1"; }

run "$(consumer)" v0.1.0-rc.2 > "$fixture/first.out"
[[ "$(git --git-dir="$fixture/app.git" show ploeg/0.1.0-rc.2:pin.txt)" == 0.1.0-rc.2 ]] || fail 'the branch does not carry what the recipe changed'
[[ "$(git --git-dir="$fixture/app.git" log -1 --format='%an <%ae>|%s' ploeg/0.1.0-rc.2)" == 'release-bot <release-bot@example.invalid>|fix(pin): move to 0.1.0-rc.2' ]] || fail 'the commit is not the bot'\''s, with the configured message'
[[ "$(git --git-dir="$fixture/app.git" rev-parse development)" == "$(git -C "$fixture/seed" rev-parse HEAD)" ]] || fail 'the base branch moved'
grep -q consumer-secret "$fixture/recipe.env" && fail 'the recipe saw the consumer token'
[[ "$(state '[p["title"] for p in s["pulls"].values() if p["head"]["ref"] == "ploeg/0.1.0-rc.2"]')" == '["fix(pin): move to 0.1.0-rc.2"]' ]] || fail 'no pull request was opened for the new branch'
grep -q 'https://example.invalid/releases/v0.1.0-rc.2' <<<"$(state '[p["body"] for p in s["pulls"].values() if p["number"] == 3]')" || fail 'the pull request does not link the release'
[[ "$(state 's["merges"]["3"]')" == '[{"Do": "merge", "merge_when_checks_succeed": true, "delete_branch_after_merge": true}, {"Do": "merge", "merge_when_checks_succeed": true, "delete_branch_after_merge": true}]' ]] || fail 'the merge was not retried after 405 and scheduled when checks succeed'
[[ "$(state '[s["pulls"]["1"]["state"], s["pulls"]["2"]["state"]]')" == '["closed", "open"]' ]] || fail 'the bot'\''s older pull request must close and a person'\''s must stay open'
[[ "$(state 's["comments"]')" == '[{"number": 1, "body": "Superseded by #3."}]' ]] || fail 'the superseded pull request names its successor'
grep -q 'acme/app#3 opened, merge scheduled, closed superseded #1' "$fixture/first.out" || fail 'the summary does not name the pull request'

run "$(consumer)" v0.1.0-rc.2 > "$fixture/rerun.out"
[[ "$(state 'len(s["pulls"])')" == 3 ]] || fail 'a rerun opened a second pull request'
grep -q 'acme/app#3 updated, merge already scheduled' "$fixture/rerun.out" || fail 'a rerun must update the open pull request and accept an existing schedule'
[[ "$(requests 'r["method"] == "PATCH" and r["path"].endswith("/pulls/3")')" == 1 ]] || fail 'a rerun did not update the pull request'

git -C "$fixture/seed" pull --quiet --rebase origin development
printf '0.1.0-rc.4\n' > "$fixture/seed/pin.txt"
git -C "$fixture/seed" commit --quiet -am 'already pinned'
git -C "$fixture/seed" push --quiet origin development
before=$(wc -l < "$fixture/requests.log")
run "$(consumer)" v0.1.0-rc.4 > "$fixture/unchanged.out"
grep -q 'already consumes v0.1.0-rc.4' "$fixture/unchanged.out" || fail 'an unchanged consumer must be reported'
[[ "$(tail -n +"$((before + 1))" "$fixture/requests.log" | grep -vc '"path": "/api/v1/user"' || true)" == 0 ]] || fail 'an unchanged consumer must not be written to'
git --git-dir="$fixture/app.git" rev-parse --verify --quiet refs/heads/ploeg/0.1.0-rc.4 >/dev/null && fail 'an unchanged consumer got a branch'

before=$(wc -l < "$fixture/requests.log")
RELEASE_CONSUMERS_DRY_RUN=true run "$(consumer)" v0.1.0-rc.5 > "$fixture/dry.out"
grep -q '^Dry run: would push ploeg/0.1.0-rc.5' "$fixture/dry.out" || fail 'a dry run must say what it would do'
[[ "$(tail -n +"$((before + 1))" "$fixture/requests.log" | grep -vc '"path": "/api/v1/user"' || true)" == 0 ]] || fail 'a dry run wrote to the forge'
git --git-dir="$fixture/app.git" rev-parse --verify --quiet refs/heads/ploeg/0.1.0-rc.5 >/dev/null && fail 'a dry run pushed'

rejects() {
  local output
  if output=$(run "$1" "$2" 2>&1); then fail "accepted $1 $2"; fi
  grep -qF -- "$3" <<<"$output" || fail "rejected $1 $2 for the wrong reason: $output"
}
rejects "$(consumer)" v0.1.0-rc.9 'scripts/consume.sh 0.1.0-rc.9 failed with exit 4'
git --git-dir="$fixture/app.git" rev-parse --verify --quiet refs/heads/ploeg/0.1.0-rc.9 >/dev/null && fail 'a failing recipe pushed'
valid="{\"forge\":\"forgejo\",\"url\":\"$forge\",\"repo\":\"acme/app\",\"base\":\"development\""
rejects "${valid/forgejo/gitlab},\"recipe\":\"r.sh\"}" v0.1.0-rc.6 "forge 'gitlab' is not supported"
rejects "${valid/$forge/http://forge.example},\"recipe\":\"r.sh\"}" v0.1.0-rc.6 'must be an https URL'
rejects "${valid/$forge/https://user:secret@forge.example},\"recipe\":\"r.sh\"}" v0.1.0-rc.6 'must carry no credentials'
rejects "$valid,\"recipe\":\"../app.git/hooks/x\"}" v0.1.0-rc.6 'must be a path inside the consumer repository'
rejects "$valid,\"recipe\":\"/bin/true\"}" v0.1.0-rc.6 'must be a path inside the consumer repository'
rejects "$valid,\"recipe\":\"pin.txt\"}" v0.1.0-rc.6 'has no executable pin.txt'
rejects "${valid/acme\/app/acme},\"recipe\":\"r.sh\"}" v0.1.0-rc.6 'is not an owner/name repository'
rejects "$valid,\"recipe\":\"r.sh\",\"message\":\"no placeholder\"}" v0.1.0-rc.6 'must be one line that contains {version}'
rejects "$valid,\"recipe\":\"r.sh\",\"token\":\"inline\"}" v0.1.0-rc.6 'unknown fields: token'
rejects "$valid}" v0.1.0-rc.6 'lacks recipe'
for tag in 0.1.0 v1 v0.1.0-rc.6/../x latest; do rejects "$(consumer)" "$tag" 'is not a release tag'; done
echo 'Release consumer delivery, reruns, supersession, dry runs and input checks passed'
