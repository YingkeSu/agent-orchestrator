import { beforeEach, describe, expect, it, vi } from "vitest";

const getMock = vi.hoisted(() => vi.fn());

vi.mock("../lib/api-client", () => ({
	apiClient: { GET: (...args: unknown[]) => getMock(...args) },
}));

import {
	fetchUsageRequestLog,
	usageRequestLogQueryKey,
	usageRequestLogQueryRoot,
	usageRequestLogPageSize,
} from "./useUsageRequestLog";

describe("global usage request log", () => {
	beforeEach(() => {
		getMock.mockReset().mockResolvedValue({ data: null });
	});

	it("fetches the newest page without filters", async () => {
		await fetchUsageRequestLog();

		expect(getMock).toHaveBeenCalledOnce();
		expect(getMock).toHaveBeenCalledWith("/api/v1/usage/log", {
			params: { query: { limit: usageRequestLogPageSize } },
		});
	});

	it("passes from/to and the before cursor as query params", async () => {
		await fetchUsageRequestLog("2026-09-01T00:00:00Z", "2026-09-08T00:00:00Z", undefined, undefined, 42);

		expect(getMock).toHaveBeenCalledWith("/api/v1/usage/log", {
			params: {
				query: {
					from: "2026-09-01T00:00:00Z",
					to: "2026-09-08T00:00:00Z",
					before: 42,
					limit: usageRequestLogPageSize,
				},
			},
		});
	});

	it("passes source/model filters as query params", async () => {
		await fetchUsageRequestLog(undefined, undefined, "codex_rollout", "gpt-5");

		expect(getMock).toHaveBeenCalledWith("/api/v1/usage/log", {
			params: {
				query: {
					source: "codex_rollout",
					model: "gpt-5",
					limit: usageRequestLogPageSize,
				},
			},
		});
	});

	it("omits the before cursor on the first page", async () => {
		await fetchUsageRequestLog(undefined, undefined, undefined, undefined, undefined);

		expect(getMock).toHaveBeenCalledWith("/api/v1/usage/log", {
			params: { query: { limit: usageRequestLogPageSize } },
		});
	});

	it("keeps filter variants under one shared query root", () => {
		expect(usageRequestLogQueryKey()).toEqual([
			...usageRequestLogQueryRoot,
			"unbounded",
			"unbounded",
			"any-source",
			"any-model",
		]);
		expect(usageRequestLogQueryKey("a", "b", "codex_rollout", "gpt-5")).toEqual([
			...usageRequestLogQueryRoot,
			"a",
			"b",
			"codex_rollout",
			"gpt-5",
		]);
	});
});
