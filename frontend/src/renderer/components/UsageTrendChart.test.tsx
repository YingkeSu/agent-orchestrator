import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import { UsageTrendChart } from "./UsageTrendChart";

// jsdom gives ResponsiveContainer a zero-size box, in which recharts mounts
// nothing. Rendering the chart body at an explicit size keeps the legend
// assertable.
vi.mock("recharts", async (importOriginal) => {
	const actual = await importOriginal<typeof import("recharts")>();
	const { cloneElement } = await import("react");
	const ResponsiveContainer = ({ children }: { children: React.ReactElement<{ width?: number; height?: number }> }) =>
		cloneElement(children, { width: 600, height: 256 });
	return { ...actual, ResponsiveContainer };
});

type TrendBucket = components["schemas"]["UsageTrendBucketResponse"];

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

	it("keeps the cache-creation legend when its bucket is unknown", () => {
		render(
			<UsageTrendChart
				buckets={[bucket({ cacheCreationInputTokens: null })]}
				bucketSize="hour"
			/>,
		);
		const chart = screen.getByRole("img", { name: /usage trend/i });
		expect(chart).toBeDefined();
		// Missing measurements must not remove the series from the legend.
		const legend = chart.querySelector(".recharts-legend-wrapper");
		expect(legend?.textContent).toContain("Cache creation");
	});
});
