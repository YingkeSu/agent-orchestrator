import { beforeEach, describe, expect, it, vi } from "vitest";

const getMock = vi.hoisted(() => vi.fn());

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: (...args: unknown[]) => getMock(...args) },
}));

import {
	fetchUsageSummary,
	usageSummaryQueryKey,
	usageSummaryQueryOptions,
	usageSummaryQueryRoot,
} from "./useUsageSummary";

describe("global usage summary", () => {
	beforeEach(() => {
		getMock.mockReset().mockResolvedValue({ data: null });
	});

	it("fetches the summary without query params when unbounded", async () => {
		await fetchUsageSummary();

		expect(getMock).toHaveBeenCalledOnce();
		expect(getMock).toHaveBeenCalledWith("/api/v1/usage/summary", { params: { query: undefined } });
	});

	it("passes the from/to range as query params", async () => {
		await fetchUsageSummary("2026-09-01T00:00:00Z", "2026-09-08T00:00:00Z");

		expect(getMock).toHaveBeenCalledWith("/api/v1/usage/summary", {
			params: { query: { from: "2026-09-01T00:00:00Z", to: "2026-09-08T00:00:00Z" } },
		});
	});

	it("omits a bound that was not supplied", async () => {
		await fetchUsageSummary(undefined, "2026-09-08T00:00:00Z");

		expect(getMock).toHaveBeenCalledWith("/api/v1/usage/summary", {
			params: { query: { to: "2026-09-08T00:00:00Z" } },
		});
	});

	it("passes source and model filters as query params", async () => {
		await fetchUsageSummary(undefined, undefined, "codex_rollout", "gpt-5.6");

		expect(getMock).toHaveBeenCalledWith("/api/v1/usage/summary", {
			params: { query: { source: "codex_rollout", model: "gpt-5.6" } },
		});
	});

	it("omits a filter that was not supplied", async () => {
		await fetchUsageSummary("2026-09-01T00:00:00Z", undefined, "claude_main");

		expect(getMock).toHaveBeenCalledWith("/api/v1/usage/summary", {
			params: { query: { from: "2026-09-01T00:00:00Z", source: "claude_main" } },
		});
	});

	it("keeps range and filter variants under one shared query root", () => {
		expect(usageSummaryQueryKey()).toEqual([...usageSummaryQueryRoot, "unbounded", "unbounded", "any", "any"]);
		expect(usageSummaryQueryKey("a", "b")).toEqual([...usageSummaryQueryRoot, "a", "b", "any", "any"]);
		expect(usageSummaryQueryKey("a", "b", "codex_rollout", "gpt-5.6")).toEqual([
			...usageSummaryQueryRoot,
			"a",
			"b",
			"codex_rollout",
			"gpt-5.6",
		]);
		expect(usageSummaryQueryOptions("a", "b", "codex_rollout", "gpt-5.6").queryKey).toEqual([
			...usageSummaryQueryRoot,
			"a",
			"b",
			"codex_rollout",
			"gpt-5.6",
		]);
	});
});
