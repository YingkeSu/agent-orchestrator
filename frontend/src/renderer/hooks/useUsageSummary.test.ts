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

	it("keeps range variants under one shared query root", () => {
		expect(usageSummaryQueryKey()).toEqual([...usageSummaryQueryRoot, "unbounded", "unbounded"]);
		expect(usageSummaryQueryKey("a", "b")).toEqual([...usageSummaryQueryRoot, "a", "b"]);
		expect(usageSummaryQueryOptions("a", "b").queryKey).toEqual([...usageSummaryQueryRoot, "a", "b"]);
	});
});
