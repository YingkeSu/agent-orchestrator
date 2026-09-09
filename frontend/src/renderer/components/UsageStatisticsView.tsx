import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Layers } from "lucide-react";
import type { components } from "../../api/schema";
import { AgentAvatar } from "./AgentAvatar";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "./ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";
import { RequestLogTable } from "./RequestLogTable";
import { useUsageSummary } from "../hooks/useUsageSummary";
import { useUsageModelStats, useUsageProviderStats } from "../hooks/useUsageAggregates";
import { UsageAggregateTable, type UsageAggregateRow } from "./UsageAggregateTable";
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

// Sentinel values for the shared select primitive: Radix rejects an empty
// string as an item value, so "all" options use these and map back to "".
const ALL_SOURCES = "__all_sources__";
const ALL_MODELS = "__all_models__";

// Usage source kinds are the certified artifact shapes the daemon persists.
// Known kinds get a friendly label and the harness brand mark; unknown kinds
// fall back to the raw kind so the dropdown still names what the data says.
const SOURCE_LABEL_KEY: Record<string, MessageKey> = {
	claude_main: "usage.sourceKind.claudeMain",
	claude_subagent: "usage.sourceKind.claudeSubagent",
	codex_rollout: "usage.sourceKind.codexRollout",
	kimi_wire: "usage.sourceKind.kimiWire",
};

const SOURCE_PROVIDER: Record<string, string> = {
	claude_main: "claude-code",
	claude_subagent: "claude-code",
	codex_rollout: "codex",
	kimi_wire: "kimi",
};

