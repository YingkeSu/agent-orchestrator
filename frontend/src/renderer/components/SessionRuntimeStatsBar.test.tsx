import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { SessionRuntimeStats } from "../hooks/useSessionRuntimeStats";
import {
	formatRuntimeDurationMs,
	SessionRuntimeStatsBar,
} from "./SessionRuntimeStatsBar";
import { TooltipProvider } from "./ui/tooltip";

const fullStats: SessionRuntimeStats = {
	sessionId: "sess-1",
	rounds: 11,
	steps: 117,
	llmMs: 1_217_000,
	toolMs: 503_000,
	firstTokenAvgMs: 4000,
	firstTokenCoverage: { covered: 12, total: 15 },
	outputTokensPerSecond: 84,
	cacheHitRate: 0.97,
	totals: {
		inputTokens: 3_000_000,
		cachedInputTokens: 2_910_000,
		uncachedInputTokens: 90_000,
		outputTokens: 3_300_000,
		processedTokens: 6_300_000,
		cacheReadTokens: 2_910_000,
		estimatedCost: null,
	},
};

function renderBar(stats: SessionRuntimeStats) {
	return render(
		<TooltipProvider>
			<SessionRuntimeStatsBar stats={stats} />
		</TooltipProvider>,
	);
}

describe("SessionRuntimeStatsBar", () => {
	it("renders the full runtime strip", () => {
		renderBar(fullStats);

		const bar = screen.getByTestId("session-runtime-stats");
		expect(bar).toHaveTextContent("11 rounds");
		expect(bar).toHaveTextContent("117 steps");
		expect(bar).toHaveTextContent("LLM 20m 17s");
		expect(bar).toHaveTextContent("tool calls 8m 23s");
		expect(bar).toHaveTextContent("first token avg 4s");
		expect(bar).toHaveTextContent("84 tok/s");
		expect(bar).toHaveTextContent("cache hit 97.0%");
		expect(bar).toHaveTextContent("total 6.3M tok");
	});

	it("renders unknown markers for uncertified timing, never zeros", () => {
		renderBar({
			...fullStats,
			rounds: null,
			llmMs: null,
			toolMs: null,
			firstTokenAvgMs: null,
			firstTokenCoverage: { covered: 0, total: 0 },
			outputTokensPerSecond: null,
			cacheHitRate: null,
		});

		const bar = screen.getByTestId("session-runtime-stats");
		expect(bar).toHaveTextContent("117 steps");
		expect(bar).toHaveTextContent("LLM —");
		expect(bar).toHaveTextContent("tool calls —");
		expect(bar).toHaveTextContent("first token avg —");
		expect(bar).toHaveTextContent("— tok/s");
		expect(bar).toHaveTextContent("cache hit —");
		expect(bar).not.toHaveTextContent("0 rounds");
	});

	// A truncated payload can drop fields entirely; a missing field is as
	// unknown as an explicit null, so it renders the marker rather than
	// crashing or rendering a partial number.
	it("treats fields missing from a truncated payload as unknown, like nulls", () => {
		const truncated: SessionRuntimeStats = { ...fullStats };
		delete (truncated as Partial<SessionRuntimeStats>).outputTokensPerSecond;
		delete (truncated as Partial<SessionRuntimeStats>).cacheHitRate;

		renderBar(truncated);

		const bar = screen.getByTestId("session-runtime-stats");
		expect(bar).toHaveTextContent("— tok/s");
		expect(bar).toHaveTextContent("cache hit —");
	});

	it("renders a known zero as a zero, distinct from the unknown marker", () => {
		renderBar({ ...fullStats, toolMs: 0 });

		expect(screen.getByTestId("session-runtime-stats")).toHaveTextContent("tool calls 0s");
	});

	it("formats sub-second durations without inventing a second", () => {
		expect(formatRuntimeDurationMs(0)).toBe("0s");
		expect(formatRuntimeDurationMs(1)).toBe("<1s");
		expect(formatRuntimeDurationMs(999)).toBe("<1s");
		expect(formatRuntimeDurationMs(1_000)).toBe("1s");
		expect(formatRuntimeDurationMs(59_400)).toBe("59s");
		expect(formatRuntimeDurationMs(60_000)).toBe("1m");
		expect(formatRuntimeDurationMs(1_217_000)).toBe("20m 17s");
		expect(formatRuntimeDurationMs(3_600_000)).toBe("1h");
		expect(formatRuntimeDurationMs(3_900_000)).toBe("1h 5m");
	});
});
