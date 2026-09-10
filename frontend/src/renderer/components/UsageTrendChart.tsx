import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import {
	CartesianGrid,
	ComposedChart,
	Legend,
	Line,
	ResponsiveContainer,
	Tooltip,
	XAxis,
	YAxis,
} from "recharts";
import type { components } from "../../api/schema";
import { formatCostNanos } from "../lib/format-cost";
import { formatTokenCount } from "../lib/format-token-count";

type TrendBucket = components["schemas"]["UsageTrendBucketResponse"];

type SeriesKey =
	| "uncachedInputTokens"
	| "cacheCreationInputTokens"
	| "cachedInputTokens"
	| "outputTokens"
	| "cost";

const NANOS_PER_DOLLAR = 1_000_000_000;

const tokenAxisTicks = new Intl.NumberFormat(undefined, { notation: "compact" });
const costAxisTicks = new Intl.NumberFormat(undefined, {
	style: "currency",
	currency: "USD",
	notation: "compact",
});

export function UsageTrendChart({
	buckets,
	bucketSize,
}: {
	buckets: TrendBucket[];
	bucketSize: "hour" | "day";
}) {
	const { t } = useTranslation();
	const hourFormatter = useMemo(
		() => new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" }),
		[],
	);
	const dayFormatter = useMemo(
		() => new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" }),
		[],
	);
	const series: { key: SeriesKey; label: string; color: string; yAxisId: "tokens" | "cost" }[] = [
		{ key: "uncachedInputTokens", label: t("usage.newInput"), color: "var(--color-accent)", yAxisId: "tokens" },
		{
			key: "cacheCreationInputTokens",
			label: t("usage.trendCacheCreation"),
			color: "var(--color-status-validating)",
			yAxisId: "tokens",
		},
		{ key: "cachedInputTokens", label: t("usage.cachedInput"), color: "var(--color-status-working)", yAxisId: "tokens" },
		{ key: "outputTokens", label: t("usage.output"), color: "var(--color-status-merged)", yAxisId: "tokens" },
		{ key: "cost", label: t("usage.trendCost"), color: "var(--color-status-in-review)", yAxisId: "cost" },
	];

	const rows = useMemo(
		() =>
			buckets.map((bucket) => ({
				time: new Date(bucket.bucketStart).getTime(),
				uncachedInputTokens: bucket.uncachedInputTokens,
				cacheCreationInputTokens: bucket.cacheCreationInputTokens,
				cachedInputTokens: bucket.cachedInputTokens,
				outputTokens: bucket.outputTokens,
				cost: bucket.costNanos === null || bucket.costNanos === undefined ? null : bucket.costNanos / NANOS_PER_DOLLAR,
				costNanos: bucket.costNanos,
			})),
		[buckets],
	);

	if (rows.length === 0) {
		return <p className="text-sm text-muted-foreground">{t("usage.emptyRange")}</p>;
	}

	return (
		<div className="h-64 w-full" role="img" aria-label={t("usage.trendTitle")}>
			<ResponsiveContainer width="100%" height="100%">
				<ComposedChart data={rows} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
					<CartesianGrid stroke="var(--color-border)" strokeDasharray="3 3" vertical={false} />
					<XAxis
						dataKey="time"
						scale="time"
						type="number"
						domain={["dataMin", "dataMax"]}
						tickFormatter={(ms: number) =>
							bucketSize === "hour" ? hourFormatter.format(ms) : dayFormatter.format(ms)
						}
						tick={{ fontSize: 11, fill: "var(--color-text-muted)" }}
						stroke="var(--color-border)"
						tickLine={false}
						axisLine={{ stroke: "var(--color-border)" }}
						minTickGap={24}
					/>
					<YAxis
						yAxisId="tokens"
						tickFormatter={(v: number) => tokenAxisTicks.format(v)}
						tick={{ fontSize: 11, fill: "var(--color-text-muted)" }}
						stroke="var(--color-border)"
						tickLine={false}
						axisLine={false}
						width={44}
					/>
					<YAxis
						yAxisId="cost"
						orientation="right"
						tickFormatter={(v: number) => costAxisTicks.format(v)}
						tick={{ fontSize: 11, fill: "var(--color-text-muted)" }}
						stroke="var(--color-border)"
						tickLine={false}
						axisLine={false}
						width={56}
					/>
					<Tooltip
						cursor={{ stroke: "var(--color-border-strong)" }}
						content={
							<TrendTooltip
								bucketSize={bucketSize}
								hourFormatter={hourFormatter}
								dayFormatter={dayFormatter}
							/>
						}
					/>
					<Legend wrapperStyle={{ fontSize: 12 }} iconType="plainline" />
					{series.map(({ key, label, color, yAxisId }) => (
						<Line
							key={key}
							name={label}
							type="monotone"
							dataKey={key}
							stroke={color}
							strokeWidth={1.5}
							yAxisId={yAxisId}
							dot={false}
							activeDot={{ r: 3 }}
						/>
					))}
				</ComposedChart>
			</ResponsiveContainer>
		</div>
	);
}

function TrendTooltip({
	active,
	payload,
	label,
	bucketSize,
	hourFormatter,
	dayFormatter,
}: {
	active?: boolean;
	payload?: { dataKey: string; value: number | null; color: string; name: string }[];
	label?: number;
	bucketSize: "hour" | "day";
	hourFormatter: Intl.DateTimeFormat;
	dayFormatter: Intl.DateTimeFormat;
}) {
	const { t } = useTranslation();
	if (!active || !payload || payload.length === 0) return null;
	const time = label === undefined ? null : new Date(label);
	const timeLabel = time
		? bucketSize === "hour"
			? hourFormatter.format(time)
			: dayFormatter.format(time)
		: null;
	return (
		<div className="rounded-md border border-border bg-overlay px-3 py-2 text-xs shadow-sm">
			{timeLabel ? <p className="mb-1 font-medium tabular-nums">{timeLabel}</p> : null}
			<div className="flex flex-col gap-0.5">
				{payload.map((entry) => {
					if (entry.value === null || entry.value === undefined) {
						return (
							<p key={entry.dataKey} className="flex items-center gap-2 text-muted-foreground">
								<span className="h-1.5 w-3 rounded-full" style={{ backgroundColor: entry.color }} />
								{entry.name}
								<span className="ml-auto pl-4">{t("usage.unavailable")}</span>
							</p>
						);
					}
					const formatted =
						entry.dataKey === "cost"
							? formatCostNanos(Math.round(entry.value * NANOS_PER_DOLLAR))
							: formatTokenCount(entry.value);
					return (
						<p key={entry.dataKey} className="flex items-center gap-2 tabular-nums">
							<span className="h-1.5 w-3 rounded-full" style={{ backgroundColor: entry.color }} />
							{entry.name}
							<span className="ml-auto pl-4 text-foreground">{formatted}</span>
						</p>
					);
				})}
			</div>
		</div>
	);
}