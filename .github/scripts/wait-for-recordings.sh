#!/usr/bin/env bash
# .github/scripts/wait-for-recordings.sh <status URL> <poll interval seconds>
#
# The deploy's wait for recordings (spec #381, #394). Polls the panel's
# recording status endpoint and returns once it says no recording is
# running, so a deploy doesn't cut one. The deploy workflow bounds the wait
# with the step's timeout.

set -euo pipefail

url=${1:?usage: wait-for-recordings.sh <status URL> <poll interval seconds>}
interval=${2:?usage: wait-for-recordings.sh <status URL> <poll interval seconds>}

while :; do
    # curl retries a poll that fails, so a blip in the tunnel or the proxy
    # doesn't end the wait early. A poll that still fails leaves the answer
    # empty, and an answer that isn't JSON leaves the flag empty.
    answer=$(curl -fsS --max-time 10 --retry 5 --retry-delay 10 --retry-all-errors "$url" || true)
    flag=$(jq -r '.recording' <<<"$answer" 2>/dev/null || true)
    case $flag in
    true)
        echo "A recording is running. Checking again in ${interval}s."
        sleep "$interval"
        ;;
    false)
        echo "No recording is running."
        exit 0
        ;;
    *)
        # A build from before the status route sends the poll to sign in,
        # and recording stays off in production until after that build. A
        # poll that still fails after curl's retries means the bot is down,
        # so its recordings have already stopped, or its panel can't read
        # the database and may be recording. Either way the deploy may be
        # the fix, so it goes ahead rather than wait out the step's timeout.
        echo "::warning::$url gave no recording flag, so the deploy goes ahead."
        exit 0
        ;;
    esac
done
