import { useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";

export type UsageModelStatsRow = components["schemas"]["UsageModelStatsRow"];
export type UsageProviderStatsRow = components["schemas"]["UsageProviderStatsRow"];

export const usageModelStatsQueryRoot = ["usage-model-stats"] as const;
export const usageProviderStatsQueryRoot = ["usage-provider-stats"] as const;

type UsageAggregateParams = {
	from?: string;
	to?: string;
	source?: string;
	model?: string;
};

function aggregateParams(from?: string, to?: string, source?: string, model?: string): UsageAggregateParams | undefined {
	const params: UsageAggregateParams = {
		...(from ? { from } : {}),
		...(to ? { to } : {}),
		...(source ? { source } : {}),
		...(model ? { model } : {}),
	};
	return Object.keys(params).length > 0 ? params : undefined;
}

export const usageModelStatsQueryKey = (from?: string, to?: string, source?: string, model?: string) =>
	[...usageModelStatsQueryRoot, from ?? "unbounded", to ?? "unbounded", source ?? "all", model ?? "all"] as const;

export const usageProviderStatsQueryKey = (from?: string, to?: string, source?: string, model?: string) =>
	[...usageProviderStatsQueryRoot, from ?? "unbounded", to ?? "unbounded", source ?? "all", model ?? "all"] as const;

export async function fetchUsageModelStats(
	from?: string,
	to?: string,
	source?: string,
	model?: string,
): Promise<UsageModelStatsRow[] | null> {
	const { data, error } = await apiClient.GET("/api/v1/usage/models", {
		params: { query: aggregateParams(from, to, source, model) },
	});
	if (error) throw error;
	return data?.models ?? null;
}

export async function fetchUsageProviderStats(
	from?: string,
	to?: string,
	source?: string,
	model?: string,
): Promise<UsageProviderStatsRow[] | null> {
	const { data, error } = await apiClient.GET("/api/v1/usage/providers", {
		params: { query: aggregateParams(from, to, source, model) },
	});
	if (error) throw error;
	return data?.providers ?? null;
}

export function useUsageModelStats(from?: string, to?: string, source?: string, model?: string) {
	return useQuery({
		queryKey: usageModelStatsQueryKey(from, to, source, model),
		queryFn: () => fetchUsageModelStats(from, to, source, model),
		retry: 1,
	});
}

export function useUsageProviderStats(from?: string, to?: string, source?: string, model?: string) {
	return useQuery({
		queryKey: usageProviderStatsQueryKey(from, to, source, model),
		queryFn: () => fetchUsageProviderStats(from, to, source, model),
		retry: 1,
	});
}