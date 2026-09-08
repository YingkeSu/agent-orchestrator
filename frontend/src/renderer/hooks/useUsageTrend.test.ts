import { beforeEach, describe, expect, it, vi } from "vitest";

const getMock = vi.hoisted(() => vi.fn());

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: (...args: unknown[]) => getMock(...args) },
}));

import {
	fetchUsageTrend,
	usageTrendQueryKey,
	usageTrendQueryOptions,
	usageTrendQueryRoot,
} from "./useUsageTrend";

describe("usage trend", () => {
	beforeEach(() => {
		getMock.mockReset().mockResolvedValue({ data: null });
	});

	it("requires a bounded range and passes the bucket through", async () => {
		await fetchUsageTrend("2026-09-08T00:00:00Z", "2026-09-08T23:59:59Z", "hour");

		expect(getMock).toHaveBeenCalledOnce();
		expect(getMock).toHaveBeenCalledWith("/api/v1/usage/trend", {
			params: {
				query: {
					from: "2026-09-08T00:00:00Z",
					to: "2026-09-08T23:59:59Z",
					bucket: "hour",
				},
			},
		});
	});

	it("passes source and model filters when supplied", async () => {
		await fetchUsageTrend("2026-09-08T00:00:00Z", "2026-09-08T23:59:59Z", "day", "codex_rollout", "gpt-5.6");

		expect(getMock).toHaveBeenCalledWith("/api/v1/usage/trend", {
			params: {
				query: {
					from: "2026-09-08T00:00:00Z",
					to: "2026-09-08T23:59:59Z",
					bucket: "day",
					source: "codex_rollout",
					model: "gpt-5.6",
				},
			},
		});
	});

	it("omits optional bucket and filters when absent", async () => {
		await fetchUsageTrend("2026-09-08T00:00:00Z", "2026-09-08T23:59:59Z");

		expect(getMock).toHaveBeenCalledWith("/api/v1/usage/trend", {
			params: {
				query: {
					from: "2026-09-08T00:00:00Z",
					to: "2026-09-08T23:59:59Z",
				},
			},
		});
	});

	it("keeps range/bucket/filter variants under one shared query root", () => {
		expect(usageTrendQueryKey("a", "b")).toEqual([...usageTrendQueryRoot, "a", "b", "hour", "", ""]);
		expect(usageTrendQueryKey("a", "b", "day", "codex_rollout", "gpt-5.6")).toEqual([
			...usageTrendQueryRoot,
			"a",
			"b",
			"day",
			"codex_rollout",
			"gpt-5.6",
		]);
		expect(usageTrendQueryOptions("a", "b").queryKey).toEqual([...usageTrendQueryRoot, "a", "b", "hour", "", ""]);
	});
});