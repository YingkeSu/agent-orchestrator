import { useTranslation } from "react-i18next";
import type { SessionRuntimeStats } from "../hooks/useSessionRuntimeStats";
import { formatTokenCount } from "../lib/format-token-count";
import {
	Tooltip,
	TooltipContent,
	TooltipTrigger,
} from "./ui/tooltip";

const UNKNOWN = "—";

/** Compact duration in the stats bar's "20m17s" style; sub-second reads "<1s" (a known zero reads "0s"). */
export function formatRuntimeDurationMs(ms: number): string {
	if (ms === 0) return "0s";
	if (ms < 1000) return "<1s";
	const totalSeconds = Math.round(ms / 1000);
	if (totalSeconds < 60) return `${totalSeconds}s`;
	const minutes = Math.floor(totalSeconds / 60);
	const seconds = totalSeconds % 60;
	if (minutes < 60) return seconds > 0 ? `${minutes}m ${seconds}s` : `${minutes}m`;
	const hours = Math.floor(minutes / 60);
	const restMinutes = minutes % 60;
	return restMinutes > 0 ? `${hours}h ${restMinutes}m` : `${hours}h`;
}

function formatTokensPerSecond(value: number): string {
	return value.toLocaleString(undefined, {
		maximumFractionDigits: value < 10 ? 1 : 0,
	});
}

function Segment({ text }: { text: string }) {
	return <span className="whitespace-nowrap">{text}</span>;
}

function GroupDivider() {
	return (
		<span aria-hidden="true" className="text-border-strong">
			|
		</span>
	);
}

function MetricDivider() {
	return (
		<span aria-hidden="true" className="text-passive">
			·
		</span>
	);
}

/**
 * The per-session runtime stats strip (timing ADR #9): rounds, steps, LLM and
 * tool time, first-token average, output tok/s, cache-hit rate, and total
 * tokens. A metric the daemon could not certify renders the unknown marker
 * ("—"), which is visually distinct from a known zero like "0s".
 */
export function SessionRuntimeStatsBar({ stats }: { stats: SessionRuntimeStats }) {
	const { t } = useTranslation();
	// Tolerate a truncated payload: a field missing from the wire is as
	// unknown as an explicit null, so every metric reads unknown instead of
	// crashing or rendering a partial number.
	const isUnknown = (value: number | null | undefined): value is null | undefined =>
		value === null || value === undefined;
	const coverage = stats.firstTokenCoverage ?? { covered: 0, total: 0 };
	const totals = stats.totals ?? {};
	const duration = (ms: number | null | undefined) =>
		isUnknown(ms) ? UNKNOWN : formatRuntimeDurationMs(ms);
	const firstTokenTitle = isUnknown(stats.firstTokenAvgMs)
		? undefined
		: t("inspector.runtime.firstTokenCoverage", {
			covered: coverage.covered,
			total: coverage.total,
		});

	return (
		<div
			className="flex flex-wrap items-center gap-x-2 gap-y-1 font-mono text-2xs leading-normal text-muted-foreground tabular-nums"
			data-testid="session-runtime-stats"
		>
			<Segment
				text={
					isUnknown(stats.rounds)
						? UNKNOWN
						: t("inspector.runtime.rounds", { count: stats.rounds })
				}
			/>
			<MetricDivider />
			<Segment
				text={
					isUnknown(stats.steps)
						? UNKNOWN
						: t("inspector.runtime.steps", { count: stats.steps })
				}
			/>
			<GroupDivider />
			<Segment text={t("inspector.runtime.llmTime", { duration: duration(stats.llmMs) })} />
			<MetricDivider />
			<Segment text={t("inspector.runtime.toolTime", { duration: duration(stats.toolMs) })} />
			<GroupDivider />
			<Tooltip>
				<TooltipTrigger asChild>
					<span
						className="whitespace-nowrap"
						aria-label={firstTokenTitle}
						tabIndex={firstTokenTitle ? 0 : undefined}
					>
						{t("inspector.runtime.firstTokenAvg", { duration: duration(stats.firstTokenAvgMs) })}
					</span>
				</TooltipTrigger>
				{firstTokenTitle ? <TooltipContent>{firstTokenTitle}</TooltipContent> : null}
			</Tooltip>
			<MetricDivider />
			<Segment
				text={
					isUnknown(stats.outputTokensPerSecond)
						? t("inspector.runtime.tokensPerSecond", { count: UNKNOWN })
						: t("inspector.runtime.tokensPerSecond", {
							count: formatTokensPerSecond(stats.outputTokensPerSecond),
						})
				}
			/>
			<GroupDivider />
			<Segment
				text={
					isUnknown(stats.cacheHitRate)
						? t("inspector.runtime.cacheHitRate", { percent: UNKNOWN })
						: t("inspector.runtime.cacheHitRate", {
							percent: `${(stats.cacheHitRate * 100).toFixed(1)}%`,
						})
				}
			/>
			<GroupDivider />
			<Segment
				text={t("inspector.runtime.totalTokens", {
					count: totals.processedTokens == null
						? UNKNOWN
						: formatTokenCount(totals.processedTokens).replace(/ tok$/, ""),
				})}
			/>
		</div>
	);
}
