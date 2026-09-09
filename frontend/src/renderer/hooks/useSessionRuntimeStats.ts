import { useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";
import { sessionUsageQueryRoot } from "./useSessionUsageSummaries";

export type SessionRuntimeStats = components["schemas"]["SessionRuntimeStatsResponse"];

export const sessionRuntimeStatsQueryKey = (sessionId: string) =>
	[...sessionUsageQueryRoot, "runtime-stats", sessionId] as const;

export async function fetchSessionRuntimeStats(sessionId: string): Promise<SessionRuntimeStats> {
	const { data, error } = await apiClient.GET("/api/v1/usage/sessions/{sessionId}/stats", {
		params: { path: { sessionId } },
	});
	if (error) throw error;
	return data;
}

export function useSessionRuntimeStats(sessionId: string, enabled = true) {
	return useQuery({
		queryKey: sessionRuntimeStatsQueryKey(sessionId),
		queryFn: () => fetchSessionRuntimeStats(sessionId),
		enabled: enabled && Boolean(sessionId),
		retry: 1,
	});
}
