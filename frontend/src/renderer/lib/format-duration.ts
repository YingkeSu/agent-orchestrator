/**
 * Human-friendly duration: "340ms", "1.2s", "3s", "20m". Returns null for
 * unknown values so callers render the explicit unknown marker — unknown is
 * never formatted as a fabricated zero (ADR 0005, Decision 3).
 */
export function formatDurationMs(ms: number | null | undefined): string | null {
	if (ms === null || ms === undefined) return null;
	if (ms < 1000) return `${ms}ms`;
	if (ms < 60_000) {
		// Drop a trailing ".0" so whole seconds read as "3s", not "3.0s".
		return `${(ms / 1000).toFixed(1).replace(/\.0$/, "")}s`;
	}
	return `${Math.round(ms / 60_000)}m`;
}
