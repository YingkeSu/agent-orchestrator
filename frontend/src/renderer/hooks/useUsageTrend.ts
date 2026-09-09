import { useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";

export type UsageTrend = components["schemas"]["UsageTrendResponse"];
export type UsageTrendBucketSize = "hour" | "day";

export const usageTrendQueryRoot = ["usage-trend"] as const;

export const usageTrendQueryKey = (
	from: string,
	to: string,
	bucket?: UsageTrendBucketSize,
	source?: string,
	model?: string,
) =>
	[
		...usageTrendQueryRoot,
		from,
		to,
		bucket ?? "hour",
		source ?? "",
		model ?? "",
	] as const;

export async function fetchUsageTrend(
	from: string,
	to: string,
	bucket?: UsageTrendBucketSize,
	source?: string,
	model?: string,
): Promise<UsageTrend | null> {
	const { data, error } = await apiClient.GET("/api/v1/usage/trend", {
		params: {
			query: {
				from,
				to,
				...(bucket ? { bucket } : {}),
				...(source ? { source } : {}),
				...(model ? { model } : {}),
			},
		},
	});
	if (error) throw error;
	return data ?? null;
}

export function usageTrendQueryOptions(
	from: string,
	to: string,
	bucket?: UsageTrendBucketSize,
	source?: string,
	model?: string,
) {
	return {
		queryKey: usageTrendQueryKey(from, to, bucket, source, model),
		queryFn: () => fetchUsageTrend(from, to, bucket, source, model),
		retry: 1,
		refetchInterval: 30_000,
		enabled: Boolean(from && to),
	};
}

export function useUsageTrend(
	from: string,
	to: string,
	bucket?: UsageTrendBucketSize,
	source?: string,
	model?: string,
) {
	return useQuery(usageTrendQueryOptions(from, to, bucket, source, model));
}