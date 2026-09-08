import { useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";

export type UsageSummary = components["schemas"]["UsageSummaryResponse"];

export const usageSummaryQueryRoot = ["usage-summary"] as const;

export const usageSummaryQueryKey = (from?: string, to?: string) =>
	[...usageSummaryQueryRoot, from ?? "unbounded", to ?? "unbounded"] as const;

export async function fetchUsageSummary(from?: string, to?: string): Promise<UsageSummary | null> {
	const { data, error } = await apiClient.GET("/api/v1/usage/summary", {
		params: {
			query:
				from || to
					? { ...(from ? { from } : {}), ...(to ? { to } : {}) }
					: undefined,
		},
	});
	if (error) throw error;
	return data ?? null;
}

export function usageSummaryQueryOptions(from?: string, to?: string) {
	return {
		queryKey: usageSummaryQueryKey(from, to),
		queryFn: () => fetchUsageSummary(from, to),
		retry: 1,
	};
}

export function useUsageSummary(from?: string, to?: string) {
	return useQuery(usageSummaryQueryOptions(from, to));
}
