import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";
import { useUsageSummary } from "../hooks/useUsageSummary";
import type { MessageKey } from "../i18n/messages";
import { formatCostNanos } from "../lib/format-cost";
import { formatTokenCount } from "../lib/format-token-count";
import { cn } from "../lib/utils";

type RangePreset = "today" | "7d" | "30d";

const PRESETS: { key: RangePreset; labelKey: MessageKey }[] = [
	{ key: "today", labelKey: "usage.range.today" },
	{ key: "7d", labelKey: "usage.range.sevenDays" },
	{ key: "30d", labelKey: "usage.range.thirtyDays" },
];

function rangeFor(preset: RangePreset): { from?: string; to?: string } {
	const now = new Date();
	const to = now.toISOString();
	switch (preset) {
		case "today": {
			const start = new Date(now);
			start.setHours(0, 0, 0, 0);
			return { from: start.toISOString(), to };
		}
		case "7d": {
			const start = new Date(now);
			start.setDate(start.getDate() - 6);
			start.setHours(0, 0, 0, 0);
			return { from: start.toISOString(), to };
		}
		case "30d": {
			const start = new Date(now);
			start.setDate(start.getDate() - 29);
			start.setHours(0, 0, 0, 0);
			return { from: start.toISOString(), to };
		}
	}
}

type MetricCardProps = {
	label: string;
	value: string | null;
};

function MetricCard({ label, value }: MetricCardProps) {
	return (
		<Card size="sm">
			<CardHeader>
				<CardTitle className="text-sm font-medium text-muted-foreground">{label}</CardTitle>
			</CardHeader>
			<CardContent className="text-2xl font-semibold tabular-nums tracking-tight">
				{value ?? "—"}
			</CardContent>
		</Card>
	);
}

export function UsageStatisticsView() {
	const { t } = useTranslation();
	const [preset, setPreset] = useState<RangePreset>("today");
	const range = useMemo(() => rangeFor(preset), [preset]);
	const { data, isLoading, isError } = useUsageSummary(range.from, range.to);

	const totals = data?.totals;
	const totalTokens = totals?.processedTokens ?? 0;
	const cacheHitRate = data?.cacheHitRate;

	return (
		<div className="flex h-full min-h-0 flex-col overflow-y-auto bg-background text-foreground">
			<div className="mx-auto flex w-full max-w-4xl flex-col gap-6 px-6 py-6">
				<header className="flex flex-wrap items-end justify-between gap-4">
					<div className="flex flex-col gap-1">
						<h1 className="text-[22px] font-semibold tracking-tight">{t("usage.title")}</h1>
						<p className="text-sm text-muted-foreground">{t("usage.subtitle")}</p>
					</div>
					<div className="flex items-center gap-1 rounded-lg bg-raised p-1">
						{PRESETS.map((item) => (
							<button
								key={item.key}
								className={cn(
									"rounded-md px-3 py-1.5 text-sm transition-colors",
									preset === item.key
										? "bg-accent text-accent-foreground"
										: "text-muted-foreground hover:bg-interactive-hover hover:text-foreground",
								)}
								onClick={() => setPreset(item.key)}
								type="button"
								aria-pressed={preset === item.key}
							>
								{t(item.labelKey)}
							</button>
						))}
					</div>
				</header>

				{isLoading ? (
					<div className="flex items-center gap-2 text-sm text-muted-foreground">
						<span className="size-4 animate-spin rounded-full border-2 border-border-strong border-t-accent" />
						{t("usage.loading")}
					</div>
				) : isError || !data ? (
					<p className="text-sm text-muted-foreground">{t("usage.loadFailed")}</p>
				) : (
					<div className="flex flex-col gap-6">
						<div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
							<MetricCard label={t("usage.totalTokens")} value={formatTokenCount(totalTokens)} />
							<MetricCard label={t("usage.totalRequests")} value={formatCount(data.requestCount)} />
							<MetricCard
								label={t("usage.totalCost")}
								value={totals?.estimatedCost ? formatCostNanos(totals.estimatedCost.totalNanos) : null}
							/>
						</div>

						<div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
							<MetricCard
								label={t("usage.newInput")}
								value={formatTokens(totals?.uncachedInputTokens)}
							/>
							<MetricCard
								label={t("usage.cachedInput")}
								value={formatTokens(totals?.cachedInputTokens)}
							/>
							<MetricCard label={t("usage.output")} value={formatTokens(totals?.outputTokens)} />
							<MetricCard label={t("usage.totalInput")} value={formatTokens(totals?.inputTokens)} />
						</div>

						<Card size="sm">
							<CardHeader>
								<CardTitle className="text-sm font-medium text-muted-foreground">
									{t("usage.cacheHitRate")}
								</CardTitle>
							</CardHeader>
							<CardContent className="flex flex-col gap-2">
								<div className="flex items-baseline justify-between">
									<span className="text-2xl font-semibold tabular-nums tracking-tight">
										{cacheHitRate === null || cacheHitRate === undefined
											? "—"
											: `${(cacheHitRate * 100).toFixed(1)}%`}
									</span>
									<span className="text-xs text-muted-foreground">{t("usage.cacheHitRateHint")}</span>
								</div>
								<div
									className="h-2 overflow-hidden rounded-full bg-muted"
									role="progressbar"
									aria-valuemin={0}
									aria-valuemax={100}
									aria-valuenow={cacheHitRate === null || cacheHitRate === undefined ? undefined : Math.round(cacheHitRate * 100)}
								>
									<div
										className="h-full rounded-full bg-accent transition-[width] duration-300 ease-out"
										style={{
											width: cacheHitRate === null || cacheHitRate === undefined ? "0%" : `${cacheHitRate * 100}%`,
										}}
									/>
								</div>
							</CardContent>
						</Card>

						{data.requestCount === 0 ? (
							<p className="text-sm text-muted-foreground">{t("usage.emptyRange")}</p>
						) : null}
					</div>
				)}
			</div>
		</div>
	);
}

function formatTokens(value: number | null | undefined): string | null {
	if (value === null || value === undefined) return null;
	return formatTokenCount(value);
}

function formatCount(value: number): string {
	return new Intl.NumberFormat(undefined).format(value);
}
