import { useInfiniteQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient } from "../lib/api-client";

export type UsageRequestLogPage = components["schemas"]["ControllersUsageRequestLogResponse"];

export const usageRequestLogQueryRoot = ["usage-request-log"] as const;

export const usageRequestLogPageSize = 50;

export const usageRequestLogQueryKey = (from?: string, to?: string, source?: string, model?: string) =>
	[
		...usageRequestLogQueryRoot,
		from ?? "unbounded",
		to ?? "unbounded",
		source ?? "any-source",
		model ?? "any-model",
	] as const;

export async function fetchUsageRequestLog(
	from?: string,
	to?: string,
	source?: string,
	model?: string,
	before?: number,
	limit = usageRequestLogPageSize,
): Promise<UsageRequestLogPage> {
	const { data, error } = await apiClient.GET("/api/v1/usage/log", {
		params: {
			query: {
				...(from ? { from } : {}),
				...(to ? { to } : {}),
				...(source ? { source } : {}),
				...(model ? { model } : {}),
				...(before != null ? { before } : {}),
				limit,
			},
		},
	});
	if (error) throw error;
	return data ?? { items: [], nextBeforeId: null };
}

export function useUsageRequestLog(from?: string, to?: string, source?: string, model?: string) {
	return useInfiniteQuery({
		queryKey: usageRequestLogQueryKey(from, to, source, model),
		queryFn: ({ pageParam }) => fetchUsageRequestLog(from, to, source, model, pageParam),
		initialPageParam: undefined as number | undefined,
		getNextPageParam: (lastPage) => lastPage.nextBeforeId ?? undefined,
		select: (data) => data.pages.flatMap((page) => page.items),
		retry: 1,
	});
}