function sourceLabel(t: (key: MessageKey) => string, kind: string): string {
	const key = SOURCE_LABEL_KEY[kind];
	return key ? t(key) : kind;
}

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
	const [source, setSource] = useState("");
	const [model, setModel] = useState("");
	const range = useMemo(() => rangeFor(preset), [preset]);
	const { data, isLoading, isError } = useUsageSummary(range.from, range.to, source || undefined, model || undefined);
	const modelStats = useUsageModelStats(range.from, range.to, source || undefined, model || undefined);
	const providerStats = useUsageProviderStats(range.from, range.to, source || undefined, model || undefined);

	const modelRows = useMemo(
		() => sortByCostDesc((modelStats.data ?? []).map(toModelRow)),
		[modelStats.data],
	);
	const providerRows = useMemo(
		() => sortByCostDesc((providerStats.data ?? []).map(toProviderRow)),
		[providerStats.data],
	);

	const totals = data?.totals;
	const totalTokens = totals?.processedTokens ?? 0;
	const cacheHitRate = data?.cacheHitRate;
	const sources = data?.sources ?? [];
	const models = data?.models ?? [];

	const selectSource = (value: string) => setSource(value === ALL_SOURCES ? "" : value);
	const selectModel = (value: string) => setModel(value === ALL_MODELS ? "" : value);

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

				<div className="flex flex-wrap items-center gap-3">
					<div className="flex items-center gap-1 rounded-lg bg-raised p-1">
						<Tooltip>
							<TooltipTrigger asChild>
								<button
									type="button"
									className={cn(
										"flex size-8 items-center justify-center rounded-md transition-colors",
										source === ""
											? "bg-accent text-accent-foreground"
											: "text-muted-foreground hover:bg-interactive-hover hover:text-foreground",
									)}
									aria-pressed={source === ""}
									aria-label={t("usage.allSources")}
									onClick={() => setSource("")}
								>
									<Layers className="size-4" aria-hidden="true" />
								</button>
							</TooltipTrigger>
							<TooltipContent>{t("usage.allSources")}</TooltipContent>
						</Tooltip>
						{sources.map((kind) => {
							const label = sourceLabel(t, kind);
							return (
								<Tooltip key={kind}>
									<TooltipTrigger asChild>
										<button
											type="button"
											className={cn(
												"flex size-8 items-center justify-center rounded-md transition-colors",
												source === kind
													? "bg-accent text-accent-foreground"
													: "text-muted-foreground hover:bg-interactive-hover hover:text-foreground",
											)}
											aria-pressed={source === kind}
											aria-label={label}
											onClick={() => setSource(kind)}
										>
											<AgentAvatar provider={SOURCE_PROVIDER[kind] ?? kind} className="size-4" decorative />
										</button>
									</TooltipTrigger>
									<TooltipContent>{label}</TooltipContent>
								</Tooltip>
							);
						})}
					</div>

					<div className="flex items-center gap-2">
						<Select value={source === "" ? ALL_SOURCES : source} onValueChange={selectSource}>
							<SelectTrigger size="sm" aria-label={t("usage.allSources")}>
								<SelectValue placeholder={t("usage.allSources")} />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value={ALL_SOURCES}>{t("usage.allSources")}</SelectItem>
								{sources.map((kind) => (
									<SelectItem key={kind} value={kind}>
										{sourceLabel(t, kind)}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<Select value={model === "" ? ALL_MODELS : model} onValueChange={selectModel}>
							<SelectTrigger size="sm" aria-label={t("usage.allModels")}>
								<SelectValue placeholder={t("usage.allModels")} />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value={ALL_MODELS}>{t("usage.allModels")}</SelectItem>
								{models.map((modelID) => (
									<SelectItem key={modelID} value={modelID}>
										{modelID}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</div>
				</div>

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

						<Tabs defaultValue="models" className="flex flex-col gap-4">
							<TabsList>
								<TabsTrigger value="models">{t("usage.tab.models")}</TabsTrigger>
								<TabsTrigger value="providers">{t("usage.tab.providers")}</TabsTrigger>
							</TabsList>
							<TabsContent value="models">
								<UsageAggregateTable
									variant="models"
									rows={modelRows}
									isLoading={modelStats.isLoading}
									isError={modelStats.isError}
								/>
							</TabsContent>
							<TabsContent value="providers">
								<UsageAggregateTable
									variant="providers"
									rows={providerRows}
									isLoading={providerStats.isLoading}
									isError={providerStats.isError}
								/>
							</TabsContent>
						</Tabs>

						{data.requestCount === 0 ? (
							<p className="text-sm text-muted-foreground">{t("usage.emptyRange")}</p>
						) : null}

						<section className="flex flex-col gap-3">
							<h2 className="text-sm font-medium text-muted-foreground">{t("usage.log.title")}</h2>
							<RequestLogTable
								from={range.from}
								to={range.to}
								source={source || undefined}
								model={model || undefined}
							/>
						</section>
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

function toModelRow(row: components["schemas"]["UsageModelStatsRow"]): UsageAggregateRow {
	return {
		name: row.modelId,
		requestCount: row.requestCount,
		tokens: row.processedTokens,
		totalCostNanos: row.totalCostNanos,
		averageCostPerRequestNanos: row.avgCostPerRequestNanos,
	};
}

function toProviderRow(row: components["schemas"]["UsageProviderStatsRow"]): UsageAggregateRow {
	return {
		name: row.billingProviderId,
		attribution: row.attributionSource,
		requestCount: row.requestCount,
		tokens: row.processedTokens,
		totalCostNanos: row.totalCostNanos,
		averageCostPerRequestNanos: row.avgCostPerRequestNanos,
	};
}

function sortByCostDesc(rows: UsageAggregateRow[]): UsageAggregateRow[] {
	return [...rows].sort((a, b) => {
		const aCost = a.totalCostNanos;
		const bCost = b.totalCostNanos;
		if ((aCost == null) !== (bCost == null)) {
			return aCost == null ? 1 : -1;
		}
		if (aCost != null && bCost != null && aCost !== bCost) {
			return bCost - aCost;
		}
		return a.name < b.name ? -1 : a.name > b.name ? 1 : 0;
	});
}
