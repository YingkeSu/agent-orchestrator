import { useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";

export type UsageSummary = components["schemas"]["UsageSummaryResponse"];

export const usageSummaryQueryRoot = ["usage-summary"] as const;

export const usageSummaryQueryKey = (from?: string, to?: string, source?: string, model?: string) =>
	[...usageSummaryQueryRoot, from ?? "unbounded", to ?? "unbounded", source ?? "any", model ?? "any"] as const;

export async function fetchUsageSummary(
	from?: string,
	to?: string,
	source?: string,
	model?: string,
): Promise<UsageSummary | null> {
	const query = {
		...(from ? { from } : {}),
		...(to ? { to } : {}),
		...(source ? { source } : {}),
		...(model ? { model } : {}),
	};
	const { data, error } = await apiClient.GET("/api/v1/usage/summary", {
		params: { query: Object.keys(query).length > 0 ? query : undefined },
	});
	if (error) throw error;
	return data ?? null;
}

export function usageSummaryQueryOptions(from?: string, to?: string, source?: string, model?: string) {
	return {
		queryKey: usageSummaryQueryKey(from, to, source, model),
		queryFn: () => fetchUsageSummary(from, to, source, model),
		retry: 1,
	};
}

export function useUsageSummary(from?: string, to?: string, source?: string, model?: string) {
	return useQuery(usageSummaryQueryOptions(from, to, source, model));
}