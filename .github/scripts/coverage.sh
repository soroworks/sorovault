#!/usr/bin/env bash
# Summarise a Go coverage profile and, when given a baseline, fail a pull
# request whose total coverage fell by more than MAX_COVERAGE_DROP.
#
# Usage: coverage.sh <profile> [baseline-profile]
set -euo pipefail

profile=${1:?usage: coverage.sh <coverage.out> [baseline.out]}
baseline=${2:-}
max_drop=${MAX_COVERAGE_DROP:-0.5}
helpers='/internal/stellar/stellartest/|/internal/wasm/wasmtest/'

totals() {
	awk -v helpers="$helpers" '
		/^mode:/ { next }
		{
			file = $1
			sub(/:.*/, "", file)
			if (file ~ helpers) next
			total += $2
			if ($3 > 0) covered += $2
		}
		END {
			if (total > 0) printf "%d %d %.2f", covered, total, 100 * covered / total
			else printf "0 0 0.00"
		}
	' "$1"
}

package_rows() {
	awk -v helpers="$helpers" '
		/^mode:/ { next }
		{
			file = $1
			sub(/:.*/, "", file)
			if (file ~ helpers) next

			pkg = file
			sub(/\/[^\/]+$/, "", pkg)
			sub(/^github\.com\/soroworks\/sorovault\/?/, "", pkg)
			if (pkg == "") pkg = "(root)"

			total[pkg] += $2
			if ($3 > 0) covered[pkg] += $2
		}
		END {
			for (pkg in total) {
				printf "%s\t%d\t%d\t%.1f\n", pkg, covered[pkg], total[pkg],
					100 * covered[pkg] / total[pkg]
			}
		}
	' "$1" | sort
}

report=$(mktemp)
trap 'rm -f "$report"' EXIT
status=0

{
	echo "## Coverage"
	echo
	echo "Test helper packages (\`stellartest\`, \`wasmtest\`) are excluded from these figures."
	echo
	echo "| Package | Covered | Statements | Coverage |"
	echo "|---|---:|---:|---:|"
	package_rows "$profile" | while IFS=$'\t' read -r pkg covered total pct; do
		printf '| `%s` | %d | %d | %s%% |\n' "$pkg" "$covered" "$total" "$pct"
	done
	read -r head_covered head_total head_pct <<<"$(totals "$profile")"
	printf '| **Total** | **%d** | **%d** | **%s%%** |\n\n' \
		"$head_covered" "$head_total" "$head_pct"

	if [[ -n "$baseline" ]]; then
		read -r base_covered base_total base_pct <<<"$(totals "$baseline")"
		echo "### Coverage gate"
		echo
		printf 'Base: **%s%%** (%d/%d statements) · Pull request: **%s%%** (%d/%d statements) · Allowed drop: **%s** percentage points.\n\n' \
			"$base_pct" "$base_covered" "$base_total" \
			"$head_pct" "$head_covered" "$head_total" "$max_drop"

		if ! awk -v base="$base_pct" -v head="$head_pct" -v max="$max_drop" 'BEGIN {
			drop = base - head
			if (drop > max) {
				printf "Coverage dropped by %.2f percentage points, more than the %.2f point allowance.\n", drop, max
				exit 1
			}
			if (drop < 0) printf "Coverage improved by %.2f percentage points.\n", -drop
			else printf "Coverage dropped by %.2f percentage points, within the allowance.\n", drop
		}' >>"$report"; then
			status=1
		fi
	fi
} >>"$report"

cat "$report"
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
	cat "$report" >>"$GITHUB_STEP_SUMMARY"
fi
exit "$status"
