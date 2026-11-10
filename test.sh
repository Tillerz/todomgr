#!/usr/bin/env bash
# Runs all tests and prints one line per check:
#   1. Go tests (store, reminders, export, HTTP API, MCP), in memory, no network.
#   2. Notes preview (needs node): markdown renders, HTML and script links in notes stay harmless.
#      Page smoke test (node): the whole page script draws the tree, Today, Next 7 days, Stats and every
#      detail panel with the demo data, without a script error.
#   3. End-to-end: builds the program, starts it on a free port with an empty temp
#      data folder, and checks the CLI, the API, the MCP stdio bridge, the lock and a restart.
# Only 127.0.0.1 is contacted; Discord is never called. Exit code 1 if anything failed.
# Usage: ./test.sh
set -uo pipefail
cd "$(dirname "$0")"
command -v go > /dev/null || { echo "go not found in PATH (is Go installed, and its bin folder in PATH?)"; exit 1; }

pass=0 fail=0
ok()  { printf '  \033[32m✔\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf '  \033[31m✘\033[0m %s   (%s)\n' "$1" "$2"; fail=$((fail + 1)); }
# check NAME CMD...: passes when CMD succeeds
check() { local name=$1; shift; if out=$("$@" 2>&1); then ok "$name"; else out=$(tail -1 <<< "$out"); bad "$name" "${out:-check failed}"; fi; }

tmp=$(mktemp -d)
trap 'kill $pid 2>/dev/null; wait $pid 2>/dev/null; rm -rf "$tmp"' EXIT
pid=""

# ---------------------------------------------------------------- 1. Go tests
echo "Go tests"
go test -count=1 -v ./... > "$tmp/go.log" 2>&1
while read -r status name _; do
  [ "$status" = "PASS:" ] && ok "$name" || bad "$name" "see go test -run $name -v"
done < <(sed -n 's/^--- \(PASS:\|FAIL:\) /\1 /p' "$tmp/go.log")
if grep -q -E '^(FAIL|panic:|.*\[build failed\])' "$tmp/go.log" && ! grep -q '^--- FAIL' "$tmp/go.log"; then
  bad "go test" "$(grep -m1 -E 'FAIL|panic|cannot|undefined' "$tmp/go.log")"
fi

# ---------------------------------------------------------------- 2. notes preview
echo "Notes preview (app/web/index.html with marked, in node)"
if command -v node > /dev/null; then
  node app/notes_test.js > "$tmp/notes.log" 2>&1; rc=$?
  while read -r status rest; do
    [ "$status" = "PASS" ] && ok "$rest" || bad "${rest%%: *}" "${rest#*: }"
  done < <(grep -E '^(PASS|FAIL) ' "$tmp/notes.log")
  # a script that stops in the middle (exit code, no FAIL line) would otherwise just lose its later checks
  [ "$rc" -ne 0 ] && ! grep -q '^FAIL ' "$tmp/notes.log" && bad "notes_test.js stopped early" "$(grep -m1 -E 'Error' "$tmp/notes.log" || tail -1 "$tmp/notes.log")"
  grep -q -E '^(PASS|FAIL) ' "$tmp/notes.log" || bad "notes_test.js" "$(tail -1 "$tmp/notes.log")"
  node app/page_check.js > "$tmp/page.log" 2>&1; rc=$?
  while read -r status rest; do
    [ "$status" = "PASS" ] && ok "$rest" || bad "${rest%%: *}" "${rest#*: }"
  done < <(grep -E '^(PASS|FAIL) ' "$tmp/page.log")
  [ "$rc" -ne 0 ] && ! grep -q '^FAIL ' "$tmp/page.log" && bad "page_check.js stopped early" "$(grep -m1 -E 'Error' "$tmp/page.log" || tail -1 "$tmp/page.log")"
  grep -q -E '^(PASS|FAIL) ' "$tmp/page.log" || bad "page_check.js" "$(tail -1 "$tmp/page.log")"
else
  bad "notes preview" "node not found in PATH"
fi

# ---------------------------------------------------------------- 3. end-to-end
echo "End-to-end (real program, temp data, 127.0.0.1 only)"
bin=$tmp/todo data=$tmp/data
if ! go build -o "$bin" ./app 2> "$tmp/build.log"; then
  bad "build" "$(tail -1 "$tmp/build.log")"
  echo "---"; echo "$pass passed, $fail failed"; exit 1
fi
ok "build"

port=17437
while (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; do port=$((port + 1)); done

start() { # start the server in the background and wait until it answers
  "$bin" serve --data "$data" --port "$port" > "$tmp/serve.log" 2>&1 &
  pid=$!
  for _ in $(seq 50); do
    curl -s -o /dev/null "http://127.0.0.1:$port/" && return 0
    sleep 0.1
  done
  return 1
}
stop() { kill -TERM "$pid" && wait "$pid"; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; } # HTTP status of a request
todo() { "$bin" --data "$data" "$@"; }

if start; then ok "server starts on port $port"; else bad "server starts on port $port" "$(tail -1 "$tmp/serve.log")"; exit 1; fi
token=$(todo token)
api=http://127.0.0.1:$port

version=$(sed -n 's/^[[:space:]]*Version = "\(.*\)"$/\1/p' app/main.go)
check "todo version shows v$version" grep -q "v$version" <<< "$("$bin" version)"
check "API without token is refused (401)" test "$(code "$api/api/info")" = 401
check "API with token works (200)" test "$(code -H "Authorization: Bearer $token" "$api/api/info")" = 200
check "foreign Host header is refused (403)" test "$(code -H "Host: evil.example" "$api/")" = 403
check "web page loads and sets the cookie" grep -qi "^set-cookie: todomgr_[0-9a-f]\{8\}=" <<< "$(curl -s -D - -o /dev/null "$api/")"

added=$(todo add "Test entry #e2e")
id=$(awk '{print $2}' <<< "$added")
check "todo add" grep -q "^added " <<< "$added"
check "todo list finds it" grep -q "Test entry" <<< "$(todo list)"
check "todo done" grep -q -- "-> done" <<< "$(todo done "$id")"
check "todo export .csv" bash -c "'$bin' --data '$data' export '$tmp/x.csv' && grep -q 'Test entry' '$tmp/x.csv'"
check "todo export .ics" bash -c "'$bin' --data '$data' export '$tmp/x.ics' && grep -q 'BEGIN:VCALENDAR' '$tmp/x.ics'"
check "todo export .json" bash -c "'$bin' --data '$data' export '$tmp/x.json' && grep -q 'Test entry' '$tmp/x.json'"
check "todo backup" todo backup
check "todo import needs --yes" bash -c "! '$bin' --data '$data' import '$tmp/x.json'"
check "todo import --yes" todo import "$tmp/x.json" --yes
check "todo import .csv adds entries" grep -q '^added [0-9]* entries to "Import: x.csv' <<< "$(todo import "$tmp/x.csv")"
printf 'BEGIN:VCALENDAR\r\nBEGIN:VTODO\r\nUID:a\r\nSUMMARY:Calendar task\r\nDUE:20301224\r\nEND:VTODO\r\nEND:VCALENDAR\r\n' > "$tmp/y.ics"
check "todo import .ics adds entries" bash -c "'$bin' --data '$data' import '$tmp/y.ics' > /dev/null && '$bin' --data '$data' list 'Calendar task' | grep -q '2030-12-24'"

check "todo mcp refuses while MCP is off" bash -c "! '$bin' --data '$data' mcp < /dev/null"
check "MCP can be switched on" test "$(code -X PUT -H "Authorization: Bearer $token" -H "Content-Type: application/json" \
  -d '{"mcp":{"enabled":true}}' "$api/api/settings")" = 200
mcp_out=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_entries","arguments":{"query":"Test"}}}' | todo mcp)
check "todo mcp: initialize" grep -q '"serverInfo"' <<< "$mcp_out"
check "todo mcp: tools/list" grep -q '"create_entry"' <<< "$mcp_out"
check "todo mcp: list_entries finds the entry" grep -q 'Test entry' <<< "$mcp_out"

timeout 5 "$bin" serve --data "$data" --port $((port + 100)) > /dev/null 2>&1; rc=$?
check "second server on the same data is refused (lock)" test "$rc" -ne 0 -a "$rc" -ne 124

if stop; then ok "server stops cleanly (TERM, like Ctrl+C)"; else bad "server stops cleanly (TERM, like Ctrl+C)" "exit code $?"; fi
check "lock file is removed on stop" test ! -e "$data/todomgr.lock"
if start; then ok "server starts again"; else bad "server starts again" "$(tail -1 "$tmp/serve.log")"; fi
check "data is still there after restart" grep -q "Test entry" <<< "$(todo list --all)"
stop

echo "---"
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
