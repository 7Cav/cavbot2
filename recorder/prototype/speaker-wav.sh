#!/usr/bin/env bash
# Builds the microphone file for the unattended test (README, "Unattended
# test"). Chrome plays it on a loop as a fake microphone: counting speech over
# faint pink noise, so Discord transmits without a break, then digital
# silence, so Discord transmits nothing. macOS only: it uses `say`.
#
#   ./speaker-wav.sh [output.wav]
#
# SPEECH_MIN and SILENCE_MIN set the two phases in minutes (default 8 and 18).
set -euo pipefail

out=${1:-out/speaker-loop.wav}
speech=$((${SPEECH_MIN:-8} * 60))
silence=$((${SILENCE_MIN:-18} * 60))

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Counting out loud makes a gap audible: a missing number is missing audio.
seq -s ', ' 1 900 >"$tmp/numbers.txt"
say -r 175 -f "$tmp/numbers.txt" -o "$tmp/count.aiff"

mkdir -p "$(dirname "$out")"
ffmpeg -v error -y \
	-i "$tmp/count.aiff" \
	-f lavfi -i "anoisesrc=color=pink:amplitude=0.01:sample_rate=24000:duration=$speech" \
	-f lavfi -i "anullsrc=channel_layout=mono:sample_rate=24000:duration=$silence" \
	-filter_complex "[0:a]aresample=24000,aformat=channel_layouts=mono,apad,atrim=duration=${speech}[s];[s][1:a]amix=inputs=2:normalize=0[talk];[talk][2:a]concat=n=2:v=0:a=1[out]" \
	-map '[out]' -c:a pcm_s16le "$out"

echo "$out: $((speech / 60)) min counting, then $((silence / 60)) min silence, looping"
