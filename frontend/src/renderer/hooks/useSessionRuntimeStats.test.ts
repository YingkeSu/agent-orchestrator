import { beforeEach, describe, expect, it, vi } from "vitest";

const getMock = vi.hoisted(() => vi.fn());

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: (...args: unknown[]) => getMock(...args) },
}));

import {
	fetchSessionRuntimeStats,
	sessionRuntimeStatsQueryKey,
} from "./useSessionRuntimeStats";
import { sessionUsageQueryRoot } from "./useSessionUsageSummaries";

describe("session runtime stats", () => {
	beforeEach(() => {
		getMock.mockReset().mockResolvedValue({ data: null });
	});

	it("fetches the stats endpoint for the session", async () => {
		await fetchSessionRuntimeStats("sess-1");

		expect(getMock).toHaveBeenCalledOnce();
		expect(getMock).toHaveBeenCalledWith("/api/v1/usage/sessions/{sessionId}/stats", {
			params: { path: { sessionId: "sess-1" } },
		});
	});

	it("throws the envelope error so the query surfaces it", async () => {
		getMock.mockResolvedValue({ data: undefined, error: { message: "boom" } });
		await expect(fetchSessionRuntimeStats("sess-1")).rejects.toEqual({ message: "boom" });
	});

	it("nests the per-session key under the shared session-usage root for SSE invalidation", () => {
		expect(sessionRuntimeStatsQueryKey("sess-1")).toEqual([
			...sessionUsageQueryRoot,
			"runtime-stats",
			"sess-1",
		]);
	});
});
