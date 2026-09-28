#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 BASELINE CURRENT" >&2
  exit 2
fi

base=$1
current=$2

jq -n --slurpfile base "$base" --slurpfile current "$current" '
  def total($u): $u.cache_read_input_tokens + $u.cache_creation_input_tokens + $u.input_tokens;
  def rate($u): if total($u) == 0 then 0 else $u.cache_read_input_tokens / total($u) end;
  ($base[0].providers // {}) as $before |
  ($current[0].providers // {}) as $after |
  if ($before | length) == 0 then error("baseline has no providers") else . end |
  [ $before | to_entries[] as $p |
    ($after[$p.key] // error("missing provider: " + $p.key)) as $now |
    $p.value.cases | to_entries[] as $case |
    ($now.cases[$case.key] // error("missing case: " + $p.key + "/" + $case.key)) as $use |
    {provider: $p.key, case: $case.key, baseline: rate($case.value), current: rate($use)}
  ] as $rows |
  $rows[] |
  "\(.provider)/\(.case): baseline=\(.baseline) current=\(.current) delta=\(.current - .baseline)" |
  if any($rows[]; .current < .baseline) then error("cache hit rate regressed") else . end
' 
