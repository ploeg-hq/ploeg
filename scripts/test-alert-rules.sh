#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
chart="$root/ops/helm/ploeg"
ci="$chart/ci"
tests="${ALERT_RULES_TEST:-$ci/alert-rules-test.yaml}"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fail() { echo "$1" >&2; exit 1; }

helm template ploeg "$chart" -n ploeg -f "$ci/required-values.yaml" \
  --set monitoring.prometheusRule.enabled=true -s templates/prometheusrule.yaml \
  | sed -n '/^spec:$/,$p' | tail -n +2 | sed 's/^  //' > "$work/rules.yaml"
cp "$tests" "$work/alert-rules-test.yaml"

promtool check rules "$work/rules.yaml"

alerts=$(sed -n 's/^ *- alert: //p' "$work/rules.yaml")
[ -n "$alerts" ] || fail "the chart's PrometheusRule rendered no alerts"
group() { awk -v start="  - name: $1 " 'index($0, start) == 1 { inside = 1; print; next } /^  - name: / { inside = 0 } inside' "$tests"; }
for alert in $alerts; do
  cases=$(group "$alert")
  [ -n "$cases" ] || fail "$alert has no test group in $tests; name one '$alert <what it shows>'"
  grep -q "^ *alertname: $alert\$" <<< "$cases" || fail "the $alert test group never evaluates $alert"
  grep -q '^ *exp_alerts: \[\]$' <<< "$cases" || fail "$alert has no case where it must not fire"
  grep -q '^ *exp_alerts:$' <<< "$cases" || fail "$alert has no case where it fires"
done

promtool test rules "$work/alert-rules-test.yaml"
