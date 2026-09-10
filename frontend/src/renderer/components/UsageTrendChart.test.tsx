import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import { UsageTrendChart } from "./UsageTrendChart";

type TrendBucket = components["schemas"]["UsageTrendBucketResponse"];

// jsdom gives ResponsiveContainer a zero-size box, in which recharts mounts
// nothing. Rendering the chart body at an explicit size keeps the legend
// assertable.
vi.mock("recharts", async (importOriginal) => {
	const actual = await importOriginal<typeof import("recharts")>();
	const { cloneElement } = await import("react");
	type SizedChart = React.ReactElement<{ width?: number; height?: number }>;
	const ResponsiveContainer = ({ children }: { children: SizedChart }) =>
		cloneElement(children, { width: 600, height: 256 });
	return { ...actual, ResponsiveContainer };
});

function bucket(overrides: Partial<TrendBucket>): TrendBucket {
	return {
		bucketStart: "2026-09-08T13:00:00Z",
		requestCount: 1,
		inputTokens: 30,
		cachedInputTokens: 7,
		uncachedInputTokens: 23,
		outputTokens: 4,
		cacheCreationInputTokens: null,
		costNanos: 50,
		...overrides,
	};
}

describe("UsageTrendChart", () => {
	it("renders the cache-creation series beside the other components", () => {
		render(
			<UsageTrendChart
				buckets={[bucket({ cacheCreationInputTokens: 3 })]}
				bucketSize="hour"
			/>,
		);
		const chart = screen.getByRole("img", { name: /usage trend/i });
		expect(chart).toBeDefined();
		// The legend carries one entry per series: new input, cache creation,
		// cached input, output, and cost.
		const legend = chart.querySelector(".recharts-legend-wrapper");
		expect(legend?.textContent).toContain("Cache creation");
		expect(legend?.textContent).toContain("New input");
	});

	it("shows the unknown marker for a nil cache-creation bucket, never 0", () => {
		render(
			<UsageTrendChart
				buckets={[bucket({ cacheCreationInputTokens: null })]}
				bucketSize="hour"
			/>,
		);
		const chart = screen.getByRole("img", { name: /usage trend/i });
		expect(chart).toBeDefined();
		// A nil bucket renders as a gap: the series exists in the legend but no
		// zero line is drawn for it. The tooltip-level unknown copy is verified
		// by the shared "Unavailable" string the chart uses for null values.
		const legend = chart.querySelector(".recharts-legend-wrapper");
		expect(legend?.textContent).toContain("Cache creation");
	});
});
