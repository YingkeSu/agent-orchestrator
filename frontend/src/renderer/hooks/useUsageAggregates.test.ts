import { beforeEach, describe, expect, it, vi } from "vitest";

const getMock = vi.hoisted(() => vi.fn());

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: (...args: unknown[]) => getMock(...args) },
}));

import {
	fetchUsageModelStats,
	fetchUsageProviderStats,
	usageModelStatsQueryKey,
	usageProviderStatsQueryKey,
	usageModelStatsQueryRoot,
	usageProviderStatsQueryRoot,
} from "./useUsageAggregates";

describe("usage aggregate tables", () => {
	beforeEach(() => {
		getMock.mockReset().mockResolvedValue({ data: null });
	});

	it("fetches model stats without query params when unbounded and unfiltered", async () => {
		await fetchUsageModelStats();

		expect(getMock).toHaveBeenCalledOnce();
		expect(getMock).toHaveBeenCalledWith("/api/v1/usage/models", { params: { query: undefined } });
	});

	it("passes range and source/model filters as query params", async () => {
		await fetchUsageModelStats("2026-09-01T00:00:00Z", "2026-09-08T00:00:00Z", "claude_main", "claude-x");

		expect(getMock).toHaveBeenCalledWith("/api/v1/usage/models", {
			params: {
				query: {
					from: "2026-09-01T00:00:00Z",
					to: "2026-09-08T00:00:00Z",
					source: "claude_main",
					model: "claude-x",
				},
			},
		});
	});

	it("omits filters that were not supplied", async () => {
		await fetchUsageProviderStats(undefined, "2026-09-08T00:00:00Z", undefined, "gpt-5");

		expect(getMock).toHaveBeenCalledWith("/api/v1/usage/providers", {
			params: { query: { to: "2026-09-08T00:00:00Z", model: "gpt-5" } },
		});
	});

	it("keeps range and filter variants under one shared query root", () => {
		expect(usageModelStatsQueryKey("a", "b", "codex_rollout", "gpt-5")).toEqual([
			...usageModelStatsQueryRoot,
			"a",
			"b",
			"codex_rollout",
			"gpt-5",
		]);
		expect(usageProviderStatsQueryKey()).toEqual([...usageProviderStatsQueryRoot, "unbounded", "unbounded", "all", "all"]);
	});
});